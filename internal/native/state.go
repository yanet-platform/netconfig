// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package native

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// Load normalizes and validates previously parsed startup configuration.
func (m *Config) Load() (desired.State, error) {
	state := desired.State{}
	for _, section := range []struct {
		Links map[string]LinkConfig
		Kind  desired.LinkKind
	}{
		{m.Ethernets, desired.LinkKindKNI},
		{m.VLANs, desired.LinkKindVLAN},
		{m.DummyDevices, desired.LinkKindDummy},
	} {
		for _, name := range slices.Sorted(maps.Keys(section.Links)) {
			config := section.Links[name]
			link := desired.Link{
				Name: name, Kind: section.Kind, Parent: config.Link,
				MTU: config.MTU, IPv6LinkLocal: true, AcceptRA: config.AcceptRA,
			}
			if section.Kind == desired.LinkKindKNI && name == "lo" {
				link.Kind = desired.LinkKindLoopback
			}
			if config.ID != nil {
				link.VLANID = *config.ID
			}
			if config.LinkLocal != nil {
				link.IPv6LinkLocal = len(*config.LinkLocal) != 0
			}
			for _, address := range config.Addresses {
				prefix, err := netip.ParsePrefix(address)
				if err != nil {
					return desired.State{}, fmt.Errorf("native link %q: address %q: %w", name, address, err)
				}
				link.Addresses = append(link.Addresses, prefix)
			}
			state.Links = append(state.Links, link)
		}
	}
	slices.SortFunc(state.Links, func(left, right desired.Link) int {
		return cmp.Compare(left.Name, right.Name)
	})
	if err := state.Validate(); err != nil {
		return desired.State{}, fmt.Errorf("native: %w", err)
	}
	return state, nil
}
