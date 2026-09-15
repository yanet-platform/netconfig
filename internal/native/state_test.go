// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package native_test

import (
	"net/netip"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/yanet-platform/netconfig/internal/desired"
	"github.com/yanet-platform/netconfig/internal/native"
)

func Test_Config_LoadFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/native.yaml")
	require.NoError(t, err)
	var config native.Config
	require.NoError(t, yaml.Unmarshal(data, &config))
	state, err := config.Load()
	require.NoError(t, err)
	acceptRA := false
	require.Equal(t, desired.State{Links: []desired.Link{
		{Name: "dummy0", Kind: desired.LinkKindDummy, Addresses: []netip.Prefix{netip.MustParsePrefix("198.51.100.1/32")}},
		{Name: "kni0", Kind: desired.LinkKindKNI, MTU: 1500, IPv6LinkLocal: true, AcceptRA: &acceptRA},
		{Name: "kni0.100", Kind: desired.LinkKindVLAN, Parent: "kni0", VLANID: 100, MTU: 1500, IPv6LinkLocal: true, AcceptRA: &acceptRA,
			Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.2/24"), netip.MustParsePrefix("2001:db8:100::2/64")}},
		{Name: "lo", Kind: desired.LinkKindLoopback, IPv6LinkLocal: true, Addresses: []netip.Prefix{netip.MustParsePrefix("2001:db8::1/128")}},
	}}, state)
}

func Test_Config_LoadOwnership(t *testing.T) {
	var config native.Config
	require.NoError(t, yaml.Unmarshal([]byte("ethernets: {kni0: {accept-ra: false, addresses: ['192.0.2.7/24'], link-local: [ipv6]}}"), &config))
	first, err := config.Load()
	require.NoError(t, err)
	second, err := config.Load()
	require.NoError(t, err)
	second.Links[0].Addresses[0] = netip.MustParsePrefix("2001:db8::2/64")
	*second.Links[0].AcceptRA = true
	link := config.Ethernets["kni0"]
	link.Addresses[0] = "198.51.100.2/32"
	*link.AcceptRA = true
	*link.LinkLocal = nil
	require.Equal(t, "192.0.2.7/24", first.Links[0].Addresses[0].String())
	require.False(t, *first.Links[0].AcceptRA)
	require.True(t, first.Links[0].IPv6LinkLocal)
}

func Test_Config_LoadDefaults(t *testing.T) {
	for _, tc := range []struct {
		data string
		want desired.State
	}{
		{data: "{}"},
		{data: "ethernets: {kni0: {}}", want: desired.State{Links: []desired.Link{{Name: "kni0", IPv6LinkLocal: true}}}},
		{data: "ethernets: {kni0: {mtu: 0}}", want: desired.State{Links: []desired.Link{{Name: "kni0", IPv6LinkLocal: true}}}},
		{data: "ethernets: {kni0: {mtu: 03000}}", want: desired.State{Links: []desired.Link{{Name: "kni0", MTU: 3000, IPv6LinkLocal: true}}}},
		{data: "ethernets: {kni0: {}}\nvlans: {v0: {id: 0, link: kni0}}", want: desired.State{Links: []desired.Link{
			{Name: "kni0", IPv6LinkLocal: true}, {Name: "v0", Kind: desired.LinkKindVLAN, Parent: "kni0", IPv6LinkLocal: true},
		}}},
	} {
		t.Run(tc.data, func(t *testing.T) {
			var config native.Config
			require.NoError(t, yaml.Unmarshal([]byte(tc.data), &config))
			state, err := config.Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, state)
		})
	}
}

// The semantic matrix lives in desired; exercise only the adapter boundary here.
func Test_Config_LoadInvalidInput(t *testing.T) {
	for _, data := range []string{
		"ethernets: {kni0: {addresses: [invalid]}}",
		"ethernets: {eth0: {}}",
		"ethernets: {kni0: {}}\nvlans: {shared0: {id: 0, link: kni0}}\ndummy-devices: {shared0: {}}",
	} {
		t.Run(data, func(t *testing.T) {
			var config native.Config
			require.NoError(t, yaml.Unmarshal([]byte(data), &config))
			state, err := config.Load()
			require.Error(t, err)
			require.Equal(t, desired.State{}, state)
		})
	}
}
