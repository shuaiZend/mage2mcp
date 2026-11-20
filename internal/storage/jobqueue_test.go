// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// PERF-19: the audit recorded a fixed 5-second retry with no growth and no jitter.
// The scheduling SQL already computes 5s * 2^(attempt-1), capped at 5 minutes, with
// ±25% jitter. This asserts the growth rather than trusting the expression, so a
// future simplification back to a constant fails here.
func TestJobRetryBackoffGrowsWithAttempts(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	// Claim is global: it returns the oldest due job in the schema, whatever test
	// created it. Push leftovers out of the way so this test observes its own.
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET scheduled_at = now() + interval '1 hour'
		 WHERE state = 'queued' AND scheduled_at <= now()`); err != nil {
		t.Fatalf("defer leftover jobs: %v", err)
	}

	id, err := queue.Enqueue(ctx, jobs.Task{
		Kind: "platform:noop", Queue: "default", MaxAttempts: 5,
		IdempotencyKey: "backoff-" + runSuffix(t),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	var delays []time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		claimed, err := queue.Claim(ctx, []string{"default"}, time.Minute)
		if err != nil || claimed == nil || claimed.ID != id {
			t.Fatalf("Claim %d: claimed=%v err=%v", attempt, claimed, err)
		}
		if err := queue.Fail(ctx, id, claimed.LeaseToken, "upstream_unavailable", true); err != nil {
			t.Fatalf("Fail %d: %v", attempt, err)
		}
		var delay time.Duration
		if err := store.pool.QueryRow(ctx,
			`SELECT scheduled_at - now() FROM jobs WHERE id = $1::uuid`, id).Scan(&delay); err != nil {
			t.Fatalf("read scheduled_at: %v", err)
		}
		delays = append(delays, delay)
		// Make it due immediately so the next Claim picks it up.
		if _, err := store.pool.Exec(ctx,
			`UPDATE jobs SET scheduled_at = now() WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("reschedule: %v", err)
		}
	}
	if len(delays) != 3 {
		t.Fatalf("delays = %v", delays)
	}
	if !(delays[0] < delays[1] && delays[1] < delays[2]) {
		t.Fatalf("delays = %v, want each retry scheduled further out", delays)
	}
	// 5s, 10s, 20s with ±25% jitter; the third must be well beyond a constant 5s.
	if delays[2] < 12*time.Second {
		t.Fatalf("third delay = %v, want exponential growth past the 5s base", delays[2])
	}
}

// runSuffix keeps each run's idempotency keys unique on a persistent test database.
func runSuffix(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
