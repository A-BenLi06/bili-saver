package jobs

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func executionStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestVideoWaitDoesNotConsumeWorkersAndReleasesAfterPanic(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 2)
	entered := make(chan struct{})
	gate := make(chan struct{})
	done := make(chan string, 10)
	var mu sync.Mutex
	active := map[string]int{}
	h := func(ctx context.Context, j *store.Job, p ProgressFn) error {
		m.mu.Lock()
		key := m.videoKeys[j.ID]
		m.mu.Unlock()
		mu.Lock()
		active[key]++
		if active[key] > 1 {
			t.Errorf("same video concurrent: %s", key)
		}
		mu.Unlock()
		defer func() { mu.Lock(); active[key]--; mu.Unlock() }()
		if j.Type == "hold" {
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
			panic("release video after panic")
		}
		done <- key
		return nil
	}
	for _, typ := range []string{"hold", "archive", "download", "touch_stats"} {
		m.RegisterVideo(typ, h)
	}
	m.Start(t.Context())
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer func() { release(); m.Stop() }()
	if _, err := m.Submit("hold", map[string]any{"bvid": "A"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hold never started")
	}
	for _, typ := range []string{"archive", "download", "touch_stats"} {
		if _, err := m.Submit(typ, map[string]any{"bvid": "A"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Submit("archive", map[string]any{"bvid": "B"}); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-done:
		if key != "B" {
			t.Fatalf("blocked A consumed slot: %s", key)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated video blocked behind A")
	}
	release()
	for i := 0; i < 3; i++ {
		select {
		case key := <-done:
			if key != "A" {
				t.Fatal(key)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("parked job lost after panic")
		}
	}
	m.Stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.videoKeys)+len(m.blockedVideos)+len(m.activeVideos) != 0 {
		t.Fatal("video reservations leaked")
	}
}

func TestVideoRecoveryPriorityAndOriginalFIFO(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 1)
	done := make(chan int64, 10)
	m.RegisterVideo("archive", func(ctx context.Context, j *store.Job, p ProgressFn) error { done <- j.ID; return nil })
	ids := make([]int64, 4)
	for i, key := range []string{"A", "A", "B", "A"} {
		id, err := st.CreateJobPrio("archive", `{"bvid":"`+key+`","n":`+string(rune('0'+i))+`}`, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	if err := st.UpdateJobState(ids[0], "running", "", "", 0); err != nil {
		t.Fatal(err)
	}
	high, err := st.CreateJobPrio("archive", `{"bvid":"A","high":true}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.Start(t.Context())
	defer m.Stop()
	for _, want := range append([]int64{high}, ids...) {
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("got %d want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("recovery stalled")
		}
	}
}

func TestProgressCoalescesConcurrentCallbacksAndRetainsFailure(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 1)
	var late ProgressFn
	m.Register("progress", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		late = p
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < 1000; n++ {
					p(1, "完成")
				}
			}()
		}
		wg.Wait()
		p(math.NaN(), "invalid")
		p(0.7, "最后步骤失败")
		return errors.New("failure")
	})
	id, err := st.CreateJob("progress", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`CREATE TABLE writes(n INTEGER); INSERT INTO writes VALUES(0); CREATE TRIGGER progress_writes AFTER UPDATE ON jobs BEGIN UPDATE writes SET n=n+1; END`); err != nil {
		t.Fatal(err)
	}
	m.process(t.Context(), id)
	late(1, "late callback")
	j, err := st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != "failed" || j.Progress != 0.7 || j.Detail != "最后步骤失败" {
		t.Fatalf("latest progress lost: %+v", j)
	}
	var n int
	st.DB.QueryRow(`SELECT n FROM writes`).Scan(&n)
	if n != 3 {
		t.Fatalf("8000 callbacks caused %d writes, want 3", n)
	}
}

func TestCanceledVideoJobsRemainQueued(t *testing.T) {
	st := executionStore(t)
	m := NewManager(st, nil, 2)
	entered := make(chan struct{})
	m.RegisterVideo("archive", func(ctx context.Context, j *store.Job, p ProgressFn) error {
		close(entered)
		p(0.4, "running")
		<-ctx.Done()
		return ctx.Err()
	})
	id, err := m.Submit("archive", map[string]any{"bvid": "A", "n": 1})
	if err != nil {
		t.Fatal(err)
	}
	next, err := m.Submit("archive", map[string]any{"bvid": "A", "n": 2})
	if err != nil {
		t.Fatal(err)
	}
	m.Start(t.Context())
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	m.Stop()
	for _, id := range []int64{id, next} {
		j, err := st.GetJob(id)
		if err != nil || j.State != "queued" {
			t.Fatalf("canceled: %+v %v", j, err)
		}
	}
}
