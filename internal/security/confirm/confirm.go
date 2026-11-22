// SPDX-License-Identifier: Apache-2.0

// Package confirm implements the two-step confirmation protocol from ADR-0014.
//
// A high-risk tool call first creates a pending request bound to a canonical
// hash of its arguments. A different human identity approves it through the
// admin surface; only then may the requester re-submit the same arguments with
// the confirmation id. The requester never receives an approval secret.
package confirm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// State is the lifecycle of a confirmation.
type State string

// States of a confirmation request.
const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateConsumed State = "consumed"
	StateExpired  State = "expired"
	StateRevoked  State = "revoked"
)

// DefaultTTL and MaxTTL bound how long an approval stays usable.
const (
	DefaultTTL = 5 * time.Minute
	MaxTTL     = 15 * time.Minute
)

// Request is one confirmation record.
type Request struct {
	ID         string
	Tool       string
	Instance   string
	Scope      string
	ParamsHash string
	Requester  string
	Approver   string
	Summary    string
	State      State
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ApprovedAt time.Time
	ConsumedAt time.Time
}

// Store persists confirmation requests. Implementations must enforce single use
// atomically, reject self-approval, and bind consumption to the tool, instance,
// requester, and argument hash the approval was issued for.
type Store interface {
	Create(ctx context.Context, r *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	Approve(ctx context.Context, id, approver string, now time.Time) error
	Consume(ctx context.Context, id, requester, tool, instance, paramsHash string, now time.Time) error
	ListPending(ctx context.Context) ([]*Request, error)
}

// Service applies the protocol rules on top of a store.
type Service struct {
	store Store
	ttl   time.Duration
	now   func() time.Time
}

// NewService validates the store and TTL and returns a service.
func NewService(store Store, ttl time.Duration) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("confirm: store is required")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		return nil, fmt.Errorf("confirm: ttl must not exceed %s", MaxTTL)
	}
	return &Service{store: store, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Require creates a pending request bound to the tool, the instance, the
// requester, and the canonical argument hash. summary is the human-facing
// rendering of the arguments shown on the approval surface; it is bounded and
// stripped of control characters before it is stored.
func (s *Service) Require(ctx context.Context, tool, instance, scope, summary, requester string, args json.RawMessage) (*Request, error) {
	hash, err := CanonicalHash(args)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	req := &Request{
		ID: id, Tool: tool, Instance: instance, Scope: scope, ParamsHash: hash,
		Requester: requester, Summary: boundText(summary, maxSummaryLen), State: StatePending,
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	if err := s.store.Create(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

// IdentityClassHuman is the only identity class ADR-0014 allows to approve: "an
// agent cannot be an approver of record".
const IdentityClassHuman = "human"

// Approve records a human approval. Self-approval is rejected by the store.
func (s *Service) Approve(ctx context.Context, id, approver string) error {
	return s.ApproveAs(ctx, id, approver, "")
}

// ApproveAs records an approval by an identity of the given class. A verified
// non-human identity (a service/agent token) is refused here, before the store
// records anything: the class used to be collected only for audit attribution,
// so a `type=service` token could approve an operator's destructive write
// (U-76). An empty class means the caller resolved no verified identity, which
// is the unauthenticated CLI path; that path already warns that the approver is
// self-asserted.
func (s *Service) ApproveAs(ctx context.Context, id, approver, class string) error {
	if strings.TrimSpace(approver) == "" {
		return errs.New(errs.CodeInvalidArgument, "approver identity is required")
	}
	if class != "" && !strings.EqualFold(class, IdentityClassHuman) {
		return errs.New(errs.CodePermissionDenied,
			"only a human identity may approve an operation").
			WithDetail("identity_class", class)
	}
	if err := s.store.Approve(ctx, id, approver, s.now()); err != nil {
		return err
	}
	return nil
}

// Consume validates and burns a confirmation for the exact same operation: the
// same requester, the same tool, the same instance, and the same arguments. A
// mismatch in any of them is confirmation_param_mismatch; the error deliberately
// does not say which field differed.
func (s *Service) Consume(ctx context.Context, id, requester, tool, instance string, args json.RawMessage) error {
	hash, err := CanonicalHash(args)
	if err != nil {
		return err
	}
	return s.store.Consume(ctx, id, requester, tool, instance, hash, s.now())
}

// Validate checks that id is an approval issued for this exact operation and
// that it has not expired, without consuming it. It is the read-only guard for
// a replay: the operation already succeeded, so consuming a new approval would
// burn a token and would make concurrent losers reach a write step.
func (s *Service) Validate(ctx context.Context, id, requester, tool, instance string, args json.RawMessage) error {
	hash, err := CanonicalHash(args)
	if err != nil {
		return err
	}
	request, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	switch {
	case request.State == StateExpired || now.After(request.ExpiresAt):
		return errs.New(errs.CodeConfirmationExpired, "confirmation has expired")
	case request.State != StateApproved && request.State != StateConsumed:
		return errs.New(errs.CodeConfirmationRequired, "confirmation has not been approved")
	case request.Requester != "" && request.Requester != requester:
		// The durable store keeps only requester_hash; when Get cannot expose
		// the raw requester, the binding check falls back to tool/instance/args
		// and the single-use Consume path still enforces the requester.
		return errs.New(errs.CodePermissionDenied, "confirmation belongs to a different requester")
	case request.Tool != tool || request.Instance != instance || request.ParamsHash != hash:
		return errs.New(errs.CodeConfirmationMismatch, "confirmation does not match this operation")
	}
	return nil
}

// Pending lists requests awaiting approval.
func (s *Service) Pending(ctx context.Context) ([]*Request, error) {
	return s.store.ListPending(ctx)
}

// Summary bounds. The approval surface is read by a human, so a summary must
// stay short, printable, and free of credential-shaped values even when the
// arguments are large (a base64 import payload, for example).
const (
	maxSummaryLen      = 1024
	maxSummaryValueLen = 120
	maxSummaryItems    = 20
	// longValueFactor turns a value into a length marker once it is this many
	// times longer than a normal value, so a payload is never copied into the
	// approval record.
	longValueFactor = 4
)

// secretKeyFragments mirrors the logger's redaction list (internal/observability
// redactAttr) and must stay identical to it. A value whose key contains one of
// these fragments is never written to a confirmation record.
var secretKeyFragments = []string{
	"token", "secret", "password", "authorization", "cookie", "api_key", "apikey", "credential",
	"private", "pwd", "session", "jwt", "bearer",
}

// prioritySummaryKeys are always rendered first, in this order, when present.
// They are the fields a human needs to judge a financial or destructive
// operation, so padding the argument object with many keys cannot push them past
// the item cap. The remaining keys fill the rest of the budget alphabetically.
var prioritySummaryKeys = []string{"amount", "currency", "order_id", "entity_id", "sku", "instance"}

// Summarize renders a bounded, sanitised, deterministic summary of tool
// arguments for the human approval surface. Keys are sorted, the
// confirmation_id echo is dropped, credential-shaped values are redacted, large
// values and argument sets are abbreviated, and control characters are removed.
// The result is safe to store in the confirmation row and print in the admin CLI.
func Summarize(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(args, &decoded); err != nil {
		return "<arguments not readable>"
	}
	delete(decoded, "confirmation_id")
	if len(decoded) == 0 {
		return ""
	}
	keys := make([]string, 0, len(decoded))
	seen := make(map[string]bool, len(decoded))
	for _, key := range prioritySummaryKeys {
		if _, ok := decoded[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0, len(decoded)-len(keys))
	for key := range decoded {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	parts := make([]string, 0, len(keys))
	for i, key := range keys {
		if i == maxSummaryItems {
			parts = append(parts, fmt.Sprintf("...(%d more)", len(keys)-i))
			break
		}
		parts = append(parts, key+"="+summaryValue(key, decoded[key]))
	}
	return boundText(strings.Join(parts, " "), maxSummaryLen)
}

// summaryValue renders one argument value for Summarize.
func summaryValue(key string, value any) string {
	if isSecretKey(key) {
		return "[REDACTED]"
	}
	var text string
	switch v := value.(type) {
	case string:
		if looksLikeCredential(v) {
			return "[REDACTED]"
		}
		if len(v) > longValueFactor*maxSummaryValueLen {
			return fmt.Sprintf("<%d chars>", len(v))
		}
		text = v
	case nil:
		text = "null"
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return "<unprintable>"
		}
		text = string(raw)
	}
	return boundText(text, maxSummaryValueLen)
}

// isSecretKey reports whether a key looks like it carries a credential.
func isSecretKey(key string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, fragment := range secretKeyFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// looksLikeCredential catches a bearer token or a JWT carried under an innocent
// key, so Summarize cannot be used to copy a token into an approval record.
func looksLikeCredential(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > len("bearer ") && strings.EqualFold(trimmed[:len("bearer ")], "bearer ") {
		return true
	}
	return strings.Count(trimmed, ".") == 2 && len(trimmed) > 64 &&
		!strings.ContainsAny(trimmed, " \t\r\n")
}

// boundText makes a value safe to store and print: ASCII control characters
// become spaces, Unicode format characters (category Cf: zero-width spaces, bidi
// overrides, BOM, and friends) are escaped as \uXXXX, the result is trimmed, and
// it is truncated to max runes with a visible cut marker.
//
// Format characters are escaped rather than deleted on purpose: deleting a bidi
// override can silently join two values into one that reads differently from
// either input, which is exactly the confusion an approval surface must not
// introduce.
func boundText(value string, limit int) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case unicode.Is(unicode.Cf, r):
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.TrimSpace(b.String())
	runes := []rune(clean)
	if len(runes) <= limit {
		return clean
	}
	return string(runes[:limit]) + "\u2026"
}

// WritePending writes the approval table: what is being approved, where, and
// until when. The instance and the argument summary are shown so the approver
// never has to guess the tenant or the payload.
func WritePending(w io.Writer, requests []*Request) error {
	if w == nil {
		return fmt.Errorf("confirm: writer is required")
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tTOOL\tINSTANCE\tSUMMARY\tEXPIRES"); err != nil {
		return err
	}
	for _, r := range requests {
		if r == nil {
			continue
		}
		instance := r.Instance
		if instance == "" {
			instance = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Tool, instance, r.Summary, r.ExpiresAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// CanonicalHash returns the hex SHA-256 of the canonical JSON arguments with the
// confirmation_id field removed. Sorted keys and compact encoding make the hash
// independent of key order and whitespace.
func CanonicalHash(args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var decoded any
	if err := json.Unmarshal(args, &decoded); err != nil {
		return "", errs.New(errs.CodeInvalidArgument, "arguments are not valid JSON").WithCause(err)
	}
	if obj, ok := decoded.(map[string]any); ok {
		delete(obj, "confirmation_id")
	}
	canonical, err := canonicalJSON(decoded)
	if err != nil {
		return "", errs.New(errs.CodeInvalidArgument, "arguments cannot be canonicalized").WithCause(err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(v any) ([]byte, error) {
	switch value := v.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if value {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case float64:
		return json.Marshal(value)
	case string:
		return json.Marshal(value)
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteByte(',')
			}
			raw, err := canonicalJSON(item)
			if err != nil {
				return nil, err
			}
			b.Write(raw)
		}
		b.WriteByte(']')
		return []byte(b.String()), nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			keyJSON, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			b.Write(keyJSON)
			b.WriteByte(':')
			raw, err := canonicalJSON(value[k])
			if err != nil {
				return nil, err
			}
			b.Write(raw)
		}
		b.WriteByte('}')
		return []byte(b.String()), nil
	default:
		return json.Marshal(value)
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errs.New(errs.CodeInternal, "cannot generate confirmation id").WithCause(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// MemoryStore is an in-memory Store for tests and single-process development.
type MemoryStore struct {
	mu       sync.Mutex
	requests map[string]*Request
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{requests: make(map[string]*Request)}
}

// Create stores a new request.
func (m *MemoryStore) Create(_ context.Context, r *Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.requests[r.ID]; exists {
		return errs.New(errs.CodeConflict, "confirmation already exists")
	}
	copied := *r
	m.requests[r.ID] = &copied
	return nil
}

// Get returns a request by id.
func (m *MemoryStore) Get(_ context.Context, id string) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "confirmation not found")
	}
	copied := *r
	return &copied, nil
}

// Approve records a human approval and rejects self-approval.
func (m *MemoryStore) Approve(_ context.Context, id, approver string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return errs.New(errs.CodeNotFound, "confirmation not found")
	}
	if r.State != StatePending {
		return errs.New(errs.CodeConflict, "confirmation is not pending")
	}
	if now.After(r.ExpiresAt) {
		r.State = StateExpired
		return errs.New(errs.CodeConfirmationExpired, "confirmation has expired")
	}
	if r.Requester == approver {
		return errs.New(errs.CodePermissionDenied, "self-approval is not allowed")
	}
	r.State = StateApproved
	r.Approver = approver
	r.ApprovedAt = now
	return nil
}

// Consume validates and burns an approved request.
func (m *MemoryStore) Consume(_ context.Context, id, requester, tool, instance, paramsHash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return errs.New(errs.CodeNotFound, "confirmation not found")
	}
	switch {
	case r.State == StateConsumed:
		return errs.New(errs.CodeConfirmationUsed, "confirmation has already been used")
	case r.State == StateExpired || now.After(r.ExpiresAt):
		r.State = StateExpired
		return errs.New(errs.CodeConfirmationExpired, "confirmation has expired")
	case r.State != StateApproved:
		return errs.New(errs.CodeConfirmationRequired, "confirmation has not been approved")
	case r.Requester != requester:
		return errs.New(errs.CodePermissionDenied, "confirmation belongs to a different requester")
	case r.Tool != tool || r.Instance != instance || r.ParamsHash != paramsHash:
		// One generic message: the caller must not learn which binding failed.
		return errs.New(errs.CodeConfirmationMismatch, "confirmation does not match this operation")
	}
	r.State = StateConsumed
	r.ConsumedAt = now
	return nil
}

// ListPending returns pending requests, oldest first.
func (m *MemoryStore) ListPending(_ context.Context) ([]*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Request, 0, len(m.requests))
	for _, r := range m.requests {
		if r.State == StatePending {
			copied := *r
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
