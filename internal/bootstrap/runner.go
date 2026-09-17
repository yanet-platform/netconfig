// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/yanet-platform/netconfig/internal/desired"
)

// Config bounds the delay between unsuccessful setup passes.
type Config struct {
	InitialBackoff time.Duration `yaml:"initial_backoff"`
	MaxBackoff     time.Duration `yaml:"max_backoff"`
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

// NewRunner takes ownership of state and retry policy validated by config loading.
func NewRunner(state desired.State, reconciler Reconciler, config Config, log *zap.Logger) *Runner {
	return &Runner{state: state, reconciler: reconciler, config: config, log: log}
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
