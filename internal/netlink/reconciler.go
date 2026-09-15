// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package netlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"

	vnetlink "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// Backend permits interface setup and narrowly scoped IPv6LL cleanup.
type Backend interface {
	LinkList() ([]vnetlink.Link, error)
	LinkAdd(vnetlink.Link) error
	LinkSetMTU(vnetlink.Link, int) error
	LinkSetUp(vnetlink.Link) error
	AddrList(vnetlink.Link, int) ([]vnetlink.Addr, error)
	AddrReplace(vnetlink.Link, *vnetlink.Addr) error
	AddrDel(vnetlink.Link, *vnetlink.Addr) error
}

// Sysctl writes a per-interface IPv6 setting.
type Sysctl interface {
	SetIPv6(context.Context, string, string, string) error
}

// Reconciler separates interface creation from configuration during bootstrap.
//
// One worker retries these operations until success, then stops calling them.
// Create and Configure consume state already validated by the input loader.
// Netconfig is the only configurator; links are not replaced during a pass.
type Reconciler struct {
	backend Backend
	sysctl  Sysctl
}

// NewReconciler uses the supplied kernel handle for startup mutations.
func NewReconciler(backend Backend, sysctl Sysctl) *Reconciler {
	return &Reconciler{backend: backend, sysctl: sysctl}
}

func (m *Reconciler) readLinks(ctx context.Context) (map[string]vnetlink.Link, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	links, err := m.backend.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	existing := map[string]vnetlink.Link{}
	for _, link := range links {
		existing[link.Attrs().Name] = link
	}
	return existing, nil
}

// Create ensures dummy and VLAN existence without configuring existing links.
//
// KNI and loopback must be supplied by the kernel/dataplane. A VLAN whose MTU
// exceeds its observed parent waits for parent configuration on a later retry.
func (m *Reconciler) Create(ctx context.Context, state desired.State) error {
	existing, err := m.readLinks(ctx)
	if err != nil {
		return err
	}
	var failures error
	for _, wanted := range state.Links {
		failures = errors.Join(failures, m.createLink(ctx, wanted, state, existing))
	}
	return failures
}

func (m *Reconciler) createLink(ctx context.Context, wanted desired.Link, state desired.State, existing map[string]vnetlink.Link) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := existing[wanted.Parent]
	if link := existing[wanted.Name]; link != nil {
		return validateLink(wanted, link, parent)
	}
	if wanted.Kind == desired.LinkKindKNI || wanted.Kind == desired.LinkKindLoopback {
		return fmt.Errorf("kernel link %q is not available yet", wanted.Name)
	}
	attributes := vnetlink.LinkAttrs{Name: wanted.Name, MTU: wanted.MTU}
	var created vnetlink.Link = &vnetlink.Dummy{LinkAttrs: attributes}
	if wanted.Kind == desired.LinkKindVLAN {
		if err := validateLink(desired.Link{Name: wanted.Parent}, parent, nil); err != nil {
			return fmt.Errorf("create VLAN %q: parent %q: %w", wanted.Name, wanted.Parent, err)
		}
		attributes.ParentIndex = parent.Attrs().Index
		if attributes.MTU == 0 {
			attributes.MTU = parent.Attrs().MTU
			for _, desiredParent := range state.Links {
				if desiredParent.Name == wanted.Parent && desiredParent.MTU != 0 {
					attributes.MTU = desiredParent.MTU
				}
			}
		}
		created = &vnetlink.Vlan{
			LinkAttrs: attributes, VlanId: wanted.VLANID,
			VlanProtocol: vnetlink.VLAN_PROTOCOL_8021Q,
		}
	}
	if err := m.backend.LinkAdd(created); err != nil {
		return fmt.Errorf("create link %q: %w", wanted.Name, err)
	}
	return nil
}

