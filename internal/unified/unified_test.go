// SPDX-License-Identifier: Apache-2.0

package unified

import (
	"context"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

type fakeTraffic struct {
	value Traffic
	err   error
	calls int
}

func (f *fakeTraffic) Traffic(_ context.Context, _ string, _, _ time.Time) (Traffic, error) {
	f.calls++
	return f.value, f.err
}

type fakeSessions struct {
	value Sessions
	err   error
	calls int
}

func (f *fakeSessions) Sessions(_ context.Context, _ string, _, _ time.Time) (Sessions, error) {
	f.calls++
	return f.value, f.err
}

func testBinding() Binding {
	return Binding{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}
}

func testService(t *testing.T, traffic *fakeTraffic, sessions *fakeSessions) *Service {
	t.Helper()
	svc, err := NewService([]Binding{testBinding()}, traffic, sessions)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func window() (time.Time, time.Time) {
	to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	return to.Add(-24 * time.Hour), to
}

func TestCompareHappyPath(t *testing.T) {
	traffic := &fakeTraffic{value: Traffic{Requests: 1000, CachedRequests: 400, Rows: 3}}
	sessions := &fakeSessions{value: Sessions{Sessions: 250, Users: 200, TimeZone: "America/Los_Angeles"}}
	svc := testService(t, traffic, sessions)
	from, to := window()
	res, err := svc.Compare(context.Background(), "b1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequestsPerSession == nil || *res.RequestsPerSession != 4 {
		t.Fatalf("ratio = %v, want 4", res.RequestsPerSession)
	}
	if !res.Quality.Aligned || res.Cloudflare.Requests != 1000 || res.GA4.Sessions != 250 {
		t.Fatalf("unexpected comparison: %+v", res)
	}
	if len(res.Quality.Notes) == 0 {
		t.Fatal("expected semantic notes")
	}
}

func TestCompareZeroSessionsHasNoRatio(t *testing.T) {
	svc := testService(t,
		&fakeTraffic{value: Traffic{Requests: 500, Rows: 1}},
		&fakeSessions{value: Sessions{Sessions: 0}})
	from, to := window()
	res, err := svc.Compare(context.Background(), "b1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequestsPerSession != nil {
		t.Fatal("ratio must be nil when sessions are zero")
	}
	if !containsNote(res.Quality.Notes, "ga4_no_sessions_in_window") {
		t.Fatalf("missing note: %v", res.Quality.Notes)
	}
}

func TestCompareZeroRequests(t *testing.T) {
	svc := testService(t,
		&fakeTraffic{value: Traffic{Requests: 0}},
		&fakeSessions{value: Sessions{Sessions: 10}})
	from, to := window()
	res, err := svc.Compare(context.Background(), "b1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !containsNote(res.Quality.Notes, "cloudflare_no_requests_in_window") {
		t.Fatalf("missing note: %v", res.Quality.Notes)
	}
}

func TestCompareRejectsUnknownBinding(t *testing.T) {
	svc := testService(t, &fakeTraffic{}, &fakeSessions{})
	from, to := window()
	if _, err := svc.Compare(context.Background(), "nope", from, to); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestCompareWindowValidation(t *testing.T) {
	svc := testService(t, &fakeTraffic{}, &fakeSessions{})
	now := time.Now().UTC()
	if _, err := svc.Compare(context.Background(), "b1", now, now); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatal("zero-length window accepted")
	}
	if _, err := svc.Compare(context.Background(), "b1", now.Add(-40*24*time.Hour), now); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatal("oversized window accepted")
	}
}

func TestComparePropagatesProviderErrors(t *testing.T) {
	svc := testService(t,
		&fakeTraffic{err: errs.New(errs.CodeUpstreamUnavailable, "cf down")},
		&fakeSessions{value: Sessions{Sessions: 1}})
	from, to := window()
	if _, err := svc.Compare(context.Background(), "b1", from, to); errs.CodeOf(err) != errs.CodeUpstreamUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(nil, &fakeTraffic{}, nil); err == nil {
		t.Fatal("nil sessions provider accepted")
	}
	bad := Binding{ID: "b", Magento: "a", Cloudflare: "a", GA4: "c"}
	if _, err := NewService([]Binding{bad}, &fakeTraffic{}, &fakeSessions{}); err == nil {
		t.Fatal("duplicate instances accepted")
	}
	dup := []Binding{testBinding(), testBinding()}
	if _, err := NewService(dup, &fakeTraffic{}, &fakeSessions{}); err == nil {
		t.Fatal("duplicate binding accepted")
	}
}

func TestCompareFetchesConcurrently(t *testing.T) {
	slow := &fakeTraffic{value: Traffic{Requests: 10}}
	svc := testService(t, slow, &fakeSessions{value: Sessions{Sessions: 5}})
	from, to := window()
	if _, err := svc.Compare(context.Background(), "b1", from, to); err != nil {
		t.Fatal(err)
	}
	if slow.calls != 1 {
		t.Fatalf("traffic calls = %d", slow.calls)
	}
}

func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if note == want {
			return true
		}
	}
	return false
}
