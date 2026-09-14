// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	vnetlink "github.com/vishvananda/netlink"
)

// commandBinary selects the independently built executable used by CLI tests.
func commandBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NETCONFIG_BINARY")
	if path == "" {
		t.Skip("requires NETCONFIG_BINARY pointing to a freshly built netconfig")
	}
	return path
}

// Test_Command_Validation verifies the real executable accepts validation-only
// input and fails promptly on invalid startup configuration and arguments.
func Test_Command_Validation(t *testing.T) {
	binary := commandBinary(t)
	for _, tc := range []struct {
		name      string
		input     string
		arguments []string
		valid     bool
	}{
		{name: "check absent KNI", input: "source: native\nnative: {ethernets: {kni8: {}}}", arguments: []string{"-check"}, valid: true},
		{name: "unknown source", input: "source: invalid", arguments: []string{"-once"}},
		{name: "malformed topology", input: "source: native\nnative: {ethernets: {eth0: {}}}", arguments: []string{"-once"}},
		{name: "mapped prefix", input: "source: native\nnative: {ethernets: {kni8: {addresses: ['::ffff:192.0.2.1/120']}}}", arguments: []string{"-once"}},
		{name: "unspecified address", input: "source: native\nnative: {dummy-devices: {dummy0: {addresses: ['0.0.0.0/32']}}}", arguments: []string{"-check"}},
		{name: "unknown flag", input: "source: native\nnative: {}", arguments: []string{"-unknown"}},
		{name: "positional argument", input: "source: native\nnative: {}", arguments: []string{"unexpected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.input), 0o600))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			arguments := append([]string{"-config", path}, tc.arguments...)
			output, err := exec.CommandContext(ctx, binary, arguments...).CombinedOutput()
			require.NoError(t, ctx.Err(), "%s", output)
			if tc.valid {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "%s", output)
			}
		})
	}
}

// runningCommand joins the subprocess before exposing its exit status.
type runningCommand struct {
	Command *exec.Cmd
	LogPath string
	Done    chan struct{}
	Err     error
}

// startCommand captures logs in a file that can be safely inspected while live.
func startCommand(t *testing.T, binary, path string, arguments ...string) *runningCommand {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	command := exec.CommandContext(ctx, binary, append([]string{"-config", path}, arguments...)...)
	logPath := filepath.Join(t.TempDir(), "process.log")
	log, err := os.Create(logPath)
	require.NoError(t, err)
	command.Stdout, command.Stderr = log, log
	process := &runningCommand{Command: command, LogPath: logPath, Done: make(chan struct{})}
	if err := command.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	go func() {
		process.Err = command.Wait()
		_ = log.Close()
		close(process.Done)
	}()
	t.Cleanup(func() { cancel(); <-process.Done })
	return process
}

// waitConfigured requires the executable to acknowledge the complete bootstrap.
func (m *runningCommand) waitConfigured(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(m.LogPath)
		return err == nil && strings.Contains(string(data), `"msg":"configured startup interfaces"`)
	}, 10*time.Second, 10*time.Millisecond)
}

