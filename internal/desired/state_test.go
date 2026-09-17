// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package desired_test

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// Test_State_Validate verifies that every adapter shares the same topology,
// naming, address and MTU safety boundary before kernel operations.
func Test_State_Validate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []desired.Link
		valid bool
	}{
		{name: "empty topology", valid: true},
		{name: "KNI loopback dummy VLAN zero", valid: true, links: []desired.Link{
			{Name: "kni0", MTU: 9000}, {Name: "lo", Kind: desired.LinkKindLoopback},
			{Name: "dummy0", Kind: desired.LinkKindDummy},
			{Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0", MTU: 1500},
		}},
		{name: "onboard Ethernet", links: []desired.Link{{Name: "eth0"}}},
		{name: "KNI without index", links: []desired.Link{{Name: "kni"}}},
		{name: "KNI with parent", links: []desired.Link{{Name: "kni0", Parent: "kni1"}}},
		{name: "unknown kind", links: []desired.Link{{Name: "dummy0", Kind: 99}}},
		{name: "incorrect loopback name", links: []desired.Link{{Name: "loop0", Kind: desired.LinkKindLoopback}}},
		{name: "dummy cannot create KNI", links: []desired.Link{{Name: "kni0", Kind: desired.LinkKindDummy}}},
		{name: "dummy cannot create onboard", links: []desired.Link{{Name: "eth1", Kind: desired.LinkKindDummy}}},
		{name: "dummy with parent", links: []desired.Link{{Name: "dummy0", Kind: desired.LinkKindDummy, Parent: "kni0"}}},
		{name: "empty name", links: []desired.Link{{Kind: desired.LinkKindDummy}}},
		{name: "long name", links: []desired.Link{{Name: "abcdefghijklmnop", Kind: desired.LinkKindDummy}}},
		{name: "unsafe name", links: []desired.Link{{Name: "../dummy0", Kind: desired.LinkKindDummy}}},
		{name: "global sysctl name", links: []desired.Link{{Name: "all", Kind: desired.LinkKindDummy}}},
		{name: "duplicate name", links: []desired.Link{{Name: "kni0"}, {Name: "kni0"}}},
		{name: "negative MTU", links: []desired.Link{{Name: "kni0", MTU: -1}}},
		{name: "MTU disables IPv6", links: []desired.Link{{Name: "kni0", MTU: 1279}}},
		{name: "maximum MTU", valid: true, links: []desired.Link{{Name: "kni0", MTU: 2147483647}}},
		{name: "invalid prefix", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{{}}}}},
		{name: "unspecified IPv4", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/32")}}}},
		{name: "unspecified IPv4 default prefix", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}}}},
		{name: "unspecified IPv6", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("::/128")}}}},
		{name: "mapped IPv6 prefix", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("::ffff:192.0.2.1/120")}}}},
		{name: "multicast IPv4", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("224.0.0.1/32")}}}},
		{name: "multicast IPv6", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("ff02::1/128")}}}},
		{name: "IPv6 loopback on KNI", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{netip.MustParsePrefix("::1/128")}}}},
		{name: "IPv6 loopback on lo", valid: true, links: []desired.Link{{Name: "lo", Kind: desired.LinkKindLoopback, Addresses: []netip.Prefix{netip.MustParsePrefix("::1/128")}}}},
		{name: "minimum MTU", valid: true, links: []desired.Link{{Name: "kni0", MTU: 1280}}},
		{name: "stacked VLAN", links: []desired.Link{{Name: "kni0"}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0"}, {Name: "v1", Kind: desired.LinkKindVLAN, Parent: "v0"}}},
		{name: "IPv6 prefix conflict", links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{
			netip.MustParsePrefix("fe80::1/64"), netip.MustParsePrefix("fe80::1/128"),
		}}}},
		{name: "identical prefixes", valid: true, links: []desired.Link{{Name: "kni0", Addresses: []netip.Prefix{
			netip.MustParsePrefix("fe80::1/64"), netip.MustParsePrefix("fe80::1/64"),
		}}}},
		{name: "missing parent", links: []desired.Link{{Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0"}}},
		{name: "non KNI parent", links: []desired.Link{{Name: "lo", Kind: desired.LinkKindLoopback}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "lo"}}},
		{name: "VLAN ID out of range", links: []desired.Link{{Name: "kni0"}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0", VLANID: 4095}}},
		{name: "negative VLAN ID", links: []desired.Link{{Name: "kni0"}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0", VLANID: -1}}},
		{name: "child exceeds parent MTU", links: []desired.Link{{Name: "kni0", MTU: 1500}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0", MTU: 9000}}},
		{name: "duplicate VLAN identity", links: []desired.Link{{Name: "kni0"}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0"}, {Name: "v1", Kind: desired.LinkKindVLAN, Parent: "kni0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (desired.State{Links: tc.links}).Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
