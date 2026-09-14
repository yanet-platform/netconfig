// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package netplan

import "github.com/yanet-platform/netconfig/internal/desired"

// Source reads the external Netplan document selected at startup.
type Source struct {
	Path string
}

// Load returns independently owned, validated interface configuration.
func (m *Source) Load() (desired.State, error) {
	return ParseFile(m.Path)
}

var _ desired.Source = (*Source)(nil)
