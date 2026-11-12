package app

import (
	"bili-saver/internal/config"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"bili-saver/internal/store"
)

func TestGoneSourceSchedulesOnlyExistenceCheck(t *testing.T) {
	a := newTestApp(t)
	editTestConfig(a, func(c *config.Config) { c.Collector.AdaptiveSync = false })
	now := store.Now()
	src := &store.Source{SyncIntervalMinutes: 60}
	v := &store.Video{Bvid: "BVgone", Status: "gone", LastStatsAt: now}
	stats, comments, dm, subs := a.dueParts(src, v, now)
	if stats || comments || dm || subs {
		t.Fatal("gone video should cool down")
	}
	v.LastStatsAt = now - 3601
	stats, comments, dm, subs = a.dueParts(src, v, now)
	if !stats || comments || dm || subs {
		t.Fatal("gone video should use touch_stats")
	}
	v.Status = "ok"
	_, comments, dm, subs = a.dueParts(src, v, now)
	if !comments || !dm || !subs {
		t.Fatal("restored video should refill missing parts")
	}
}

func TestCancelledStatsSweepDoesNotTraverseVideos(t *testing.T) {
	a := newTestApp(t)
	if err := a.St.UpsertVideo(&store.Video{Bvid: "BVcancel", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	err := a.jobSyncStatsAll(ctx, &store.Job{}, func(float64, string) { calls++ })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled sweep: err=%v progress=%d", err, calls)
	}
}

func TestStatsSweepSpansPagesAndKeepsInitialWindow(t *testing.T) {
	a := newTestApp(t)
	a.Log = slog.Default()
	editTestConfig(a, func(c *config.Config) { c.Collector.AdaptiveSync = true })
	editTestConfig(a, func(c *config.Config) { c.Collector.StatsResyncHours = 24 })
	now := store.Now()
	for i := 0; i < 805; i++ {
		if err := a.St.UpsertVideo(&store.Video{Bvid: fmt.Sprintf("BVpage%04d", i), Pubdate: now, LastStatsAt: now, FirstArchivedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cooled video fetched: %s", r.URL)
		w.WriteHeader(500)
	}))
	defer mock.Close()
	a.API.SetBase(mock.URL, mock.URL)
	calls := 0
	err := a.jobSyncStatsAll(t.Context(), &store.Job{}, func(f float64, detail string) {
		calls++
		if f > 1 {
			t.Errorf("progress %f", f)
		}
		if calls == 1 {
			if err := a.St.UpsertVideo(&store.Video{Bvid: "BVnextSweep", FirstArchivedAt: 1}); err != nil {
				t.Error(err)
			}
		}
	})
	if err != nil || calls != 805 {
		t.Fatalf("sweep calls=%d err=%v", calls, err)
	}
}

func TestGoneRecoveryImmediatelyQueuesDueParts(t *testing.T) {
	for _, gone := range []bool{true, false} {
		t.Run(fmt.Sprintf("still_gone_%v", gone), func(t *testing.T) {
			a := newTestApp(t)
			editTestConfig(a, func(c *config.Config) { c.Collector.AdaptiveSync = false })
			src := &store.Source{Type: "video", ExtID: "BVrestore", SyncIntervalMinutes: 60, IntervalDanmakuMin: -1, IntervalSubsMin: -1}
			if _, err := a.St.UpsertSource(src); err != nil {
				t.Fatal(err)
			}
			if err := a.St.UpsertVideo(&store.Video{Bvid: "BVrestore", Status: "gone", FirstArchivedAt: 1, SourceID: src.ID}); err != nil {
				t.Fatal(err)
			}
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/x/web-interface/view" {
					t.Errorf("unexpected child request: %s", r.URL.Path)
				}
				body := map[string]any{"code": 0, "data": map[string]any{"bvid": "BVrestore", "aid": 111, "title": "restored"}}
				if gone {
					body = map[string]any{"code": -404, "message": "gone"}
				}
				json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(mock.Close)
			a.API.SetBase(mock.URL, mock.URL)
			j := &store.Job{Priority: 7, PayloadJSON: fmt.Sprintf(`{"bvid":"BVrestore","recover_parts":true,"source_id":%d,"download":false}`, src.ID)}
			if err := a.jobTouchStats(t.Context(), j, func(float64, string) {}); err != nil {
				t.Fatal(err)
			}
			queued, err := a.St.ListJobs(10, []string{"queued"})
			if err != nil {
				t.Fatal(err)
			}
			if gone {
				if len(queued) != 0 {
					t.Fatal("still gone should not queue archive")
				}
				return
			}
			if len(queued) != 1 || queued[0].Type != "archive" || queued[0].Priority != 7 {
				t.Fatalf("restoration queued: %+v", queued)
			}
			var p struct {
				RefreshComments  bool `json:"refresh_comments"`
				RefreshDanmaku   bool `json:"refresh_danmaku"`
				RefreshSubtitles bool `json:"refresh_subtitles"`
				Download         bool `json:"download"`
			}
			if err := json.Unmarshal([]byte(queued[0].PayloadJSON), &p); err != nil {
				t.Fatal(err)
			}
			if !p.RefreshComments || p.RefreshDanmaku || p.RefreshSubtitles || p.Download {
				t.Fatalf("ignored source policy: %+v", p)
			}
		})
	}
}
