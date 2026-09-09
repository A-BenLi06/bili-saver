package jobs

// 任务处理器 panic 恢复回归：panic 必须变成任务 failed，服务存活继续处理后续任务
// （2026-09-09：card 渲染 panic 曾带崩整个服务）。

import (
	"context"
	"fmt"
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

func TestSyncSourceBlockedBacksOff(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	base := int64(100000)
	insertJob := func(srcID int64, state string, at int64) {
		t.Helper()
		if _, err := s.DB.Exec(`INSERT INTO jobs (type, payload_json, state, created_at, updated_at) VALUES ('sync_source', ?, ?, ?, ?)`,
			fmt.Sprintf(`{"source_id":%d}`, srcID), state, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// 源 7：6 连败，最近一次在 base-5
	for i := int64(6); i >= 1; i-- {
		insertJob(7, "failed", base-i)
	}
	// 源 9：仅 4 连败
	for i := int64(4); i >= 1; i-- {
		insertJob(9, "failed", base-i)
	}
	sch := &Scheduler{St: s, Log: slog.Default(), Every: time.Minute}

	if !sch.syncSourceBlocked(7, base) {
		t.Fatal("连续失败≥5 次的源应退避")
	}
	if sch.syncSourceBlocked(9, base) {
		t.Fatal("连续失败仅 4 次不应退避")
	}
	if sch.syncSourceBlocked(8, base) {
		t.Fatal("无任务记录的源不应退避")
	}
	// 退避窗口已过（上次尝试在 2h 前）→ 放行
	for i := int64(6); i >= 1; i-- {
		insertJob(10, "failed", base-7200-i)
	}
	if sch.syncSourceBlocked(10, base) {
		t.Fatal("退避窗口过后应放行")
	}
	// 成功任务清零前导失败
	insertJob(7, "done", base-1)
	if sch.syncSourceBlocked(7, base) {
		t.Fatal("成功任务应解除退避")
	}
}
