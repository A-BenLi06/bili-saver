package app

import (
	"bili-saver/internal/config"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/jobs"
	"bili-saver/internal/store"
)

func TestPersonalFirstScanAndDeleteSourceRetention(t *testing.T) {
	a := newTestApp(t)
	a.Log = slog.Default()
	editTestConfig(a, func(c *config.Config) { c.Collector.AdaptiveSync = false })
	u, err := a.St.CreateUser("scan-user", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bvid        string
		last        int64
		wantRequest bool
	}{{"BV1xx411c7mD", 0, true}, {"BV1xx411c7mE", store.Now(), false}} {
		src := &store.Source{Type: "video", ExtID: tc.bvid, UserID: u.ID, LastSyncAt: tc.last, AutoDownload: 1, Watch: true, CreatedAt: store.Now()}
		id, err := a.St.UpsertSource(src)
		if err != nil {
			t.Fatal(err)
		}
		if tc.last != 0 {
			if err = a.St.SetSourceSynced(id, tc.last); err != nil {
				t.Fatal(err)
			}
		}
		now := store.Now()
		if err = a.St.UpsertVideo(&store.Video{Bvid: tc.bvid, Status: "ok", LastStatsAt: now, LastCommentsAt: now, LastDanmakuAt: now, LastSubsAt: now}); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"source_id": id})
		if err = a.jobSyncSource(context.Background(), &store.Job{PayloadJSON: string(body)}, func(float64, string) {}); err != nil {
			t.Fatal(err)
		}
		if has, err := a.St.UserHasVideo(u.ID, tc.bvid); err != nil || !has {
			t.Fatal(has, err)
		}
		_, reqErr := a.St.UserDownload(u.ID, tc.bvid)
		if (reqErr == nil) != tc.wantRequest {
			t.Fatal(tc, reqErr)
		}
		if err = a.St.DeleteSource(id); err != nil {
			t.Fatal(err)
		}
		if has, err := a.St.UserHasVideo(u.ID, tc.bvid); err != nil || !has {
			t.Fatal(has, err)
		}
	}
	// The shared membership helper is also used for dynamic-video discoveries.
	if err = a.attachSourceVideo(&store.Source{ID: 999, UserID: u.ID}, "BVDynamic", true, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = a.St.UserDownload(u.ID, "BVDynamic"); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalDownloadRecoveryAndExistingFiles(t *testing.T) {
	a := newTestApp(t)
	u, err := a.St.CreateUser("download-user", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	bvid := "BV1xx411c7mD"
	if err = a.St.UpsertVideo(&store.Video{Bvid: bvid, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err = a.St.UpsertPage(&store.Page{Bvid: bvid, Cid: 123, Page: 1}); err != nil {
		t.Fatal(err)
	}
	if err = a.St.AddUserVideo(u.ID, bvid); err != nil {
		t.Fatal(err)
	}
	r, err := a.St.RequestUserDownload(u.ID, bvid, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _, _, err := a.St.QueueUserDownload(r.ID, store.Now(), false)
	if err != nil || jobID == 0 {
		t.Fatal(jobID, err)
	}
	// A crash leaves the same request, job and reservation durable; replay skips a file prepared meanwhile.
	if _, err = a.St.BeginUserDownload(r.ID, jobID); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.Cfg.DataDir, "media", bvid, "p1", bvid+"-p1.mp4")
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("existing mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := a.St.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.jobUserDownload(context.Background(), j, jobs.ProgressFn(func(float64, string) {})); err != nil {
		t.Fatal(err)
	}
	q, err := a.St.DownloadQuota(u.ID, store.Now())
	if err != nil || q.Used != 0 || q.Reserved != 0 {
		t.Fatal(q, err)
	}
	got, err := a.St.UserDownload(u.ID, bvid)
	if err != nil || got.State != "done" {
		t.Fatal(got, err)
	}
	if err = a.jobUserDownload(context.Background(), j, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	got, err = a.St.UserDownload(u.ID, bvid)
	if err != nil || got.State != "done" {
		t.Fatal("replay reopened completed request", got, err)
	}
}
