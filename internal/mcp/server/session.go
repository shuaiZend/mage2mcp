// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"sync"

	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

// mcpSessionHeader is the header the MCP Streamable HTTP transport uses to name a
// session.
const mcpSessionHeader = "Mcp-Session-Id"

// maxTrackedSessions bounds the binding map. A session that falls out of it is
// simply unknown, which falls through to the SDK's own lookup rather than being
// rejected, so eviction never breaks a live session.
const maxTrackedSessions = 4096

// sessionOwners binds an MCP session id to the subject that created it.
//
// The SDK resolves a session from the header alone, so without this an attacker
// holding any valid token of their own could attach to another caller's session by
// presenting its id, and would receive that session's notifications and in-flight
// state. Authorization still uses the caller's own identity, which is why this is a
// confidentiality issue rather than escalation — but it is a real one.
type sessionOwners struct {
	mu     sync.Mutex
	owners map[string]string
	order  []string
}

func newSessionOwners() *sessionOwners {
	return &sessionOwners{owners: make(map[string]string, 64)}
}

// bind records the subject that created sessionID.
func (s *sessionOwners) bind(sessionID, subject string) {
	if sessionID == "" || subject == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.owners[sessionID]; !exists {
		s.order = append(s.order, sessionID)
		if len(s.order) > maxTrackedSessions {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.owners, oldest)
		}
	}
	s.owners[sessionID] = subject
}

// allows reports whether subject may use sessionID.
//
// An untracked session is allowed through: the SDK rejects ids it does not know,
// and eviction must not break a live session. A tracked session presented by a
// different subject is refused.
func (s *sessionOwners) allows(sessionID, subject string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, tracked := s.owners[sessionID]
	if !tracked {
		return true
	}
	return owner == subject
}

// sessionBinding ties a session to its creator's verified subject.
//
// It sits inside the auth middleware so the identity is already on the context. A
// mismatch answers 404 rather than 403: confirming that a session exists but
// belongs to someone else is itself a probe signal (threat-model T-11).
func sessionBinding(owners *sessionOwners) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			subject := requestSubject(r)
			if subject == "" {
				// Authentication is disabled: there is no identity to bind, and the
				// deployment is already documented as local-only.
				next.ServeHTTP(w, r)
				return
			}
			if id := r.Header.Get(mcpSessionHeader); id != "" && !owners.allows(id, subject) {
				http.NotFound(w, r)
				return
			}
			recorder := &sessionRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			if id := recorder.Header().Get(mcpSessionHeader); id != "" {
				owners.bind(id, subject)
			}
		})
	}
}

// requestSubject returns the verified subject of the request, or "".
func requestSubject(r *http.Request) string {
	if id, ok := auth.IdentityFromContext(r.Context()); ok {
		return id.Subject
	}
	return ""
}

// sessionRecorder remembers whether the wrapped handler set the session header, so
// a newly created session can be bound to its creator.
type sessionRecorder struct {
	http.ResponseWriter
	sessionID string
}

// Header returns the underlying header set, which is how the transport emits the
// session id on initialize.
func (r *sessionRecorder) Header() http.Header { return r.ResponseWriter.Header() }

// WriteHeader forwards the status code.
func (r *sessionRecorder) WriteHeader(status int) { r.ResponseWriter.WriteHeader(status) }

// Write forwards the body and captures a session id set on the header first.
func (r *sessionRecorder) Write(b []byte) (int, error) {
	if r.sessionID == "" {
		r.sessionID = r.ResponseWriter.Header().Get(mcpSessionHeader)
	}
	return r.ResponseWriter.Write(b)
}

// Flush forwards a flush when the transport streams a response.
func (r *sessionRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
