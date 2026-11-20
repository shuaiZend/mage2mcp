// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

// SEC-15: the SDK resolves a session from the header alone. Without a binding, a
// caller holding any valid token could present another caller's session id and
// receive that session's notifications and in-flight state.
func TestSessionIsBoundToItsCreator(t *testing.T) {
	owners := newSessionOwners()
	handler := sessionBinding(owners)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(mcpSessionHeader) == "" {
			// initialize: the transport names the new session here.
			w.Header().Set(mcpSessionHeader, "session-a")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Alice initializes, which binds the session to her subject.
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "alice"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", rec.Code)
	}
	if owners.allows("session-a", "alice") != true {
		t.Fatal("session was not bound to its creator")
	}

	// Bob presents Alice's session id with his own valid identity.
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(mcpSessionHeader, "session-a")
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "bob"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not 403: confirming the session exists is itself a probe signal)", rec.Code)
	}

	// Alice keeps working.
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(mcpSessionHeader, "session-a")
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "alice"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", rec.Code)
	}
}

// Without authentication there is no subject to bind; the deployment is already
// documented as local-only, so requests pass through unchanged.
func TestSessionBindingIsInertWithoutIdentity(t *testing.T) {
	handler := sessionBinding(newSessionOwners())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(mcpSessionHeader, "session-a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when authentication is off", rec.Code)
	}
}

// An untracked session must fall through: the SDK rejects ids it does not know,
// and eviction from the binding map must never break a live session.
func TestSessionBindingAllowsUntrackedSession(t *testing.T) {
	owners := newSessionOwners()
	handler := sessionBinding(owners)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(mcpSessionHeader, "never-seen")
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "alice"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an untracked session", rec.Code)
	}
}

// The binding map is bounded so a long-running gateway cannot grow without limit.
func TestSessionOwnersAreBounded(t *testing.T) {
	owners := newSessionOwners()
	for i := 0; i < maxTrackedSessions+10; i++ {
		owners.bind(string(rune('a'+i%26))+string(rune('0'+i%10))+"-"+string(rune(i)), "alice")
	}
	owners.mu.Lock()
	size := len(owners.owners)
	owners.mu.Unlock()
	if size > maxTrackedSessions {
		t.Fatalf("tracked sessions = %d, want at most %d", size, maxTrackedSessions)
	}
}
