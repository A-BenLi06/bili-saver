package jobs

import (
	"bili-saver/internal/store"
	"bili-saver/internal/taskflow"
	"context"
	"testing"
	"time"
)

func TestSingleWorkerYieldsToControlAndKeepsVideoReservation(t *testing.T) {
	m := NewManager(executionStore(t), nil, 1)
	m.PreferAtYield("quick", "touch")
	entered := make(chan struct{})
	gate := make(chan struct{})
	events := make(chan string, 8)
	m.RegisterVideo("long", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		close(entered)
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := taskflow.Yield(ctx); err != nil {
			return err
		}
		events <- "long"
		return nil
	})
	m.Register("quick", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		events <- "quick"
		return taskflow.Yield(ctx)
	})
	m.RegisterVideo("touch", func(ctx context.Context, j *store.Job, p ProgressFn) error { events <- "touch"; return nil })
	if _, err := m.Submit("long", map[string]any{"bvid": "A"}); err != nil {
		t.Fatal(err)
	}
	m.Start(t.Context())
	defer m.Stop()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not entered")
	}
	if _, err := m.Submit("touch", map[string]any{"bvid": "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit("quick", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	for _, want := range []string{"quick", "long", "touch"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("%s want %s", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("yield deadlocked")
		}
	}
	m.Stop()
	if m.readySizeLocked() != 0 || len(m.controlJobs) != 0 || len(m.videoKeys) != 0 {
		t.Fatal("queue reservation leak")
	}
}

func TestControlAndOrdinaryDispatchKeepPriorityFIFO(t *testing.T) {
	m := NewManager(executionStore(t), nil, 1)
	m.PreferAtYield("short")
	events := make(chan int64, 8)
	h := func(ctx context.Context, j *store.Job, p ProgressFn) error { events <- j.ID; return nil }
	m.Register("short", h)
	m.Register("long", h)
	var ordinary []int64
	for _, typ := range []string{"long", "short", "long", "short"} {
		id, err := m.Submit(typ, map[string]any{"n": len(ordinary)})
		if err != nil {
			t.Fatal(err)
		}
		ordinary = append(ordinary, id)
	}
	id, err := m.SubmitPrio("short", map[string]any{"high": true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(t.Context())
	defer m.Stop()
	for _, want := range append([]int64{id}, ordinary...) {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("%d want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("dispatch stalled")
		}
	}
}
