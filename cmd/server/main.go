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
	"strings"
	"syscall"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/cloudflare"
	"github.com/shuaiZend/mage2mcp/internal/core/config"
	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/ga4"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/observability"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/security/jwt"
	"github.com/shuaiZend/mage2mcp/internal/security/rbac"
	"github.com/shuaiZend/mage2mcp/internal/storage"
	"github.com/shuaiZend/mage2mcp/internal/unified"
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
	if err := registerModules(cfg, reg); err != nil {
		return err
	}
	var auditor audit.Recorder = audit.NewMemory()
	var confirmStore confirm.Store = confirm.NewMemoryStore()
	var store *storage.Store
	if cfg.Database.URL != "" {
		store, err = storage.Open(context.Background(), storage.Config{
			URL: cfg.Database.URL, MaxConns: cfg.Database.MaxConns,
		})
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Migrate(context.Background()); err != nil {
			return err
		}
		auditor = storage.NewAuditRecorder(store)
		confirmStore = storage.NewConfirmStore(store)
		logger.Info("durable audit and confirmations enabled", "store", "postgresql")
	} else {
		logger.Warn("database.url is not set: audit is in memory and lost on restart")
	}
	confirmSvc, err := confirm.NewService(confirmStore, confirm.DefaultTTL)
	if err != nil {
		return err
	}
	policy, err := rbac.New([]rbac.Grant{
		{Role: "viewer", Tool: "*", Scope: rbac.ScopeRead, Effect: rbac.EffectAllow},
		{Role: "analyst", Tool: "*", Scope: rbac.ScopeRead, Effect: rbac.EffectAllow},
	})
	if err != nil {
		return err
	}
	mcpServer, err := server.NewMCP(server.Options{
		Version:   version,
		Logger:    logger,
		Registry:  reg,
		Authorize: authorizeFunc(cfg, policy),
		Confirm:   confirmSvc,
		Audit: func(ctx context.Context, tool, instance, phase, state, code string) {
			id, _ := auth.IdentityFromContext(ctx)
			subject := "anonymous"
			class := "anonymous"
			if id != nil {
				subject, class = id.Subject, id.Class
			}
			_ = auditor.Record(ctx, audit.Event{
				Subject: subject, IdentityClass: class, Tool: tool, Instance: instance,
				Decision: "allow", Phase: phase, State: state, ErrorCode: code,
			})
		},
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

// configuredInstances lists instance ids per module for platform.info.
func configuredInstances(cfg config.Config) map[string][]string {
	out := map[string][]string{}
	add := func(module string, instances []config.Instance) {
		if len(instances) == 0 {
			return
		}
		ids := make([]string, 0, len(instances))
		for _, inst := range instances {
			ids = append(ids, inst.ID)
		}
		out[module] = ids
	}
	add("magento", cfg.Modules.Magento.Instances)
	add("cloudflare", cfg.Modules.Cloudflare.Instances)
	add("ga4", cfg.Modules.GA4.Instances)
	return out
}

// ga4TokenSource accepts either an inline service-account JSON document or a
// static bearer token. Service-account JSON is the production form.
func ga4TokenSource(credential string) (ga4.TokenSource, error) {
	trimmed := strings.TrimSpace(credential)
	if strings.HasPrefix(trimmed, "{") {
		return ga4.NewServiceAccountTokenSource([]byte(trimmed), nil)
	}
	if trimmed == "" {
		return nil, fmt.Errorf("ga4 credential is empty")
	}
	return ga4.StaticToken(trimmed), nil
}

// authorizeFunc returns nil when authentication is disabled (loopback-only
// development), so the dev quickstart can call tools without an identity.
func authorizeFunc(cfg config.Config, policy *rbac.Policy) func(context.Context, string, string) error {
	if !cfg.Auth.Enabled {
		return nil
	}
	return func(ctx context.Context, tool, instance string) error {
		id, ok := auth.IdentityFromContext(ctx)
		if !ok {
			return errs.New(errs.CodeUnauthenticated, "no verified identity")
		}
		return policy.Authorize(id, tool, instance, rbac.ScopeRead)
	}
}

// registerModules builds one service per enabled module and registers its tools.
// Multi-instance routing is not wired yet: a module with more than one instance
// is a startup error rather than a silently wrong credential choice.
func registerModules(cfg config.Config, reg *registry.Registry) error {
	var cloudflareSvc *cloudflare.Service
	var ga4Svc *ga4.Service
	if cfg.Modules.Magento.Enabled {
		services := make(map[string]*magento.Service, len(cfg.Modules.Magento.Instances))
		for _, inst := range cfg.Modules.Magento.Instances {
			signer, err := magentoSigner(inst.Credential)
			if err != nil {
				return fmt.Errorf("magento %s: %w", inst.ID, err)
			}
			client, err := magento.NewClient(magento.ClientConfig{
				BaseURL: inst.Endpoint, StoreCode: inst.StoreCode, Signer: signer,
			})
			if err != nil {
				return fmt.Errorf("magento %s: %w", inst.ID, err)
			}
			svc, err := magento.NewService(client)
			if err != nil {
				return err
			}
			services[inst.ID] = svc
		}
		if len(services) == 1 {
			for _, svc := range services {
				if err := magento.Register(reg, svc); err != nil {
					return err
				}
				if cfg.Tools.Writes {
					if err := magento.RegisterWrites(reg, svc); err != nil {
						return err
					}
				}
			}
		} else {
			// Write tools are not yet resolver-aware: refuse rather than apply a
			// write to the wrong store.
			if cfg.Tools.Writes {
				return fmt.Errorf("magento: write tools require exactly one store in this release")
			}
			resolver, err := magento.NewResolver(services)
			if err != nil {
				return err
			}
			if err := magento.RegisterWithResolver(reg, resolver); err != nil {
				return err
			}
		}
	}
	if cfg.Modules.Cloudflare.Enabled {
		services := make(map[string]*cloudflare.Service, len(cfg.Modules.Cloudflare.Instances))
		for _, inst := range cfg.Modules.Cloudflare.Instances {
			client, err := cloudflare.NewClient(cloudflare.Config{
				APIBaseURL: "https://api.cloudflare.com/client/v4",
				APIToken:   inst.Credential, ZoneID: inst.ZoneID, AccountID: inst.AccountID,
			})
			if err != nil {
				return fmt.Errorf("cloudflare %s: %w", inst.ID, err)
			}
			services[inst.ID] = cloudflare.NewService(client)
		}
		if len(services) == 1 {
			for _, svc := range services {
				cloudflareSvc = svc
			}
			if err := cloudflare.Register(reg, cloudflareSvc); err != nil {
				return err
			}
		} else {
			resolver, err := cloudflare.NewResolver(services)
			if err != nil {
				return err
			}
			if err := cloudflare.RegisterWithResolver(reg, resolver); err != nil {
				return err
			}
		}
	}
	if cfg.Modules.GA4.Enabled {
		services := make(map[string]*ga4.Service, len(cfg.Modules.GA4.Instances))
		for _, inst := range cfg.Modules.GA4.Instances {
			tokenSource, err := ga4TokenSource(inst.Credential)
			if err != nil {
				return fmt.Errorf("ga4 %s: %w", inst.ID, err)
			}
			svc, err := ga4.NewService(ga4.Config{PropertyID: inst.PropertyID, TokenSource: tokenSource})
			if err != nil {
				return fmt.Errorf("ga4 %s: %w", inst.ID, err)
			}
			services[inst.ID] = svc
		}
		if len(services) == 1 {
			for _, svc := range services {
				ga4Svc = svc
			}
			if err := ga4.Register(reg, ga4Svc); err != nil {
				return err
			}
			if err := ga4.RegisterExtra(reg, ga4Svc); err != nil {
				return err
			}
		} else {
			resolver, err := ga4.NewResolver(services)
			if err != nil {
				return err
			}
			if err := ga4.RegisterWithResolver(reg, resolver); err != nil {
				return err
			}
		}
	}
	if len(cfg.Bindings) > 0 {
		if cloudflareSvc == nil || ga4Svc == nil {
			return fmt.Errorf("bindings require both cloudflare and ga4 modules to be enabled")
		}
		bindings := make([]unified.Binding, 0, len(cfg.Bindings))
		for _, b := range cfg.Bindings {
			bindings = append(bindings, unified.Binding{ID: b.ID, Magento: b.Magento, Cloudflare: b.Cloudflare, GA4: b.GA4})
		}
		unifiedSvc, err := unified.NewService(bindings,
			cloudflareTrafficAdapter{svc: cloudflareSvc}, ga4SessionsAdapter{svc: ga4Svc})
		if err != nil {
			return err
		}
		if err := unified.Register(reg, unifiedSvc); err != nil {
			return err
		}
	}
	return nil
}

// cloudflareTrafficAdapter adapts the Cloudflare service to the unified module.
type cloudflareTrafficAdapter struct{ svc *cloudflare.Service }

func (a cloudflareTrafficAdapter) Traffic(ctx context.Context, _ string, from, to time.Time) (unified.Traffic, error) {
	rows, err := a.svc.Traffic(ctx, cloudflare.TrafficQuery{Since: from, Until: to, GroupBy: "hostname", Limit: 1000})
	if err != nil {
		return unified.Traffic{}, err
	}
	out := unified.Traffic{Rows: len(rows)}
	for _, row := range rows {
		out.Requests += row.Requests
		out.CachedRequests += row.CachedRequests
	}
	return out, nil
}

// ga4SessionsAdapter adapts the GA4 service to the unified module.
type ga4SessionsAdapter struct{ svc *ga4.Service }

func (a ga4SessionsAdapter) Sessions(ctx context.Context, _ string, from, to time.Time) (unified.Sessions, error) {
	end := to.Add(-time.Second)
	result, err := a.svc.Traffic(ctx, ga4.TrafficQuery{
		StartDate: from.Format("2006-01-02"),
		EndDate:   end.Format("2006-01-02"),
		Metrics:   []string{"sessions", "totalUsers"},
	})
	if err != nil {
		return unified.Sessions{}, err
	}
	return unified.Sessions{
		Sessions: result.Totals["sessions"],
		Users:    result.Totals["totalUsers"],
		TimeZone: result.TimeZone,
		Sampled:  result.Sampled,
	}, nil
}

// magentoSigner parses the credential form. "bearer:<token>" is supported for
// stores that enable integration-as-bearer; "oauth1:<key>:<secret>:<token>:<token-secret>"
// is the production form.
func magentoSigner(credential string) (magento.Signer, error) {
	if strings.HasPrefix(credential, "bearer:") {
		return &magento.BearerSigner{Token: strings.TrimPrefix(credential, "bearer:")}, nil
	}
	if strings.HasPrefix(credential, "oauth1:") {
		parts := strings.Split(strings.TrimPrefix(credential, "oauth1:"), ":")
		if len(parts) != 4 {
			return nil, fmt.Errorf("oauth1 credential must be oauth1:<key>:<secret>:<token>:<token-secret>")
		}
		return magento.NewOAuth1Signer(parts[0], parts[1], parts[2], parts[3])
	}
	return nil, fmt.Errorf("unsupported credential form; use bearer:<token> or oauth1:<key>:<secret>:<token>:<token-secret>")
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
