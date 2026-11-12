package jobs

// 优先级派发测试：新添加补档插队（prio>0 先于 prio=0），同优先级 FIFO，
// 恢复装载按 priority DESC,id ASC。

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestPriorityDispatch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	m := NewManager(s, slog.Default(), 1) // 单 worker 保证顺序可断言
	var mu sync.Mutex
	var order []int64
	gate := make(chan struct{}) // 首个任务阻塞，制造积压
	startedA := make(chan struct{})
	done := make(chan int64, 8)
	var gateOnce sync.Once
	closeGate := func() { gateOnce.Do(func() { close(gate) }) }

	m.Register("gated", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		close(startedA) // 通知已被 worker 取走
		<-gate
		mu.Lock()
		order = append(order, j.ID)
		mu.Unlock()
		done <- j.ID
		return nil
	})
	m.Register("plain", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		mu.Lock()
		order = append(order, j.ID)
		mu.Unlock()
		done <- j.ID
		return nil
	})

	m.Start(t.Context())
	defer func() { closeGate(); m.Stop() }()

	idA, err := m.Submit("gated", map[string]any{"n": "A"}) // 占住唯一 worker
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedA:
	case <-time.After(5 * time.Second):
		t.Fatalf("A 未被 worker 执行")
	}
	submit := func(n string, prio int) int64 {
		t.Helper()
		id, err := m.SubmitPrio("plain", map[string]any{"n": n}, prio)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	idB := submit("B", 0)
	idC := submit("C", 1)
	idD := submit("D", 0)

	closeGate() // 放行积压
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("任务未完成: %v", order)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []int64{idA, idC, idB, idD} // C 优先插队，B/D 保持 FIFO
	if len(order) != 4 {
		t.Fatalf("应完成 4 个任务: %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("执行顺序错误: got %v, want %v", order, want)
		}
	}
}

func TestRecoveryPriorityOrder(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var seq []int64
	for i, prio := range []int{0, 1, 0, 1} {
		id, err := s.CreateJobPrio("plain", fmt.Sprintf(`{"n":%d,"prio":%d}`, i, prio), prio)
		if err != nil {
			t.Fatal(err)
		}
		seq = append(seq, id)
	}
	ids, err := s.QueuedJobIDs(100)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{seq[1], seq[3], seq[0], seq[2]} // prio1 在前（按 id），prio0 在后
	if len(ids) != len(want) {
		t.Fatalf("恢复任务数量错误: %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("恢复顺序错误: got %v, want %v", ids, want)
		}
	}
}

// 恢复时必须保留真实优先级，新提交任务不能越过同级恢复任务。
func TestRecoveredPriorityWithNewSubmissions(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := NewManager(s, nil, 1)
	gate, started := make(chan struct{}), make(chan struct{})
	finished := make(chan int64, 8)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer func() { release(); m.Stop() }()
	m.Register("gated", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		close(started)
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		finished <- j.ID
		return nil
	})
	m.Register("plain", func(ctx context.Context, j *store.Job, p ProgressFn) error { finished <- j.ID; return nil })
	create := func(kind, payload string, prio int) int64 {
		t.Helper()
		id, err := s.CreateJobPrio(kind, payload, prio)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := create("gated", `{}`, 1)
	if err := s.UpdateJobState(a, "running", "", "", 0); err != nil {
		t.Fatal(err)
	}
	b := create("plain", `{"n":"B"}`, 1)
	c := create("plain", `{"n":"C"}`, 0)
	// Start 前提交也落库，恢复后只应执行一次。
	d, err := m.Submit("plain", map[string]string{"n": "D"})
	if err != nil {
		t.Fatal(err)
	}
	m.Start(t.Context())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("恢复任务未开始")
	}
	e, err := m.SubmitPrio("plain", map[string]string{"n": "E"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, want := range []int64{a, b, e, c, d} {
		select {
		case got := <-finished:
			if got != want {
				t.Fatalf("恢复后派发顺序: got %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("恢复任务未完成")
		}
	}
	m.Stop()
	select {
	case id := <-finished:
		t.Fatalf("重复执行恢复任务 %d", id)
	default:
	}
}

func TestConcurrentSubmissionDeduplicates(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := NewManager(s, nil, 1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, err := m.Submit("plain", map[string]string{"bvid": "same"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	queued, err := s.ListJobs(-1, []string{"queued"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("重复活动任务: %d", len(queued))
	}
}

func TestIdleWorkerExitsOnContextCancellation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := NewManager(s, nil, 1)
	ctx, cancel := context.WithCancel(t.Context())
	m.Start(ctx)
	defer m.Stop()
	cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后空闲 worker 未退出")
	}
}
