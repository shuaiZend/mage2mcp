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
	"strconv"
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
	var healthcheck, devInsecure bool
	flag.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flag.BoolVar(&healthcheck, "healthcheck", false, "probe the local /healthz endpoint and exit")
	flag.BoolVar(&devInsecure, "dev-insecure", false, "allow authentication to be disabled on a non-loopback listener (local containers only)")
	flag.Parse()

	if healthcheck {
		return runHealthcheck()
	}
	if !devInsecure && strings.EqualFold(os.Getenv("MAGE2MCP_DEV_INSECURE"), "true") {
		devInsecure = true
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	metrics := observability.NewMetrics()

	var auditor audit.Recorder = audit.NewMemory()
	var confirmStore confirm.Store = confirm.NewMemoryStore()
	var store *storage.Store
	var importQueue magento.ImportQueue
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
		importQueue = storage.NewJobQueue(store)
		// Money-moving tools fail closed without the durable operation ledger:
		// an unguarded capture, refund or order placement is worse than a
		// disabled tool.
		magento.ConfigureOperations(storage.NewOperationLedger(store))
		logger.Info("durable audit and confirmations enabled", "store", "postgresql")
	} else {
		logger.Warn("database.url is not set: audit is in memory and lost on restart")
	}

	reg := registry.New()
	if err := registerModules(cfg, reg, importWiring{Queue: importQueue, Batches: store}); err != nil {
		return err
	}
	confirmSvc, err := confirm.NewService(confirmStore, confirm.DefaultTTL)
	if err != nil {
		return err
	}
	policy, err := rbac.DefaultPolicy()
	if err != nil {
		return err
	}
	// The operation ledger is the authority on business outcome; the job queue
	// stays the durable record of execution. Wiring it here means every write
	// tool is gated at the one middleware choke point.
	var ledger *storage.OperationLedger
	if store != nil {
		ledger = storage.NewOperationLedger(store)
	}
	mcpServer, err := server.NewMCP(server.Options{
		Version:           version,
		Logger:            logger,
		Registry:          reg,
		Authorize:         authorizeFunc(cfg, policy, logger),
		Confirm:           confirmSvc,
		Instances:         configuredInstances(cfg),
		BeginOperation:    operationBegin(ledger),
		FinishOperation:   operationFinish(ledger),
		SelfLedgeredTools: selfLedgeredTools(),
		Audit: func(ctx context.Context, tool, instance, phase, state, code string) {
			id, _ := auth.IdentityFromContext(ctx)
			subject := "anonymous"
			class := "anonymous"
			if id != nil {
				subject, class = id.Subject, id.Class
			}
			// SEC-06: a denied call is a denial, not an allow.
			_ = auditor.Record(ctx, audit.Event{
				Subject: subject, IdentityClass: class, Tool: tool, Instance: instance,
				Decision: auditDecision(phase, state), Phase: phase, State: state, ErrorCode: code,
			})
		},
	})
	if err != nil {
		return err
	}

	authMiddleware, err := buildAuth(cfg, logger, devInsecure)
	if err != nil {
		return err
	}

	handler := server.Handler(mcpServer, server.Options{
		Version:        version,
		Logger:         logger,
		Registry:       reg,
		AuthMiddleware: authMiddleware,
		Metrics:        metrics.Handler(),
		// A metrics endpoint on a non-loopback listener is reconnaissance for an
		// unauthenticated caller, so it goes behind the same middleware as /mcp.
		MetricsRequireAuth: !isLoopbackListen(cfg.Server.Listen),
		MaxBodyBytes:       cfg.Server.MaxBodyBytes,
		// QA-06: readiness must consult the durable store when one is configured.
		// Without it a gateway whose database is down answers 200 and then fails
		// every confirmed call, which is the opposite of what a readiness probe is
		// for. A nil store means the deployment is in-memory and ready by definition.
		ReadyCheck: readyCheckFor(store),
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

// readyCheckFor returns the readiness probe for the configured store.
func readyCheckFor(store *storage.Store) func(ctx context.Context) error {
	if store == nil {
		return nil
	}
	return store.Ping
}

func buildAuth(cfg config.Config, logger *slog.Logger, devInsecure bool) (func(http.Handler) http.Handler, error) {
	if !cfg.Auth.Enabled {
		if !isLoopbackListen(cfg.Server.Listen) && !devInsecure {
			return nil, fmt.Errorf("auth is disabled but server.listen %q is not loopback; set -dev-insecure or MAGE2MCP_DEV_INSECURE=true for local containers only", cfg.Server.Listen)
		}
		logger.Warn("authentication disabled: development only, never expose this listener")
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
	if cfg.Modules.Magento.Enabled {
		add("magento", cfg.Modules.Magento.Instances)
	}
	if cfg.Modules.Cloudflare.Enabled {
		add("cloudflare", cfg.Modules.Cloudflare.Instances)
	}
	if cfg.Modules.GA4.Enabled {
		add("ga4", cfg.Modules.GA4.Instances)
	}
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
// development), so the dev quickstart can call tools without an identity. It
// logs a prominent warning in that case: a silent fail-open is not acceptable.
//
// The required scope is supplied by the MCP server from the tool's declared
// registry permission and is passed straight through — it is never defaulted
// here, so a viewer grant cannot authorize a write.
func authorizeFunc(cfg config.Config, policy *rbac.Policy, logger *slog.Logger) func(context.Context, string, string, string) error {
	if !cfg.Auth.Enabled {
		if logger != nil {
			logger.Warn("AUTHORIZATION DISABLED: authentication is off, so every tool call is allowed without a role check; never expose this listener",
				"auth_enabled", false, "authorization_enabled", false)
		}
		return nil
	}
	return func(ctx context.Context, tool, instance, requiredScope string) error {
		id, ok := auth.IdentityFromContext(ctx)
		if !ok {
			return errs.New(errs.CodeUnauthenticated, "no verified identity")
		}
		return policy.Authorize(id, tool, instance, requiredScope)
	}
}

// registerModules builds one service per enabled module and registers its tools.
// Multi-instance routing is not wired yet: a module with more than one instance
// is a startup error rather than a silently wrong credential choice.
// importWiring bundles the optional durable import dependencies.
type importWiring struct {
	Queue   magento.ImportQueue
	Batches magento.ImportBatchReader
}

// magentoLimits maps a configured per-instance budget onto the Magento client.
//
// A nil configuration keeps the module default: an operator who never mentions
// limits still gets protection from a retry storm. An explicit `limits:` block
// is honoured verbatim, including an all-zero block, which means unlimited —
// that is how a deployment opts out.
func magentoLimits(l *config.InstanceLimits) magento.Limits {
	if l == nil {
		return magento.DefaultLimits()
	}
	return magento.Limits{
		RequestsPerSecond: l.RequestsPerSecond,
		Burst:             l.Burst,
		MaxConcurrent:     l.MaxConcurrent,
	}
}

func registerModules(cfg config.Config, reg *registry.Registry, wirings ...importWiring) error {
	// The registry is the single write gate: with tools.writes false, Register
	// rejects every non-read tool, so a module that forgets its own check cannot
	// leak a write. The import tools are split because their read-only status
	// tools are useful without writes.
	reg.SetWritesEnabled(cfg.Tools.Writes)
	var importQueue magento.ImportQueue
	var importBatches magento.ImportBatchReader
	if len(wirings) > 0 {
		importQueue = wirings[0].Queue
		importBatches = wirings[0].Batches
	}
	var cloudflareSvc *cloudflare.Service
	var ga4Svc *ga4.Service
	if cfg.Modules.Magento.Enabled {
		settings := make([]magento.InstanceSettings, 0, len(cfg.Modules.Magento.Instances))
		for _, inst := range cfg.Modules.Magento.Instances {
			settings = append(settings, magento.InstanceSettings{
				ID: inst.ID, Endpoint: inst.Endpoint, StoreCode: inst.StoreCode, Credential: inst.Credential,
				Limits: magentoLimits(inst.Limits),
			})
		}
		resolver, err := magento.NewResolverFromSettings(settings)
		if err != nil {
			return err
		}
		if err := magento.RegisterWithResolver(reg, resolver); err != nil {
			return err
		}
		if cfg.Tools.Writes {
			if err := magento.RegisterWritesWithResolver(reg, resolver); err != nil {
				return err
			}
		}
		if importQueue != nil {
			if cfg.Tools.Writes {
				if err := magento.RegisterImportJobs(reg, resolver, importQueue, importBatches); err != nil {
					return err
				}
			} else if err := magento.RegisterImportReadTools(reg, resolver, importQueue, importBatches); err != nil {
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
// runHealthcheck probes the local /healthz endpoint. Distroless images have no
// shell or curl, so the binary is the container health probe.
func runHealthcheck() error {
	listen := os.Getenv("MAGE2MCP_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("healthcheck: invalid MAGE2MCP_LISTEN %q: %w", listen, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("healthcheck: invalid port %q", port)
	}
	// Always probe the loopback interface: the healthcheck never follows a
	// caller-supplied host, so it cannot be turned into an SSRF primitive.
	url := "http://127.0.0.1:" + port + "/healthz"                                         // #nosec G704 -- fixed loopback host, validated numeric port
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil) // #nosec G704 -- fixed loopback host
	if err != nil {
		return fmt.Errorf("healthcheck: build request: %w", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req) // #nosec G704 -- fixed loopback host and validated numeric port
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /healthz returned %d", resp.StatusCode)
	}
	return nil
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

// auditDecision maps a middleware phase/state onto the audit_events decision
// vocabulary. Before this, every row was written as "allow", so a denied call
// read as an authorized one.
func auditDecision(phase, state string) string {
	if phase != "outcome" {
		// An intent row records that a call was attempted, not that it was
		// allowed: the decision has not been made yet.
		return storage.DecisionPending
	}
	switch state {
	case "denied":
		return "deny"
	case "succeeded":
		return "allow"
	case "failed", "outcome_unknown":
		return "error"
	}
	return "error"
}

// selfLedgeredTools lists the write tools that already drive their own operation
// through internal/magento/operations.go. They carry richer entity and money
// detail than the middleware can derive, so the middleware must not open a
// second operation for them. Every other write tool is gated at the middleware.
// This list should shrink to nothing as those tools move onto the gate.
func selfLedgeredTools() map[string]bool {
	return map[string]bool{
		"magento.invoice.create":    true,
		"magento.invoice.capture":   true,
		"magento.creditmemo.create": true,
		"magento.order.create":      true,
		"magento.order.cancel":      true,
	}
}

// operationBegin adapts the storage ledger to the middleware's gate. A nil
// ledger disables the gate (development without a database).
func operationBegin(ledger *storage.OperationLedger) func(context.Context, server.OperationRequest) (server.OperationTicket, error) {
	if ledger == nil {
		return nil
	}
	return func(ctx context.Context, req server.OperationRequest) (server.OperationTicket, error) {
		ticket, err := ledger.BeginCall(ctx, storage.GateRequest{
			Operation:      req.Tool,
			InstanceID:     req.Instance,
			Subject:        req.Subject,
			ArgsHash:       req.ArgsHash,
			IdempotencyKey: req.IdempotencyKey,
			ConfirmationID: req.ConfirmationID,
			EntityType:     req.EntityType,
			EntityID:       req.EntityID,
			AmountMinor:    req.AmountMinor,
			Currency:       req.Currency,
		})
		return server.OperationTicket{
			OperationID:  ticket.OperationID,
			Fence:        ticket.Fence,
			Decision:     string(ticket.Decision),
			Attempt:      ticket.Attempt,
			Replayed:     ticket.Replayed,
			ReplayResult: ticket.ReplayResult,
			UpstreamRef:  ticket.UpstreamRef,
		}, err
	}
}

// operationFinish adapts the middleware's outcome onto the ledger transition.
func operationFinish(ledger *storage.OperationLedger) func(context.Context, server.OperationTicket, server.OperationOutcome) error {
	if ledger == nil {
		return nil
	}
	return func(ctx context.Context, ticket server.OperationTicket, out server.OperationOutcome) error {
		return ledger.FinishCall(ctx,
			storage.GateTicket{
				OperationID: ticket.OperationID,
				Fence:       ticket.Fence,
				Attempt:     ticket.Attempt,
			},
			storage.GateOutcome{
				State:       out.State,
				ErrorCode:   out.ErrorCode,
				UpstreamRef: out.UpstreamRef,
				Result:      out.Result,
				RetryAfter:  out.RetryAfter,
				Audit:       out.Audit,
			})
	}
}
