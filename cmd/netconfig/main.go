// Copyright 2026 YANDEX LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	vnetlink "github.com/vishvananda/netlink"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"

	"github.com/yanet-platform/netconfig/internal/bootstrap"
	"github.com/yanet-platform/netconfig/internal/config"
	"github.com/yanet-platform/netconfig/internal/desired"
	netreconcile "github.com/yanet-platform/netconfig/internal/netlink"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("netconfig", flag.ContinueOnError)
	path := flags.String("config", "/etc/netconfig/config.yaml", "startup configuration file")
	check := flags.Bool("check", false, "validate configuration without accessing kernel networking")
	once := flags.Bool("once", false, "exit after successful setup instead of waiting for shutdown")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	startup, err := config.ParseFile(*path)
	if err != nil {
		return err
	}
	state, err := startup.Load()
	if err != nil {
		return err
	}
	log, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	log.Info("validated startup configuration", zap.String("source", startup.Source))
	if *check {
		return nil
	}
	if err := setup(ctx, state, &startup.Retry, log); err != nil {
		return err
	}
	if !*once {
		<-ctx.Done()
	}
	return nil
}

func setup(ctx context.Context, state desired.State, retry *bootstrap.Config, log *zap.Logger) error {
	handle, err := vnetlink.NewHandle(unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("open netlink: %w", err)
	}
	defer handle.Close()
	if err := handle.SetSocketTimeout(5 * time.Second); err != nil {
		return fmt.Errorf("configure netlink socket timeout: %w", err)
	}
	reconciler := netreconcile.NewReconciler(handle, netreconcile.ProcSysctl{})
	runner := bootstrap.NewRunner(state, reconciler, *retry, log)
	return runner.Run(ctx)
}
