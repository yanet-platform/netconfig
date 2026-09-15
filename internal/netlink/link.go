// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package netlink

import (
	"fmt"

	vnetlink "github.com/vishvananda/netlink"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// validateLink checks compatibility with state left by a previous startup.
func validateLink(wanted desired.Link, link, parent vnetlink.Link) error {
	if link == nil {
		return fmt.Errorf("kernel link %q is not available yet", wanted.Name)
	}
	switch wanted.Kind {
	case desired.LinkKindKNI:
		tap, isTap := link.(*vnetlink.Tuntap)
		if link.Type() != "device" && (!isTap || tap.Mode != vnetlink.TUNTAP_MODE_TAP) {
			return fmt.Errorf("KNI %q has incompatible type %q", wanted.Name, link.Type())
		}
	case desired.LinkKindDummy:
		if link.Type() != "dummy" {
			return fmt.Errorf("dummy %q has incompatible type %q", wanted.Name, link.Type())
		}
	case desired.LinkKindVLAN:
		vlan, ok := link.(*vnetlink.Vlan)
		if !ok || parent == nil || vlan.ParentIndex != parent.Attrs().Index ||
			vlan.VlanId != wanted.VLANID || vlan.VlanProtocol != vnetlink.VLAN_PROTOCOL_8021Q {
			return fmt.Errorf("VLAN %q has incompatible type, parent, ID or protocol", wanted.Name)
		}
	}
	return nil
}
