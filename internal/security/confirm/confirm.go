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
	"sort"
	"strings"
	"sync"
	"time"

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

// Store persists confirmation requests. Implementations must enforce single
// use atomically and reject self-approval.
type Store interface {
	Create(ctx context.Context, r *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	Approve(ctx context.Context, id, approver string, now time.Time) error
	Consume(ctx context.Context, id, requester, paramsHash string, now time.Time) error
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

// Require creates a pending request bound to the canonical argument hash.
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
		Requester: requester, Summary: summary, State: StatePending,
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	if err := s.store.Create(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

// Approve records a human approval. Self-approval is rejected by the store.
func (s *Service) Approve(ctx context.Context, id, approver string) error {
	if strings.TrimSpace(approver) == "" {
		return errs.New(errs.CodeInvalidArgument, "approver identity is required")
	}
	if err := s.store.Approve(ctx, id, approver, s.now()); err != nil {
		return err
	}
	return nil
}

// Consume validates and burns a confirmation for the exact same arguments.
func (s *Service) Consume(ctx context.Context, id, requester string, args json.RawMessage) error {
	hash, err := CanonicalHash(args)
	if err != nil {
		return err
	}
	return s.store.Consume(ctx, id, requester, hash, s.now())
}

// Pending lists requests awaiting approval.
func (s *Service) Pending(ctx context.Context) ([]*Request, error) {
	return s.store.ListPending(ctx)
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
func (m *MemoryStore) Consume(_ context.Context, id, requester, paramsHash string, now time.Time) error {
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
	case r.ParamsHash != paramsHash:
		return errs.New(errs.CodeConfirmationMismatch, "arguments changed after approval")
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
