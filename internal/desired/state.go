// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package desired

import "net/netip"

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
