package jobs

import (
	"bili-saver/internal/store"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartFailsClosedOnRecoveryError(t *testing.T) {
	st := executionStore(t)
	st.Close()
	m := NewManager(st, nil, 1)
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("start accepted unavailable durable queue")
	}
	if m.started {
		t.Fatal("workers started after recovery failure")
	}
}
func TestTerminalStateRetriesWithoutReexecuting(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 1)
	var called atomic.Int32
	_, err := st.DB.Exec(`CREATE TRIGGER fail_terminal BEFORE UPDATE ON jobs WHEN new.state='done' BEGIN SELECT RAISE(FAIL,'temporary failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	m.Register("test", func(context.Context, *store.Job, ProgressFn) error { called.Add(1); close(finished); return nil })
	if err = m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	id, err := m.Submit("test", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-finished
	time.Sleep(150 * time.Millisecond)
	if _, err = st.DB.Exec(`DROP TRIGGER fail_terminal`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := st.GetJob(id)
		if j.State == "done" {
			if called.Load() != 1 {
				t.Fatal("handler replayed for terminal retry")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("terminal write never recovered")
}
func TestStalledJobFailsWithoutRetryLoop(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 1)
	m.StallTimeout = 30 * time.Millisecond
	m.Register("stall", func(ctx context.Context, _ *store.Job, _ ProgressFn) error { <-ctx.Done(); return ctx.Err() })
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	id, _ := m.Submit("stall", nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := st.GetJob(id)
		if j.State == "failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stalled job not failed")
}
