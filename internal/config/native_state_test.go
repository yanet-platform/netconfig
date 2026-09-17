// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"net/netip"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yanet-platform/netconfig/internal/desired"
)

func Test_Config_LoadFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/native.yaml")
	require.NoError(t, err)
	state, err := loadSource(t, "native", string(data))
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
			state, err := loadSource(t, "native", tc.data)
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
			state, err := loadSource(t, "native", data)
			require.Error(t, err)
			require.Equal(t, desired.State{}, state)
		})
	}
}
