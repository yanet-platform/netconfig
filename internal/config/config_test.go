// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yanet-platform/netconfig/internal/config"
	"github.com/yanet-platform/netconfig/internal/desired"
)

// Test_Config_Selection verifies that both sources produce the same startup
// state and native configuration never requires an external Netplan file.
func Test_Config_Selection(t *testing.T) {
	body := "ethernets: {kni0: {mtu: 1500, addresses: ['192.0.2.1/24']}}"
	native, err := config.Parse([]byte("source: native\nnative: {" + body + "}"))
	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, native.Retry.InitialBackoff)
	require.Equal(t, 30*time.Second, native.Retry.MaxBackoff)
	first, err := native.Load()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "netplan.yaml")
	require.NoError(t, os.WriteFile(path, []byte("network: {version: 2, "+body+"}"), 0o600))
	netplan, err := config.Parse(fmt.Appendf(nil, "source: netplan\nnetplan_path: %q", path))
	require.NoError(t, err)
	second, err := netplan.Load()
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NoError(t, os.Remove(path))
	_, err = native.Load()
	require.NoError(t, err)
	_, err = netplan.Load()
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Test_Config_InvalidSchema verifies that ambiguous input and malformed retry
// settings cannot silently fall back to another source or busy-loop.
func Test_Config_InvalidSchema(t *testing.T) {
	for _, input := range []string{
		"", "null", "[]", "{}", "source: ''", "source: other", "source: null",
		"source: native", "source: native\nnative: null", "source: native\nnative: []",
		"source: native\nnative: {}\nnetplan_path: ''",
		"source: native\nnative: {}\nnetplan_path: /tmp/file",
		"source: netplan\nnative: {}", "source: netplan\nnative: null",
		"source: netplan\nnetplan_path: ''", "source: netplan\nnetplan_path: null",
		"source: netplan\nnetplan_path: 123", "source: netplan\nunknown: true",
		"source: netplan\nsource: native", "source: netplan\n---\nsource: netplan",
		"source: netplan\nretry: null", "source: netplan\nretry: {unknown: 1s}",
		"source: netplan\nretry: {initial_backoff: 0s}",
		"source: netplan\nretry: {initial_backoff: -1s}",
		"source: netplan\nretry: {initial_backoff: null}",
		"source: netplan\nretry: {initial_backoff: 10s, max_backoff: 1s}",
	} {
		t.Run(input, func(t *testing.T) {
			parsed, err := config.Parse([]byte(input))
			require.Error(t, err)
			require.Nil(t, parsed)
		})
	}
}

// Test_Config_MappedPrefixes verifies that both sources reject mapped addresses
// during loading, before a caller can construct the kernel bootstrap.
func Test_Config_MappedPrefixes(t *testing.T) {
	for _, source := range []string{"native", "netplan"} {
		for _, prefix := range []string{"::ffff:192.0.2.1/120", "::ffff:192.0.2.2/24"} {
			t.Run(source+"/"+prefix, func(t *testing.T) {
				body := "dummy-devices: {dummy0: {addresses: ['" + prefix + "']}}"
				input := "source: native\nnative: {" + body + "}"
				if source == "netplan" {
					path := filepath.Join(t.TempDir(), "netplan.yaml")
					require.NoError(t, os.WriteFile(path, []byte("network: {version: 2, "+body+"}"), 0o600))
					input = fmt.Sprintf("source: netplan\nnetplan_path: %q", path)
				}
				parsed, err := config.Parse([]byte(input))
				require.NoError(t, err)
				state, err := parsed.Load()
				require.ErrorContains(t, err, "IPv4-mapped IPv6 prefix")
				require.Equal(t, desired.State{}, state)
			})
		}
	}
}
