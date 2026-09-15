// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package bootstrap_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/yanet-platform/netconfig/internal/bootstrap"
	"github.com/yanet-platform/netconfig/internal/desired"
)

// setupStub independently controls both phases without kernel operations.
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

// Test_Runner_PartialSetupAndOwnership verifies that creation failure cannot
// strand available links and caller mutations cannot change retry input.
func Test_Runner_PartialSetupAndOwnership(t *testing.T) {
	acceptRA := false
	state := desired.State{Links: []desired.Link{{
		Name: "kni0", AcceptRA: &acceptRA,
		Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/24")},
	}}}
	expectedRA := false
	expected := desired.State{Links: []desired.Link{{
		Name: "kni0", AcceptRA: &expectedRA,
		Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/24")},
	}}}
	creationCalls, configurationCalls := 0, 0
	reconciler := &setupStub{
		CreateFunc: func(ctx context.Context, state desired.State) error {
			require.Equal(t, expected, state)
			creationCalls++
			if creationCalls < 3 {
				return errors.New("KNI is missing")
			}
			return nil
		},
		ConfigureFunc: func(ctx context.Context, state desired.State) error {
			require.Equal(t, expected, state)
			configurationCalls++
			if configurationCalls < 5 {
				return errors.New("IPv6 DAD is pending")
			}
			return nil
		},
	}
	core, logs := observer.New(zap.InfoLevel)
	runner, err := bootstrap.NewRunner(state, reconciler,
		&bootstrap.Config{InitialBackoff: time.Millisecond, MaxBackoff: 3 * time.Millisecond},
		zap.New(core),
	)
	require.NoError(t, err)
	state.Links[0].Name = "eth0"
	state.Links[0].Addresses[0] = netip.Prefix{}
	acceptRA = true
	started := time.Now()
	require.NoError(t, runner.Run(t.Context()))
	require.GreaterOrEqual(t, time.Since(started), 9*time.Millisecond)
	require.Equal(t, 5, creationCalls)
	require.Equal(t, creationCalls, configurationCalls)
	require.Equal(t, 1, logs.FilterMessage("configured startup interfaces").Len())
	var delays []time.Duration
	for _, entry := range logs.FilterLevelExact(zap.WarnLevel).All() {
		delays = append(delays, entry.ContextMap()["backoff"].(time.Duration))
	}
	require.Equal(t, []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 3 * time.Millisecond}, delays)
}

// Test_Runner_Cancellation verifies that pending setup is interruptible even
// when the retry delay is long, with no new pass after cancellation.
func Test_Runner_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	configured := make(chan struct{})
	reconciler := &setupStub{
		CreateFunc:    func(context.Context, desired.State) error { return errors.New("missing KNI") },
		ConfigureFunc: func(context.Context, desired.State) error { close(configured); return nil },
	}
	runner, err := bootstrap.NewRunner(desired.State{}, reconciler,
		&bootstrap.Config{InitialBackoff: time.Hour, MaxBackoff: time.Hour}, nil,
	)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-configured:
	case <-time.After(time.Second):
		t.Fatal("configuration was blocked by missing KNI")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry delay")
	}
	require.ErrorIs(t, runner.Run(ctx), context.Canceled)
}

// Test_Runner_InvalidConfig verifies that malformed desired state and retry
// policy are rejected before either setup phase can be invoked.
func Test_Runner_InvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  desired.State
		config *bootstrap.Config
	}{
		{name: "zero retry", config: &bootstrap.Config{}},
		{name: "invalid topology", state: desired.State{Links: []desired.Link{{Name: "eth0"}}}, config: &bootstrap.Config{InitialBackoff: 1, MaxBackoff: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := bootstrap.NewRunner(tc.state, &setupStub{}, tc.config, nil)
			require.Error(t, err)
			require.Nil(t, runner)
		})
	}
}
