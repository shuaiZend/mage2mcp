// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// fakeClock lets the limiter tests drive token refill without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// sleep advances the fake clock instead of blocking, and records the wait.
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.advance(d)
	return nil
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestLimitsValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Limits
		ok   bool
	}{
		{"zero is unlimited", Limits{}, true},
		{"typical budget", Limits{RequestsPerSecond: 10, Burst: 20, MaxConcurrent: 4}, true},
		{"concurrency only", Limits{MaxConcurrent: 4}, true},
		{"rate only", Limits{RequestsPerSecond: 5}, true},
		{"negative rate", Limits{RequestsPerSecond: -1}, false},
		{"negative burst", Limits{Burst: -1}, false},
		{"negative concurrency", Limits{MaxConcurrent: -1}, false},
		{"burst without rate", Limits{Burst: 10}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate(%+v) = %v, want nil", tc.in, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate(%+v) = nil, want an error", tc.in)
			}
		})
	}
}

func TestLimiterUnlimitedIsNil(t *testing.T) {
	if lim := newLimiter(Limits{}); lim != nil {
		t.Fatalf("newLimiter(zero) = %+v, want nil so the request path takes no lock", lim)
	}
	var nilLim *limiter
	release, err := nilLim.acquire(context.Background())
	if err != nil {
		t.Fatalf("nil limiter acquire: %v", err)
	}
	release() // must not panic
}

func TestLimiterAllowsBurstThenPaces(t *testing.T) {
	clock := newFakeClock()
	lim := newLimiterWithClock(Limits{RequestsPerSecond: 10, Burst: 3}, clock.now, clock.sleep)

	ctx := context.Background()
	start := clock.now()
	for i := 0; i < 3; i++ {
		release, err := lim.acquire(ctx)
		if err != nil {
			t.Fatalf("burst acquire %d: %v", i, err)
		}
		release()
	}
	if waited := clock.now().Sub(start); waited != 0 {
		t.Fatalf("the burst allowance was paced: waited %s, want 0", waited)
	}

	// The fourth request must wait for a refill at 10 rps: 100ms.
	release, err := lim.acquire(ctx)
	if err != nil {
		t.Fatalf("paced acquire: %v", err)
	}
	release()
	if waited := clock.now().Sub(start); waited != 100*time.Millisecond {
		t.Fatalf("paced wait = %s, want 100ms", waited)
	}
}

func TestLimiterBurstDefaultsToOne(t *testing.T) {
	clock := newFakeClock()
	lim := newLimiterWithClock(Limits{RequestsPerSecond: 1}, clock.now, clock.sleep)
	if lim.burst != 1 {
		t.Fatalf("burst = %v, want 1 when only a rate is configured", lim.burst)
	}
	ctx := context.Background()
	start := clock.now()
	for i := 0; i < 2; i++ {
		release, err := lim.acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		release()
	}
	if waited := clock.now().Sub(start); waited != time.Second {
		t.Fatalf("second request waited %s, want 1s at 1 rps", waited)
	}
}

func TestLimiterCapsConcurrency(t *testing.T) {
	lim := newLimiter(Limits{MaxConcurrent: 2})
	ctx := context.Background()

	first, err := lim.acquire(ctx)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := lim.acquire(ctx)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	// The third must block until one of the first two is released.
	shortCtx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if _, err := lim.acquire(shortCtx); err == nil {
		t.Fatal("third acquire succeeded beyond the concurrency cap")
	} else if errs.CodeOf(err) != errs.CodeRateLimited {
		t.Fatalf("third acquire code = %s, want rate_limited", errs.CodeOf(err))
	}

	first()
	third, err := lim.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second()
	third()
}

func TestLimiterWaitsAreBoundedByContext(t *testing.T) {
	// One token per 1000s with a 20ms deadline: the wait cannot be met.
	lim := newLimiter(Limits{RequestsPerSecond: 0.001, Burst: 1})
	ctx := context.Background()
	release, err := lim.acquire(ctx)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release()

	shortCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = lim.acquire(shortCtx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("acquire succeeded despite an unreachable budget")
	}
	if code := errs.CodeOf(err); code != errs.CodeRateLimited {
		t.Fatalf("code = %s, want rate_limited", code)
	}
	if !errs.RetryableError(err) {
		t.Fatal("an exhausted budget is transient; the error must be retryable")
	}
	if elapsed > time.Second {
		t.Fatalf("acquire took %s; it must return when the context expires", elapsed)
	}
}

// TestClientBudgetSurfacesRateLimited proves the budget is enforced on the real
// request path, not only in the limiter unit tests.
func TestClientBudgetSurfacesRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:   srv.URL,
		StoreCode: "default",
		Signer:    testSigner(t),
		Timeout:   60 * time.Millisecond,
		// One token, then a rate so slow the second call cannot wait it out.
		Limits: Limits{RequestsPerSecond: 0.001, Burst: 1},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil); err != nil {
		t.Fatalf("first request: %v", err)
	}
	_, err = client.do(context.Background(), http.MethodGet, "/products", nil, nil)
	if err == nil {
		t.Fatal("second request succeeded despite an exhausted budget")
	}
	if code := errs.CodeOf(err); code != errs.CodeRateLimited {
		t.Fatalf("second request code = %s, want rate_limited", code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1: a rate-limited request must not reach Magento", got)
	}
}

// TestClientZeroLimitsIsUnlimited keeps the zero value a true no-op.
func TestClientZeroLimitsIsUnlimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:   srv.URL,
		StoreCode: "default",
		Signer:    testSigner(t),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("upstream calls = %d, want 5", got)
	}
}
