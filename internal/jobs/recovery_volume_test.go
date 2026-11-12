package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestRecoveryLoadsAllBacklogBeyond5000(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO jobs(type,payload_json,state,priority,created_at,updated_at) VALUES('blocked','{}','queued',0,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5001; i++ {
		if _, err := stmt.Exec(); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil, 1)
	started := make(chan int64, 1)
	m.Register("blocked", func(ctx context.Context, j *store.Job, _ ProgressFn) error {
		started <- j.ID
		<-ctx.Done()
		return ctx.Err()
	})
	m.Start(t.Context())
	t.Cleanup(m.Stop)
	select {
	case id := <-started:
		if id != 1 {
			t.Fatalf("FIFO first=%d", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovered worker did not start")
	}
	m.mu.Lock()
	remaining := m.pending.size
	m.mu.Unlock()
	if remaining != 5000 {
		t.Fatalf("truncated backlog: %d pending + 1 running", remaining)
	}
	m.Stop()
	queued, err := s.QueuedJobs()
	if err != nil || len(queued) != 5001 {
		t.Fatalf("stop lost backlog: %d %v", len(queued), err)
	}
}
