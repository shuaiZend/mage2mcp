// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"sync"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Limits is the outbound request budget for one Magento instance.
//
// The gateway is not the only client of a Magento store, and a store's PHP
// workers, database and search index all degrade when a burst of agent calls
// arrives at once. A per-instance budget converts "as fast as the agent asks"
// into a bounded, predictable load, and it is per instance because two stores
// have independent capacity.
//
// A zero Limits disables limiting entirely. DefaultLimits is what a
// configuration that does not mention limits gets.
type Limits struct {
	// RequestsPerSecond is the sustained request rate. Zero disables the rate
	// limit (the concurrency cap still applies).
	RequestsPerSecond float64
	// Burst is the token bucket capacity: how many requests may be issued
	// back-to-back before the sustained rate applies. Zero with a positive
	// RequestsPerSecond means a burst of one.
	Burst int
	// MaxConcurrent bounds in-flight requests for the instance. Zero disables
	// the cap.
	MaxConcurrent int
}

// DefaultLimits is deliberately generous: it does not throttle ordinary use,
// but it stops a runaway agent or a retry storm from saturating a store.
const (
	DefaultRequestsPerSecond = 50
	DefaultBurst             = 100
	DefaultMaxConcurrent     = 8
)

// DefaultLimits returns the budget applied when an instance does not configure
// one.
func DefaultLimits() Limits {
	return Limits{
		RequestsPerSecond: DefaultRequestsPerSecond,
		Burst:             DefaultBurst,
		MaxConcurrent:     DefaultMaxConcurrent,
	}
}

// IsZero reports whether no budget is configured.
func (l Limits) IsZero() bool {
	return l.RequestsPerSecond <= 0 && l.Burst <= 0 && l.MaxConcurrent <= 0
}

// Validate rejects a budget that cannot be honoured.
func (l Limits) Validate() error {
	if l.RequestsPerSecond < 0 {
		return invalidf("limits.requests_per_second must not be negative")
	}
	if l.Burst < 0 {
		return invalidf("limits.burst must not be negative")
	}
	if l.MaxConcurrent < 0 {
		return invalidf("limits.max_concurrent must not be negative")
	}
	if l.Burst > 0 && l.RequestsPerSecond <= 0 {
		return invalidf("limits.burst requires limits.requests_per_second to be greater than zero")
	}
	return nil
}

// limiter is a token bucket plus a concurrency semaphore for one instance.
//
// It is safe for concurrent use. Waiting honours the caller's context, so a
// budget that cannot be met before the tool deadline fails as rate_limited
// instead of stalling the call.
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	sem    chan struct{}

	// now and sleep are seams for tests; production uses the real clock.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// newLimiter returns a limiter for the budget, or nil when the budget is
// unlimited. A nil limiter is the fast path: no lock, no channel.
func newLimiter(l Limits) *limiter {
	return newLimiterWithClock(l, time.Now, sleepCtx)
}

// newLimiterWithClock is newLimiter with the time seams injected, so tests can
// exercise refill without sleeping. The clock must be supplied at construction:
// the bucket records its start instant from it.
func newLimiterWithClock(l Limits, now func() time.Time, sleep func(context.Context, time.Duration) error) *limiter {
	if l.IsZero() {
		return nil
	}
	lim := &limiter{
		rate:  l.RequestsPerSecond,
		burst: float64(l.Burst),
		now:   now,
		sleep: sleep,
	}
	if lim.rate > 0 && lim.burst <= 0 {
		lim.burst = 1
	}
	lim.tokens = lim.burst
	lim.last = lim.now()
	if l.MaxConcurrent > 0 {
		lim.sem = make(chan struct{}, l.MaxConcurrent)
	}
	return lim
}

// acquire blocks until the caller may issue one request, and returns a release
// function that must be called when the request finishes.
func (l *limiter) acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	if l.sem != nil {
		select {
		case l.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, budgetExhausted(ctx)
		}
	}
	release := func() {
		if l.sem != nil {
			<-l.sem
		}
	}
	if l.rate <= 0 {
		return release, nil
	}
	for {
		wait, ok := l.take()
		if ok {
			return release, nil
		}
		if err := l.sleep(ctx, wait); err != nil {
			release()
			return nil, budgetExhausted(ctx)
		}
	}
}

// take consumes one token if available and otherwise reports how long the
// caller must wait for the next one.
func (l *limiter) take() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0, true
	}
	deficit := 1 - l.tokens
	wait := time.Duration(deficit / l.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Millisecond
	}
	return wait, false
}

func budgetExhausted(ctx context.Context) error {
	e := errs.New(errs.CodeRateLimited,
		"the Magento instance request budget was exhausted before the call deadline; retry later").
		WithDetail("reason", "instance_budget_exhausted").
		WithCause(ctx.Err())
	// The condition is transient by construction: the bucket refills.
	e.Retryable = true
	return e
}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
