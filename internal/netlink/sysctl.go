// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package netlink

import (
	"context"
	"os"
	"path/filepath"
)

// ProcSysctl writes per-interface IPv6 settings in the current namespace.
type ProcSysctl struct{}

// SetIPv6 consumes validated names and fixed settings supplied by the reconciler.
func (ProcSysctl) SetIPv6(ctx context.Context, name, setting, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("/proc/sys/net/ipv6/conf", name, setting), []byte(value), 0)
}

var _ Sysctl = ProcSysctl{}