// waitExited observes orderly process termination without tearing down links.
func (m *runningCommand) waitExited(t *testing.T) {
	t.Helper()
	select {
	case <-m.Done:
		data, _ := os.ReadFile(m.LogPath)
		require.NoError(t, m.Err, "%s", data)
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
}

// Test_Command_NetnsLifecycle verifies delayed KNI, partial progress, immutable
// input, orderly shutdown, restart reconciliation and the absence of drift repair.
func Test_Command_NetnsLifecycle(t *testing.T) {
	binary := commandBinary(t)
	if os.Getenv("NETCONFIG_NETNS_TESTS") != "1" {
		t.Skip("requires a disposable network namespace")
	}
	for _, source := range []string{"native", "netplan"} {
		t.Run(source, func(t *testing.T) {
			handle, err := vnetlink.NewHandle()
			require.NoError(t, err)
			t.Cleanup(handle.Close)
			root := t.TempDir()
			configPath, netplanPath := filepath.Join(root, "config.yaml"), filepath.Join(root, "netplan.yaml")
			writeConfig := func(mtu int) {
				t.Helper()
				body := fmt.Sprintf(`
ethernets:
  kni8: {mtu: 1500, addresses: [192.0.2.8/24], link-local: [], accept-ra: false}
vlans:
  vlan8: {link: kni8, id: 100, mtu: 1500, addresses: ['2001:db8:8::8/64'], link-local: []}
dummy-devices:
  dummy8: {mtu: %d, addresses: [198.51.100.8/32], link-local: []}
`, mtu)
				input := "source: " + source + "\nretry: {initial_backoff: 10ms, max_backoff: 20ms}\n"
				if source == "native" {
					input += "native:\n  " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ") + "\n"
				} else {
					input += fmt.Sprintf("netplan_path: %q\n", netplanPath)
					require.NoError(t, os.WriteFile(netplanPath, []byte("network:\n  version: 2\n  "+strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ")+"\n"), 0o600))
				}
				require.NoError(t, os.WriteFile(configPath, []byte(input), 0o600))
			}
			writeConfig(1500)
			t.Cleanup(func() {
				if dummy, err := handle.LinkByName("dummy8"); err == nil {
					_ = handle.LinkDel(dummy)
				}
			})
			first := startCommand(t, binary, configPath)
			require.Eventually(t, func() bool {
				link, err := handle.LinkByName("dummy8")
				return err == nil && link.Attrs().MTU == 1500
			}, 5*time.Second, 10*time.Millisecond)
			dummy, err := handle.LinkByName("dummy8")
			require.NoError(t, err)
			foreign, err := vnetlink.ParseAddr("198.51.100.99/32")
			require.NoError(t, err)
			require.NoError(t, handle.AddrAdd(dummy, foreign))
			// Pending retries retain the original input even after invalid edits.
			require.NoError(t, os.WriteFile(configPath, []byte("invalid: ["), 0o600))
			if source == "netplan" {
				require.NoError(t, os.Remove(netplanPath))
			}
			tap := &vnetlink.Tuntap{LinkAttrs: vnetlink.LinkAttrs{Name: "kni8"}, Mode: vnetlink.TUNTAP_MODE_TAP, Queues: 1}
			require.NoError(t, handle.LinkAdd(tap))
			t.Cleanup(func() {
				_ = handle.LinkDel(tap)
				for _, descriptor := range tap.Fds {
					_ = descriptor.Close()
				}
			})
			first.waitConfigured(t)
			vlan, err := handle.LinkByName("vlan8")
			require.NoError(t, err)
			originalVLANIndex := vlan.Attrs().Index
			require.NoError(t, first.Command.Process.Signal(syscall.SIGTERM))
			first.waitExited(t)
			vlan, err = handle.LinkByName("vlan8")
			require.NoError(t, err)
			require.Equal(t, originalVLANIndex, vlan.Attrs().Index)

			writeConfig(9000)
			second := startCommand(t, binary, configPath)
			second.waitConfigured(t)
			dummy, err = handle.LinkByName("dummy8")
			require.NoError(t, err)
			require.Equal(t, 9000, dummy.Attrs().MTU)
			addresses, err := handle.AddrList(dummy, vnetlink.FAMILY_V4)
			require.NoError(t, err)
			require.Len(t, addresses, 2)
			vlan, err = handle.LinkByName("vlan8")
			require.NoError(t, err)
			require.Equal(t, originalVLANIndex, vlan.Attrs().Index)
			require.NoError(t, handle.LinkDel(vlan))
			require.Never(t, func() bool {
				_, err := handle.LinkByName("vlan8")
				return err == nil
			}, 200*time.Millisecond, 10*time.Millisecond)
			require.NoError(t, second.Command.Process.Signal(syscall.SIGTERM))
			second.waitExited(t)

			third := startCommand(t, binary, configPath, "-once")
			third.waitExited(t)
			vlan, err = handle.LinkByName("vlan8")
			require.NoError(t, err)
			require.NotEqual(t, originalVLANIndex, vlan.Attrs().Index)
			addresses, err = handle.AddrList(dummy, vnetlink.FAMILY_V4)
			require.NoError(t, err)
			require.Len(t, addresses, 2)
		})
	}
}
