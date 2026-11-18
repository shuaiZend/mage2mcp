// SPDX-License-Identifier: Apache-2.0

// Command server runs the mage2mcp MCP gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/observability"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/jwt"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mage2mcp:", err)
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
	metrics := observability.NewMetrics()

	reg := registry.New()
	mcpServer, err := server.NewMCP(server.Options{
		Version:  version,
		Logger:   logger,
		Registry: reg,
	})
	if err != nil {
		return err
	}

	authMiddleware, err := buildAuth(cfg, logger)
	if err != nil {
		return err
	}

	handler := server.Handler(mcpServer, server.Options{
		Version:        version,
		Logger:         logger,
		Registry:       reg,
		AuthMiddleware: authMiddleware,
		Metrics:        metrics.Handler(),
		MaxBodyBytes:   cfg.Server.MaxBodyBytes,
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("gateway listening", "addr", cfg.Server.Listen, "version", version, "auth_enabled", cfg.Auth.Enabled)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace)
	defer cancel()
	logger.Info("shutting down", "grace", cfg.Server.ShutdownGrace.String())
	return httpServer.Shutdown(shutdownCtx)
}

func buildAuth(cfg config.Config, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if !cfg.Auth.Enabled {
		if !isLoopbackListen(cfg.Server.Listen) {
			return nil, fmt.Errorf("auth is disabled but server.listen %q is not loopback; refusing to start", cfg.Server.Listen)
		}
		logger.Warn("authentication disabled: development mode only, loopback listener")
		return nil, nil
	}
	verifier, err := jwt.NewVerifier(jwt.Config{
		Issuer:   cfg.Auth.Issuer,
		Audience: cfg.Auth.Audience,
		JWKSURL:  cfg.Auth.JWKSURL,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return auth.Middleware(verifier, logger), nil
}

func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
