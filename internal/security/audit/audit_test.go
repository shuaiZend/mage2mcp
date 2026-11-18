// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		Subject:       "user-1",
		IdentityClass: "human",
		Tool:          "magento.product.get",
		Instance:      "store-us",
		Scope:         "read",
		Decision:      "allow",
		Phase:         "intent",
		State:         "pending",
	}
}

func TestRecordRequiresFields(t *testing.T) {
	m := NewMemory()
	cases := []func(*Event){
		func(e *Event) { e.Subject = "" },
		func(e *Event) { e.Tool = "" },
		func(e *Event) { e.Decision = "" },
		func(e *Event) { e.Phase = "" },
	}
	for i, mutate := range cases {
		e := validEvent()
		mutate(&e)
		if err := m.Record(context.Background(), e); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestRecordAssignsDefaultsAndKeepsCopy(t *testing.T) {
	m := NewMemory()
	if err := m.Record(context.Background(), validEvent()); err != nil {
		t.Fatal(err)
	}
	events := m.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].EventID == "" || events[0].OccurredAt.IsZero() {
		t.Fatal("defaults not assigned")
	}
	events[0].Subject = "tampered"
	if m.Events()[0].Subject != "user-1" {
		t.Fatal("Events must return a copy")
	}
}

func TestRecordHonorsCancelledContext(t *testing.T) {
	m := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Record(ctx, validEvent()); err == nil {
		t.Fatal("cancelled context must not be written")
	}
	if len(m.Events()) != 0 {
		t.Fatal("event written despite cancellation")
	}
}

func TestRecordNilRecorder(t *testing.T) {
	var m *MemoryRecorder
	if err := m.Record(context.Background(), validEvent()); err == nil {
		t.Fatal("nil recorder must error")
	}
}

func TestConcurrentRecord(t *testing.T) {
	m := NewMemory()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Record(context.Background(), validEvent()); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(m.Events()); got != 50 {
		t.Fatalf("events = %d, want 50", got)
	}
}

func TestOccurredAtPreserved(t *testing.T) {
	m := NewMemory()
	e := validEvent()
	e.OccurredAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := m.Record(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !m.Events()[0].OccurredAt.Equal(e.OccurredAt) {
		t.Fatal("explicit OccurredAt overwritten")
	}
}
