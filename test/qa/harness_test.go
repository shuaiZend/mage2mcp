//go:build qa

// Package qa is the independent verification suite for the production-readiness
// audit. It is deliberately separate from the authors' tests: it drives the real
// MCP server over its HTTP surface, the real PostgreSQL store, the real job
// queue, the real confirmation service and the real operation ledger, against
// fake Magento upstreams that the reviewer controls.
//
// It is not part of `go test ./...`. Run it with:
//
//	MAGE2MCP_TEST_DATABASE_URL=... go test -count=1 -race -tags qa ./test/qa/...
//
// scripts/qa-independent.sh runs exactly that and fails on any skipped test.
// Every test names the audit finding it guards in its doc comment.
package qa

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/security/rbac"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// mainStore is the shared PostgreSQL store. It is opened once and migrated; each
// test uses a unique instance id so runs cannot collide.
var mainStore *storage.Store

func TestMain(m *testing.M) {
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "qa: MAGE2MCP_TEST_DATABASE_URL is required; the independent suite never skips")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 12})
	if err != nil {
		fmt.Fprintf(os.Stderr, "qa: open store: %v\n", err)
		os.Exit(1)
	}
	if err := store.Migrate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "qa: migrate: %v\n", err)
		os.Exit(1)
	}
	mainStore = store
	code := m.Run()
	store.Close()
	os.Exit(code)
}

// fixture is one independently wired gateway: registry, policy, confirmation,
// ledger and the HTTP surface the MCP client talks to.
type fixture struct {
	t        *testing.T
	store    *storage.Store
	queue    *storage.JobQueue
	ledger   *storage.OperationLedger
	confirms *confirm.Service
	registry *registry.Registry
	mcpSrv   *mcp.Server
	httpSrv  *httptest.Server
	urls     map[string]string
}

type fixtureOptions struct {
	// upstreams maps an instance id to the fake Magento base URL.
	upstreams map[string]string
	// writes registers the Magento write tools.
	writes bool
	// imports registers the asynchronous import job tools (needs the queue).
	imports bool
	// withoutLedger builds a gateway with no operation ledger (the pre-ledger
	// development path).
	withoutLedger bool
	// policy, when set, is the RBAC policy; the identity is injected from the
	// X-QA-Subject / X-QA-Roles headers.
	policy *rbac.Policy
	// clientTimeout overrides the Magento HTTP client timeout (default 2s). The
	// timeout tests use a short value so a slow fake exercises the ambiguous path.
	clientTimeout time.Duration
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	return newFixtureWithStore(t, mainStore, opts)
}

func newFixtureWithStore(t *testing.T, store *storage.Store, opts fixtureOptions) *fixture {
	t.Helper()
	if len(opts.upstreams) == 0 {
		t.Fatal("qa fixture: at least one upstream is required")
	}
	services := make(map[string]*magento.Service, len(opts.upstreams))
	timeout := opts.clientTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	for id, url := range opts.upstreams {
		services[id] = newMagentoService(t, url, timeout)
	}
	resolver, err := magento.NewResolver(services)
	if err != nil {
		t.Fatalf("qa fixture: resolver: %v", err)
	}
	reg := registry.New()
	if err := magento.RegisterWithResolver(reg, resolver); err != nil {
		t.Fatalf("qa fixture: register reads: %v", err)
	}
	if opts.writes {
		if err := magento.RegisterWritesWithResolver(reg, resolver); err != nil {
			t.Fatalf("qa fixture: register writes: %v", err)
		}
	}
	f := &fixture{t: t, store: store, registry: reg, urls: opts.upstreams}
	if opts.imports {
		f.queue = storage.NewJobQueue(store)
		if err := magento.RegisterImportJobs(reg, resolver, f.queue, store); err != nil {
			t.Fatalf("qa fixture: register import tools: %v", err)
		}
	}
	f.ledger = storage.NewOperationLedger(store)
	if !opts.withoutLedger {
		magento.ConfigureOperations(f.ledger)
		t.Cleanup(func() { magento.ConfigureOperations(nil) })
	}
	confirms, err := confirm.NewService(storage.NewConfirmStore(store), 5*time.Minute)
	if err != nil {
		t.Fatalf("qa fixture: confirm service: %v", err)
	}
	f.confirms = confirms

	serverOpts := server.Options{
		Version:  "qa",
		Registry: reg,
		Confirm:  confirms,
	}
	if opts.policy != nil {
		serverOpts.Authorize = authorizer(opts.policy)
	}
	if !opts.withoutLedger {
		serverOpts.BeginOperation = beginOperation(f.ledger)
		serverOpts.FinishOperation = finishOperation(f.ledger)
		// The money tools drive their own ledger operation (internal/magento
		// operations.go); the middleware must not open a second one for them.
		// This mirrors cmd/server's selfLedgeredTools().
		serverOpts.SelfLedgeredTools = map[string]bool{
			"magento.invoice.create":    true,
			"magento.invoice.capture":   true,
			"magento.creditmemo.create": true,
			"magento.order.create":      true,
			"magento.order.cancel":      true,
		}
	}
	f.mcpSrv, err = server.NewMCP(serverOpts)
	if err != nil {
		t.Fatalf("qa fixture: NewMCP: %v", err)
	}
	handler := server.Handler(f.mcpSrv, server.Options{Registry: reg, AuthMiddleware: identityMiddleware})
	f.httpSrv = httptest.NewServer(handler)
	t.Cleanup(f.httpSrv.Close)
	return f
}