// Configure attempts every available link without waiting for missing ones.
//
// MTU increases precede child changes; parent decreases follow them and refuse
// to clamp any remaining oversized child. Partial setup is safe to retry.
func (m *Reconciler) Configure(ctx context.Context, state desired.State) error {
	existing, err := m.readLinks(ctx)
	if err != nil {
		return err
	}
	available := map[string]vnetlink.Link{}
	var failures error
	for _, wanted := range state.Links {
		link := existing[wanted.Name]
		if err := validateLink(wanted, link, existing[wanted.Parent]); err != nil {
			failures = errors.Join(failures, fmt.Errorf("configure link %q: %w", wanted.Name, err))
			continue
		}
		if err := validateEffectiveMTU(wanted, state, existing); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		available[wanted.Name] = link
	}
	configurationOrder := slices.Clone(state.Links)
	slices.SortStableFunc(configurationOrder, func(left, right desired.Link) int {
		leftVLAN, rightVLAN := left.Kind == desired.LinkKindVLAN, right.Kind == desired.LinkKindVLAN
		if leftVLAN == rightVLAN {
			return 0
		}
		if leftVLAN {
			return 1
		}
		return -1
	})
	for _, wanted := range configurationOrder {
		link := available[wanted.Name]
		if link == nil || wanted.Parent != "" && available[wanted.Parent] == nil {
			continue
		}
		if wanted.Kind != desired.LinkKindKNI || existing[wanted.Name].Attrs().MTU < wanted.MTU {
			if err := m.ensureMTU(ctx, wanted, existing); err != nil {
				failures = errors.Join(failures, err)
				continue
			}
		}
		if err := m.configureLink(ctx, wanted, link); err != nil {
			failures = errors.Join(failures, fmt.Errorf("configure link %q: %w", wanted.Name, err))
		}
	}
	for _, wanted := range state.Links {
		if available[wanted.Name] != nil && wanted.Kind == desired.LinkKindKNI {
			failures = errors.Join(failures, m.ensureMTU(ctx, wanted, existing))
		}
	}
	return failures
}

func validateEffectiveMTU(wanted desired.Link, state desired.State, existing map[string]vnetlink.Link) error {
	mtu := wanted.MTU
	if mtu == 0 {
		mtu = existing[wanted.Name].Attrs().MTU
	}
	if err := desired.ValidateMTU(mtu); err != nil {
		return fmt.Errorf("link %q: %w", wanted.Name, err)
	}
	if wanted.Kind == desired.LinkKindVLAN {
		parentMTU := existing[wanted.Parent].Attrs().MTU
		for _, parent := range state.Links {
			if parent.Name == wanted.Parent && parent.MTU != 0 {
				parentMTU = parent.MTU
			}
		}
		if mtu > parentMTU {
			return fmt.Errorf("VLAN %q MTU %d exceeds parent MTU %d", wanted.Name, mtu, parentMTU)
		}
	}
	return nil
}

func (m *Reconciler) ensureMTU(ctx context.Context, wanted desired.Link, existing map[string]vnetlink.Link) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if wanted.MTU == 0 {
		return nil
	}
	link := existing[wanted.Name]
	if link.Attrs().MTU == wanted.MTU {
		return nil
	}
	if wanted.Kind == desired.LinkKindKNI && link.Attrs().MTU > wanted.MTU {
		for _, child := range existing {
			// A veth link index names its peer, not an MTU-dependent child.
			if child.Type() != "veth" && child.Attrs().ParentIndex == link.Attrs().Index && child.Attrs().MTU > wanted.MTU {
				return fmt.Errorf("child %q exceeds desired parent MTU", child.Attrs().Name)
			}
		}
	}
	if err := m.backend.LinkSetMTU(link, wanted.MTU); err != nil {
		return fmt.Errorf("set MTU on %q: %w", wanted.Name, err)
	}
	// Keep this pass's snapshot in sync for the later parent-decrease check.
	link.Attrs().MTU = wanted.MTU
	return nil
}

