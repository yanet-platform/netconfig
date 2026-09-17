// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package bootstrap_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/yanet-platform/netconfig/internal/bootstrap"
	"github.com/yanet-platform/netconfig/internal/desired"
)

type setupStub struct {
	CreateFunc    func(context.Context, desired.State) error
	ConfigureFunc func(context.Context, desired.State) error
}

func (m *setupStub) Create(ctx context.Context, state desired.State) error {
	return m.CreateFunc(ctx, state)
}

func (m *setupStub) Configure(ctx context.Context, state desired.State) error {
	return m.ConfigureFunc(ctx, state)
}

func Test_Runner_PartialSetupAndBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := 0
		var passes []time.Duration
		started := time.Now()
		reconciler := &setupStub{
			CreateFunc: func(context.Context, desired.State) error {
				created++
				if created < 3 {
					return errors.New("missing KNI")
				}
				return nil
			},
			ConfigureFunc: func(context.Context, desired.State) error {
				passes = append(passes, time.Since(started))
				if len(passes) < 5 {
					return errors.New("DAD pending")
				}
				return nil
			},
		}
		runner := bootstrap.NewRunner(desired.State{}, reconciler,
			bootstrap.Config{InitialBackoff: time.Millisecond, MaxBackoff: 3 * time.Millisecond}, zap.NewNop())
		require.NoError(t, runner.Run(t.Context()))
		require.Equal(t, 5, created)
		require.Equal(t, []time.Duration{0, time.Millisecond, 3 * time.Millisecond, 6 * time.Millisecond, 9 * time.Millisecond}, passes)
	})
}

func Test_Runner_Cancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		configured := 0
		reconciler := &setupStub{
			CreateFunc:    func(context.Context, desired.State) error { return errors.New("missing KNI") },
			ConfigureFunc: func(context.Context, desired.State) error { configured++; return nil },
		}
		runner := bootstrap.NewRunner(desired.State{}, reconciler,
			bootstrap.Config{InitialBackoff: time.Hour, MaxBackoff: time.Hour}, zap.NewNop())
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx) }()
		synctest.Wait() // The worker is now blocked on the retry timer.
		require.Equal(t, 1, configured)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.ErrorIs(t, runner.Run(ctx), context.Canceled)
		require.Equal(t, 1, configured)
	})
}
