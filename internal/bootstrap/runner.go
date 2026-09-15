// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// Config bounds the delay between unsuccessful setup passes.
type Config struct {
	InitialBackoff time.Duration `yaml:"initial_backoff"`
	MaxBackoff     time.Duration `yaml:"max_backoff"`
}

// Validate rejects delays that could spin or overflow the retry schedule.
func (m *Config) Validate() error {
	if m == nil || m.InitialBackoff <= 0 || m.MaxBackoff < m.InitialBackoff {
		return errors.New("retry requires 0 < initial_backoff <= max_backoff")
	}
	return nil
}

// Reconciler permits partial setup while other configured links are missing.
type Reconciler interface {
	Create(context.Context, desired.State) error
	Configure(context.Context, desired.State) error
}

// Runner owns one immutable startup configuration and stops after success.
type Runner struct {
	state      desired.State
	reconciler Reconciler
	config     Config
	log        *zap.Logger
}

// NewRunner validates and detaches configuration before the first setup pass.
func NewRunner(state desired.State, reconciler Reconciler, config *Config, log *zap.Logger) (*Runner, error) {
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("startup configuration: %w", err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if reconciler == nil {
		return nil, errors.New("bootstrap reconciler is nil")
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Runner{state: state.Clone(), reconciler: reconciler, config: *config, log: log}, nil
}

// Run retries both setup phases until success or cancellation without rollback.
//
// Missing links cannot block configuration of available ones. A successful
// invocation performs no further kernel operations.
func (m *Runner) Run(ctx context.Context) error {
	delay := m.config.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := errors.Join(m.reconciler.Create(ctx, m.state), m.reconciler.Configure(ctx, m.state))
		if err == nil {
			m.log.Info("configured startup interfaces")
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m.log.Warn("interface setup failed; retrying", zap.Duration("backoff", delay), zap.Error(err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay > m.config.MaxBackoff-delay {
			delay = m.config.MaxBackoff
		} else {
			delay = min(2*delay, m.config.MaxBackoff)
		}
	}
}