func (m *Reconciler) configureLink(ctx context.Context, wanted desired.Link, link vnetlink.Link) error {
	settings := []struct{ Name, Value string }{{"addr_gen_mode", "1"}}
	if wanted.IPv6LinkLocal {
		settings[0].Value = "0"
	}
	if wanted.AcceptRA != nil {
		value := "0"
		if *wanted.AcceptRA {
			// Router advertisements remain usable while forwarding is enabled.
			value = "2"
		}
		settings = append(settings, struct{ Name, Value string }{"accept_ra", value})
	}
	settings = append(settings, struct{ Name, Value string }{"disable_ipv6", "0"})
	for _, setting := range settings {
		if err := m.sysctl.SetIPv6(ctx, wanted.Name, setting.Name, setting.Value); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		if err := m.backend.LinkSetUp(link); err != nil {
			return err
		}
	}
	addresses, err := m.backend.AddrList(link, vnetlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("list addresses: %w", err)
	}
	for _, desired := range wanted.Addresses {
		if err := ctx.Err(); err != nil {
			return err
		}
		present := false
		for _, address := range addresses {
			if !address.IP.Equal(desired.Addr().AsSlice()) {
				continue
			}
			bits, _ := address.Mask.Size()
			if bits != desired.Bits() && desired.Addr().Is6() {
				return fmt.Errorf("IPv6 address %s has incompatible prefix length %d", desired.Addr(), bits)
			}
			if desired.Addr().Is6() && address.Flags&unix.IFA_F_DADFAILED != 0 {
				if err := m.backend.AddrDel(link, &address); err != nil {
					return fmt.Errorf("remove failed-DAD address %s: %w", desired, err)
				}
				continue
			}
			// A static address must survive after the startup worker exits.
			present = present || bits == desired.Bits() &&
				address.ValidLft == int(^uint32(0)) && address.PreferedLft == int(^uint32(0)) &&
				address.Flags&unix.IFA_F_DEPRECATED == 0
		}
		if present {
			continue
		}
		address := vnetlink.Addr{IPNet: &net.IPNet{
			IP: desired.Addr().AsSlice(), Mask: net.CIDRMask(desired.Bits(), desired.Addr().BitLen()),
		}}
		if err := m.backend.AddrReplace(link, &address); err != nil {
			return fmt.Errorf("ensure address %s: %w", desired, err)
		}
	}
	addresses, err = m.backend.AddrList(link, vnetlink.FAMILY_V6)
	if err != nil {
		return fmt.Errorf("check IPv6 address readiness: %w", err)
	}
	linkLocalReady := false
	for _, address := range addresses {
		ready := address.Flags&(unix.IFA_F_DADFAILED|unix.IFA_F_TENTATIVE|unix.IFA_F_DEPRECATED) == 0
		linkLocalReady = linkLocalReady || address.IP.IsLinkLocalUnicast() && ready
		if !ready && slices.ContainsFunc(wanted.Addresses, func(prefix netip.Prefix) bool {
			return address.IP.Equal(prefix.Addr().AsSlice())
		}) {
			return fmt.Errorf("desired IPv6 address %s has not completed duplicate address detection", address.IP)
		}
	}
	if wanted.Kind != desired.LinkKindLoopback && wanted.IPv6LinkLocal && !linkLocalReady {
		return errors.New("IPv6 link-local address is not ready; waiting for carrier and duplicate address detection")
	}
	if wanted.Kind != desired.LinkKindLoopback && !wanted.IPv6LinkLocal {
		return m.removeUnlistedIPv6LL(ctx, wanted, link, addresses)
	}
	return ctx.Err()
}

func (m *Reconciler) removeUnlistedIPv6LL(ctx context.Context, wanted desired.Link, link vnetlink.Link, addresses []vnetlink.Addr) error {
	for _, address := range addresses {
		if !address.IP.IsLinkLocalUnicast() {
			continue
		}
		if slices.ContainsFunc(wanted.Addresses, func(prefix netip.Prefix) bool {
			return address.IP.Equal(prefix.Addr().AsSlice())
		}) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.backend.AddrDel(link, &address); err != nil {
			return fmt.Errorf("remove unlisted IPv6 link-local address: %w", err)
		}
	}
	return ctx.Err()
}

var _ Backend = (*vnetlink.Handle)(nil)