func newMagentoService(t *testing.T, baseURL string, timeout time.Duration) *magento.Service {
	t.Helper()
	client, err := magento.NewClient(magento.ClientConfig{
		BaseURL: baseURL, StoreCode: "default",
		Signer:  &magento.BearerSigner{Token: "qa-token"},
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("qa: magento client: %v", err)
	}
	svc, err := magento.NewService(client)
	if err != nil {
		t.Fatalf("qa: magento service: %v", err)
	}
	return svc
}

// authorizer is the production wiring: the scope is resolved from the registry
// by the server and the policy decides. It is not the code under test; it is the
// composition an operator would deploy.
func authorizer(policy *rbac.Policy) func(context.Context, string, string, string) error {
	return func(ctx context.Context, tool, instance, scope string) error {
		id, ok := auth.IdentityFromContext(ctx)
		if !ok || id == nil {
			return errs.New(errs.CodeUnauthenticated, "no verified identity")
		}
		return policy.Authorize(id, tool, instance, scope)
	}
}

func identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject := r.Header.Get("X-QA-Subject")
		if subject == "" {
			next.ServeHTTP(w, r)
			return
		}
		var roles []string
		if raw := r.Header.Get("X-QA-Roles"); raw != "" {
			roles = strings.Split(raw, ",")
		}
		ctx := auth.WithIdentity(r.Context(), &auth.Identity{
			Subject: subject, Class: auth.ClassService, Roles: roles,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// beginOperation and finishOperation adapt the storage ledger onto the server's
// middleware gate. This is the same composition cmd/server uses; the suite needs
// it so the ledger is exercised at the same choke point as production.
func beginOperation(ledger *storage.OperationLedger) func(context.Context, server.OperationRequest) (server.OperationTicket, error) {
	return func(ctx context.Context, req server.OperationRequest) (server.OperationTicket, error) {
		ticket, err := ledger.BeginCall(ctx, storage.GateRequest{
			Operation: req.Tool, InstanceID: req.Instance, Subject: req.Subject,
			ArgsHash: req.ArgsHash, IdempotencyKey: req.IdempotencyKey,
			ConfirmationID: req.ConfirmationID, EntityType: req.EntityType,
			EntityID: req.EntityID, AmountMinor: req.AmountMinor, Currency: req.Currency,
		})
		return server.OperationTicket{
			OperationID: ticket.OperationID, Fence: ticket.Fence,
			Decision: string(ticket.Decision), Attempt: ticket.Attempt,
			Replayed: ticket.Replayed, ReplayResult: ticket.ReplayResult,
			UpstreamRef: ticket.UpstreamRef,
		}, err
	}
}

func finishOperation(ledger *storage.OperationLedger) func(context.Context, server.OperationTicket, server.OperationOutcome) error {
	return func(ctx context.Context, ticket server.OperationTicket, out server.OperationOutcome) error {
		return ledger.FinishCall(ctx,
			storage.GateTicket{OperationID: ticket.OperationID, Fence: ticket.Fence, Attempt: ticket.Attempt},
			storage.GateOutcome{
				State: out.State, ErrorCode: out.ErrorCode, UpstreamRef: out.UpstreamRef,
				Result: out.Result, RetryAfter: out.RetryAfter,
			})
	}
}

// session connects a fresh MCP client over the HTTP surface as subject with the
// given roles.
func (f *fixture) session(t *testing.T, subject string, roles ...string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "qa-" + subject, Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: f.httpSrv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: &identityTransport{
			subject: subject, roles: strings.Join(roles, ","),
		}},
	}, nil)
	if err != nil {
		t.Fatalf("qa: connect as %s: %v", subject, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type identityTransport struct {
	subject string
	roles   string
}

func (t *identityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-QA-Subject", t.subject)
	if t.roles != "" {
		clone.Header.Set("X-QA-Roles", t.roles)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

// toolResult is the agent-facing view of one tools/call.
type toolResult struct {
	IsError    bool
	Structured map[string]any
	Text       string
}

func (f *fixture) call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) toolResult {
	t.Helper()
	out, err := f.callSafe(session, tool, args)
	if err != nil {
		t.Fatalf("qa: call %s: %v", tool, err)
	}
	return out
}

// callSafe is call for concurrent callers: it returns transport errors instead of
// failing the test from a goroutine.
func (f *fixture) callSafe(session *mcp.ClientSession, tool string, args map[string]any) (toolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return toolResult{}, err
	}
	out := toolResult{IsError: res.IsError}
	if res.StructuredContent != nil {
		raw, marshalErr := json.Marshal(res.StructuredContent)
		if marshalErr == nil {
			_ = json.Unmarshal(raw, &out.Structured)
		}
	}
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			out.Text += text.Text
		}
	}
	return out, nil
}

func (r toolResult) code() string {
	if r.Structured == nil {
		return ""
	}
	code, _ := r.Structured["code"].(string)
	return code
}

func (r toolResult) details() map[string]any {
	if r.Structured == nil {
		return nil
	}
	details, _ := r.Structured["details"].(map[string]any)
	return details
}

func (r toolResult) detail(key string) string {
	if value, ok := r.details()[key].(string); ok {
		return value
	}
	return ""
}

func (r toolResult) mustSucceed(t *testing.T) {
	t.Helper()
	if r.IsError {
		t.Fatalf("qa: expected success, got tool error: %s", r.Text)
	}
}

func (r toolResult) mustFail(t *testing.T, wantCode string) {
	t.Helper()
	if !r.IsError {
		t.Fatalf("qa: expected a tool error with code %q, got success: %v", wantCode, r.Structured)
	}
	if wantCode != "" && r.code() != wantCode {
		t.Fatalf("qa: error code = %q, want %q (%s)", r.code(), wantCode, r.Text)
	}
}

// confirmState reads the persisted confirmation row so a test can assert the
// approval's terminal state, not just the response codes.
func (f *fixture) confirmState(t *testing.T, id string) (string, time.Time) {
	t.Helper()
	store := storage.NewConfirmStore(f.store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("qa: read confirmation %s: %v", id, err)
	}
	return string(req.State), req.ConsumedAt
}

// requireConfirmation asks for a confirmation by calling a write tool without an
// approval and returns the pending id. It fails (never skips) if the gateway does
// not demand one.
func (f *fixture) requireConfirmation(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res := f.call(t, session, tool, args)
	res.mustFail(t, string(errs.CodeConfirmationRequired))
	id := res.detail("confirmation_id")
	if id == "" {
		t.Fatalf("qa: confirmation_required carried no id: %s", res.Text)
	}
	return id
}

func (f *fixture) approve(t *testing.T, id, approver string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.confirms.Approve(ctx, id, approver); err != nil {
		t.Fatalf("qa: approve %s: %v", id, err)
	}
}

func withConfirmation(args map[string]any, id string) map[string]any {
	out := make(map[string]any, len(args)+1)
	for key, value := range args {
		out[key] = value
	}
	out["confirmation_id"] = id
	return out
}

// approvedWrite performs the full two-step protocol and returns the result.
func (f *fixture) approvedWrite(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any, approver string) toolResult {
	t.Helper()
	id := f.requireConfirmation(t, session, tool, args)
	f.approve(t, id, approver)
	return f.call(t, session, tool, withConfirmation(args, id))
}

// fakeUpstream is a fake Magento endpoint that records every request it sees.
type fakeUpstream struct {
	mu       sync.Mutex
	requests []requestRecord
	srv      *httptest.Server
}

type requestRecord struct {
	Method string
	Path   string
	Body   []byte
}

func newFakeUpstream(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, requestRecord{Method: r.Method, Path: r.URL.Path, Body: body})
		f.mu.Unlock()
		handle(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) url() string { return f.srv.URL }

func (f *fakeUpstream) count(method, pathPrefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.requests {
		if req.Method == method && strings.HasPrefix(req.Path, pathPrefix) {
			n++
		}
	}
	return n
}

func (f *fakeUpstream) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// uniqueInstance returns an instance id that is valid for the tool schema and
// unique per test run, so rows from earlier runs cannot satisfy an assertion.
func uniqueInstance(t *testing.T, prefix string) string {
	t.Helper()
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := strings.ToLower(t.Name())
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, "-")
	if len(name) > 32 {
		name = name[:32]
	}
	return fmt.Sprintf("qa-%s-%s-%x", prefix, name, b)
}

// productArgs is a valid magento.product.create request.
func productArgs(instance, sku string) map[string]any {
	return map[string]any{
		"instance": instance, "sku": sku, "name": "QA product " + sku,
		"attribute_set_id": 4, "type_id": "simple", "price": "9.99",
	}
}

// transferArgs is a valid magento.inventory.transfer request.
func transferArgs(instance, sku string, qty int) map[string]any {
	return map[string]any{
		"instance": instance, "sku": sku,
		"from_source": "default", "to_source": "warehouse", "quantity": qty,
	}
}
