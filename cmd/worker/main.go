// SPDX-License-Identifier: Apache-2.0

// Command worker runs the asynchronous task worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/observability"
)

// version is set at build time.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mage2mcp-worker:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	if cfg.Redis.Addr == "" {
		return errors.New("worker: redis.addr is required")
	}
	server := jobs.NewServer(jobs.RedisOptions(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB), 0, logger)
	mux := jobs.NewMux(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("worker starting", "version", version, "redis", cfg.Redis.Addr)
		if err := server.Run(mux); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	logger.Info("worker shutting down")
	server.Shutdown()
	return nil
}
