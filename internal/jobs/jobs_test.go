package jobs

// 任务处理器 panic 恢复回归：panic 必须变成任务 failed，服务存活继续处理后续任务
// （2026-09-09：card 渲染 panic 曾带崩整个服务）。

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestHandlerPanicDoesNotKillWorker(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := NewManager(s, slog.Default(), 1)
	m.Register("panic_job", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		panic("模拟处理器崩溃")
	})
	processed := make(chan struct{})
	m.Register("next_job", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		close(processed)
		return nil
	})
	m.Start(t.Context())
	defer m.Stop()

	if _, err := m.Submit("panic_job", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit("next_job", map[string]any{}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-processed:
	case <-time.After(5 * time.Second):
		t.Fatal("panic 后 worker 应存活并继续处理后续任务")
	}
	j, err := s.GetJob(1)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != "failed" {
		t.Errorf("panic 任务应标 failed，实际 %s", j.State)
	}
}
