// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package netlink_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	vnetlink "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/yanet-platform/netconfig/internal/desired"
	netreconcile "github.com/yanet-platform/netconfig/internal/netlink"
)

// Script only observations and failures that are awkward to induce in Linux.
// Creation, MTU, lifetimes, activation and retries use the real kernel tests.
// Unscripted calls panic through the embedded interface instead of simulating Linux.
type observations struct {
	netreconcile.Backend
	links         []vnetlink.Link
	before, after []vnetlink.Addr
	writes        []string
	settings      []string
	failure       string
	cancel        context.CancelFunc
}

var rejected = errors.New("injected kernel failure")

func (m *observations) result(operation string) error {
	if operation == m.failure {
		return rejected
	}
	return nil
}

func (m *observations) LinkList() ([]vnetlink.Link, error) {
	return m.links, m.result("links")
}

func (m *observations) AddrList(_ vnetlink.Link, family int) ([]vnetlink.Addr, error) {
	if family == vnetlink.FAMILY_V6 {
		return m.after, m.result("readiness")
	}
	return m.before, m.result("addresses")
}

func (m *observations) AddrReplace(_ vnetlink.Link, address *vnetlink.Addr) error {
	m.writes = append(m.writes, "replace:"+address.String())
	return m.result("replace")
}

func (m *observations) AddrDel(_ vnetlink.Link, address *vnetlink.Addr) error {
	m.writes = append(m.writes, "delete:"+address.String())
	return m.result("delete")
}

func (m *observations) SetIPv6(ctx context.Context, _, setting, value string) error {
	m.settings = append(m.settings, setting+"="+value)
	if m.cancel != nil {
		m.cancel()
	}
	return errors.Join(ctx.Err(), m.result("sysctl"))
}

func observedKNI() *observations {
	return &observations{links: []vnetlink.Link{&vnetlink.Device{LinkAttrs: vnetlink.LinkAttrs{
		Name: "kni0", Index: 1, MTU: 1500, Flags: net.FlagUp,
	}}}}
}

func mustAddr(value string) vnetlink.Addr {
	address, err := vnetlink.ParseAddr(value)
	if err != nil {
		panic(err)
	}
	address.ValidLft, address.PreferedLft = int(^uint32(0)), int(^uint32(0))
	return *address
}

func Test_Reconciler_AddressTransitions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   int
		failure string
		pending bool
	}{
		{name: "ready"},
		{name: "tentative", flags: unix.IFA_F_TENTATIVE, pending: true},
		{name: "repair failed DAD", flags: unix.IFA_F_DADFAILED},
		{name: "failed DAD delete error", flags: unix.IFA_F_DADFAILED, failure: "delete"},
		{name: "failed DAD replace error", flags: unix.IFA_F_DADFAILED, failure: "replace"},
		{name: "link dump", failure: "links"},
		{name: "address dump", failure: "addresses"},
		{name: "readiness dump", failure: "readiness"},
		{name: "policy failure", failure: "sysctl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := observedKNI()
			address, unlisted := mustAddr("fe80::f1/64"), mustAddr("fe80::abcd/64")
			address.Flags = tc.flags
			backend.before = []vnetlink.Addr{address, unlisted}
			if !tc.pending {
				address.Flags = 0
			}
			backend.after = []vnetlink.Addr{address, unlisted}
			backend.failure = tc.failure
			state := desired.State{Links: []desired.Link{{Name: "kni0", AcceptRA: new(false),
				Addresses: []netip.Prefix{netip.MustParsePrefix("fe80::f1/64")}}}}
			err := netreconcile.NewReconciler(backend, backend).Configure(t.Context(), state)
			if tc.failure != "" || tc.pending {
				require.Error(t, err)
				require.NotContains(t, backend.writes, "delete:fe80::abcd/64")
				if tc.failure != "" {
					require.ErrorIs(t, err, rejected)
				}
			} else {
				require.NoError(t, err)
				writes := []string{"delete:fe80::abcd/64"}
				if tc.flags == unix.IFA_F_DADFAILED {
					writes = append([]string{"delete:fe80::f1/64", "replace:fe80::f1/64"}, writes...)
				}
				require.Equal(t, writes, backend.writes)
				require.Equal(t, []string{"addr_gen_mode=1", "accept_ra=0", "disable_ipv6=0"}, backend.settings)
			}
		})
	}
}

func Test_Reconciler_AutomaticLinkLocalReadiness(t *testing.T) {
	for _, flags := range []int{0, unix.IFA_F_TENTATIVE, unix.IFA_F_DADFAILED, unix.IFA_F_DEPRECATED, -1} {
		t.Run(strconv.Itoa(flags), func(t *testing.T) {
			backend := observedKNI()
			foreign := mustAddr("2001:db8::99/64")
			foreign.Flags = unix.IFA_F_TENTATIVE
			backend.after = []vnetlink.Addr{foreign}
			if flags != -1 {
				address := mustAddr("fe80::abcd/64")
				address.Flags = flags
				backend.after = append(backend.after, address)
			}
			err := netreconcile.NewReconciler(backend, backend).Configure(t.Context(),
				desired.State{Links: []desired.Link{{Name: "kni0", IPv6LinkLocal: true}}})
			if flags == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "link-local address is not ready")
			}
			require.Empty(t, backend.writes)
		})
	}
}

func Test_Reconciler_IncompatibleOrCancelled(t *testing.T) {
	for _, problem := range []string{"late KNI", "TUN", "veth", "dummy", "prefix conflict", "cancelled", "cancel during policy"} {
		t.Run(problem, func(t *testing.T) {
			backend := observedKNI()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			attrs := *backend.links[0].Attrs()
			switch problem {
			case "late KNI":
				backend.links = nil
			case "TUN":
				backend.links[0] = &vnetlink.Tuntap{LinkAttrs: attrs, Mode: vnetlink.TUNTAP_MODE_TUN}
			case "veth":
				backend.links[0] = &vnetlink.Veth{LinkAttrs: attrs}
			case "dummy":
				backend.links[0] = &vnetlink.Dummy{LinkAttrs: attrs}
			case "prefix conflict":
				backend.before = []vnetlink.Addr{mustAddr("fe80::f1/128")}
			case "cancelled":
				cancel()
			case "cancel during policy":
				backend.cancel = cancel
			}
			err := netreconcile.NewReconciler(backend, backend).Configure(ctx, desired.State{Links: []desired.Link{
				{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("fe80::f1/64")}},
			}})
			require.Error(t, err)
			require.Empty(t, backend.writes)
		})
	}
}
