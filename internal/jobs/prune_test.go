package jobs

// 终态任务滚动保留回归：done/failed 只留最近 N 条，queued/running 不受影响；
// 调度器侧 24h 节流生效（jobs 表曾被失败循环刷到数千条，见 P-009）。

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestPruneTerminalJobs(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	insert := func(typ, state string) {
		t.Helper()
		if _, err := s.DB.Exec(`INSERT INTO jobs (type, payload_json, state, created_at, updated_at) VALUES (?, '{}', ?, 1, 1)`, typ, state); err != nil {
			t.Fatal(err)
		}
	}
	// 8 条终态（id 1..8）+ 2 条活动任务
	for i := 0; i < 5; i++ {
		insert("t", "done")
	}
	for i := 0; i < 3; i++ {
		insert("t", "failed")
	}
	insert("t", "queued")
	insert("t", "running")

	n, err := s.PruneTerminalJobs(5)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应删除 3 条最旧终态任务，实际 %d", n)
	}
	var states struct{ done, failed, queued, running int }
	if err := s.DB.QueryRow(`SELECT
		SUM(state='done'), SUM(state='failed'),
		SUM(state='queued'), SUM(state='running') FROM jobs`).
		Scan(&states.done, &states.failed, &states.queued, &states.running); err != nil {
		t.Fatal(err)
	}
	if states.done != 3 || states.failed != 2 || states.queued != 1 || states.running != 1 {
		t.Fatalf("终态应保留最近 5 条(done 3 + failed 2)，活动任务不动: %+v", states)
	}
	// 已在窗口内：再清一次无变化
	if n, err := s.PruneTerminalJobs(5); err != nil || n != 0 {
		t.Fatalf("幂等清理应删 0 条: n=%d err=%v", n, err)
	}
}

func TestPruneJobsIfDueGating(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB.Exec(`INSERT INTO jobs (type, payload_json, state, created_at, updated_at) VALUES ('t','{}','done',1,1)`); err != nil {
		t.Fatal(err)
	}
	sch := &Scheduler{St: s, Log: slog.Default(), Every: time.Minute}

	sch.pruneJobsIfDue() // lastPruneAt=0 → 首轮即清理
	if sch.lastPruneAt == 0 {
		t.Fatal("首轮清理后应记录时间戳")
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='done'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("终态任务应被清空: n=%d err=%v", n, err)
	}

	// 24h 节流窗口内不重复清理：放入一条终态任务后触发 tick 不应删除
	if _, err := s.DB.Exec(`INSERT INTO jobs (type, payload_json, state, created_at, updated_at) VALUES ('t','{}','done',1,1)`); err != nil {
		t.Fatal(err)
	}
	sch.pruneJobsIfDue()
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='done'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("节流窗口内不应清理: n=%d err=%v", n, err)
	}

	// 窗口过后恢复清理
	sch.lastPruneAt -= pruneJobsEverySec + 10
	sch.pruneJobsIfDue()
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='done'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("窗口过后应恢复清理: n=%d err=%v", n, err)
	}
}
