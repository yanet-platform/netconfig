// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package desired

import (
	"net/netip"
	"slices"
)

// LinkKind distinguishes kernel-owned links from created interfaces.
type LinkKind int

const (
	LinkKindKNI LinkKind = iota
	LinkKindVLAN
	LinkKindLoopback
	LinkKindDummy
)

// State is the startup configuration, ordered lexicographically by link name.
type State struct {
	Links []Link
}

// Clone detaches every mutable part of the desired configuration.
func (m State) Clone() State {
	links := slices.Clone(m.Links)
	for idx := range links {
		links[idx].Addresses = slices.Clone(links[idx].Addresses)
		if links[idx].AcceptRA != nil {
			value := *links[idx].AcceptRA
			links[idx].AcceptRA = &value
		}
	}
	return State{Links: links}
}

// Link describes an explicitly managed interface.
type Link struct {
	Name      string
	Kind      LinkKind
	Parent    string
	VLANID    int
	MTU       int
	Addresses []netip.Prefix
	// AcceptRA leaves the kernel setting untouched when unspecified.
	AcceptRA      *bool
	IPv6LinkLocal bool
}
