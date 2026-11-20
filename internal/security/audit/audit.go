// SPDX-License-Identifier: Apache-2.0

// Package audit defines the append-only audit event and an in-memory recorder.
//
// An event records who called which tool, on which instance, at which scope,
// and what was decided. Payload contents are never recorded: only a canonical
// parameter hash. The in-memory recorder exists for tests and local runs; the
// durable writer lives in the storage layer.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one audit record. EventID and OccurredAt are assigned by the
// recorder when empty.
type Event struct {
	EventID       string
	OccurredAt    time.Time
	Subject       string
	IdentityClass string
	Tool          string
	Instance      string
	Scope         string
	Decision      string
	Phase         string
	State         string
	ParamHash     string
	ErrorCode     string
	RequestID     string
}

// Recorder persists audit events. A write error must be handled by the caller
// according to the fail-closed policy for the operation's risk class.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// MemoryRecorder is an in-memory Recorder safe for concurrent use.
type MemoryRecorder struct {
	mu     sync.Mutex
	events []Event
}

// NewMemory returns an empty in-memory recorder.
func NewMemory() *MemoryRecorder { return &MemoryRecorder{} }

// Record validates the required fields, fills EventID and OccurredAt when they
// are empty, and appends the event. Required fields are Subject, Tool,
// Decision, and Phase. A cancelled context is never written.
func (m *MemoryRecorder) Record(ctx context.Context, e Event) error {
	if m == nil {
		return errors.New("audit: nil recorder")
	}
	if ctx == nil {
		return errors.New("audit: nil context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("audit: record: %w", err)
	}
	switch {
	case e.Subject == "":
		return errors.New("audit: subject is required")
	case e.Tool == "":
		return errors.New("audit: tool is required")
	case e.Decision == "":
		return errors.New("audit: decision is required")
	case e.Phase == "":
		return errors.New("audit: phase is required")
	}
	if e.EventID == "" {
		id, err := newEventID()
		if err != nil {
			return fmt.Errorf("audit: generate event id: %w", err)
		}
		e.EventID = id
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

// Events returns a copy of the recorded events in insertion order.
func (m *MemoryRecorder) Events() []Event {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// newEventID returns a UUIDv7 so identifiers sort by creation time.
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(time.Now().UnixMilli()))
	copy(b[0:6], ts[2:8])       // 48-bit millisecond timestamp
	b[6] = 0x70 | (b[6] & 0x0f) // version 7
	b[8] = 0x80 | (b[8] & 0x3f) // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// CallState is the per-call marker the middleware installs and the tool updates.
//
// A context value cannot carry information upward, so the marker has to be a pointer
// the middleware owns and the tool mutates.
type CallState struct {
	operationOwned atomic.Bool
}

// MarkOperationOwned records that the tool opened its own ledger operation and will
// write its own outcome audit row.
func (c *CallState) MarkOperationOwned() {
	if c != nil {
		c.operationOwned.Store(true)
	}
}

// OperationOwned reports whether the tool claimed ownership of the audit outcome.
func (c *CallState) OperationOwned() bool {
	return c != nil && c.operationOwned.Load()
}

// callStateKey is the context key for the per-call marker.
type callStateKey struct{}

// WithCallState installs a fresh marker for one call and returns the context and the
// state the caller reads afterwards.
func WithCallState(ctx context.Context) (context.Context, *CallState) {
	state := &CallState{}
	if ctx == nil {
		return ctx, state
	}
	return context.WithValue(ctx, callStateKey{}, state), state
}

// CallStateFrom returns the marker installed for this call, or nil.
func CallStateFrom(ctx context.Context) *CallState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(callStateKey{}).(*CallState)
	return state
}

// MarkOperationOwned records, for the current call, that the tool opened its own
// operation and therefore owns its outcome audit row.
//
// The middleware writes the outcome row for every call it gates. For a self-ledgered
// tool it cannot know whether the tool got as far as opening an operation — a tool that
// fails validation returns before `Begin` — so without this marker it would either
// duplicate the row or leave a call unaudited. The tool is the only party that knows.
func MarkOperationOwned(ctx context.Context) {
	CallStateFrom(ctx).MarkOperationOwned()
}
