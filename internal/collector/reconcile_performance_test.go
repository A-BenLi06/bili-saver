package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestVideoGateCancellationAndIsolation(t *testing.T) {
	var gates videoGates
	release, err := gates.acquire(t.Context(), "BVa")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, err := gates.acquire(t.Context(), "BVb")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		r, err := gates.acquire(ctx, "BVa")
		if r != nil {
			r()
		}
		result <- err
	}()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation: %v", err)
	}
	gates.mu.Lock()
	defer gates.mu.Unlock()
	if len(gates.items) != 1 || gates.items["BVa"].refs != 1 {
		t.Fatalf("leaked gates: %+v", gates.items)
	}
}

func TestPartialRefreshNeverReconcilesSkippedComments(t *testing.T) {
	mock := newMockBili(t)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api, err := biliapi.New(writeLoginCookie(t), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	api.SetBase(mock.srv.URL, mock.srv.URL)
	col := New(st, api, cfg, nil, nil)
	if _, err := col.ArchiveVideo(t.Context(), "BV1Test00001"); err != nil {
		t.Fatal(err)
	}
	before, deletedBefore, err := st.CountCommentsByStatus("BV1Test00001")
	if err != nil {
		t.Fatal(err)
	}
	res, err := col.ArchiveVideoOpts(t.Context(), "BV1Test00001", CollectOptions{RefreshDanmaku: true})
	if err != nil {
		t.Fatal(err)
	}
	after, deletedAfter, err := st.CountCommentsByStatus("BV1Test00001")
	if err != nil || res.Complete || res.CommentsDeleted != 0 || after != before || deletedAfter != deletedBefore {
		t.Fatalf("skipped comments reconciled: %+v before=%d/%d after=%d/%d err=%v", res, before, deletedBefore, after, deletedAfter, err)
	}
}

func TestGoneStatsCheckAdvancesCooldownWithoutChildRequests(t *testing.T) {
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/x/web-interface/view" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		writeJSON(w, map[string]any{"code": -404, "message": "gone"})
	}))
	t.Cleanup(mock.Close)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertVideo(&store.Video{Bvid: "BVgone", FirstArchivedAt: 1, LastStatsAt: 1, LastCommentsAt: 100}); err != nil {
		t.Fatal(err)
	}
	api, err := biliapi.New(filepath.Join(cfg.DataDir, "cookies.json"), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	api.SetBase(mock.URL, mock.URL)
	col := New(st, api, cfg, nil, nil)
	if err := col.SyncVideoStats(t.Context(), "BVgone"); err != nil {
		t.Fatal(err)
	}
	v, err := st.GetVideo("BVgone")
	if err != nil || v.Status != "gone" || v.LastStatsAt <= 1 || v.LastCommentsAt != 100 {
		t.Fatalf("gone cooldown: %+v %v", v, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one existence request, got %d", calls.Load())
	}
}

func TestVideoGateSerializesSameVideo(t *testing.T) {
	var gates videoGates
	var active atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gates.acquire(t.Context(), "BVshared")
			if err != nil {
				t.Error(err)
				return
			}
			if active.Add(1) != 1 {
				t.Error("same video ran concurrently")
			}
			active.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if len(gates.items) != 0 {
		t.Fatal("gate entries were not reclaimed")
	}
}
