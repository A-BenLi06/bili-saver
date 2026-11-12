package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/jobs"
	"bili-saver/internal/store"
)

func TestPersonalEndpointsIsolationAndTeriteriPublic(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Normalize()
	cfg.Admin.Token = "test-admin"
	u, err := st.CreateUser("alice", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.CreateUser("bob", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range []struct {
		uid  int64
		bvid string
	}{{u.ID, "BV1xx411c7mD"}, {v.ID, "BV1xx411c7mE"}} {
		if err := st.UpsertVideo(&store.Video{Bvid: p.bvid, Title: fmt.Sprintf("Video %d", i), LocalPath: "C:/private/secret.mp4", Status: "ok"}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddUserVideo(p.uid, p.bvid); err != nil {
			t.Fatal(err)
		}
	}
	session, err := st.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	mgr := jobs.NewManager(st, nil, 1)
	h := New(Deps{Cfg: cfg, St: st, Mgr: mgr}).Handler()
	call := func(method, path, body, auth string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth == "user" {
			r.AddCookie(&http.Cookie{Name: "bs_sess", Value: session})
		} else if auth == "admin" {
			r.Header.Set("Authorization", "Bearer test-admin")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/videos?scope=all", "/api/v1/videos?scope=mine", "/api/v1/videos?q=Video"} {
		w := call("GET", path, "", "user")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got struct {
			Total  int
			Counts map[string]int
			Items  []store.VideoWithCounts
		}
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 1 || got.Counts["ok"] != 1 || len(got.Items) != 1 || got.Items[0].Bvid != "BV1xx411c7mD" || got.Items[0].LocalPath != "" {
			t.Fatal(w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/videos/BV1xx411c7mE", "/api/v1/videos/BV1xx411c7mE/download"} {
		method := "GET"
		if strings.HasSuffix(path, "download") {
			method = "POST"
		}
		w := call(method, path, "", "user")
		if w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/videos/BV1xx411c7mD/local-open", "/api/v1/videos/BV1xx411c7mD/archive", "/api/v1/jobs/1/retry", "/api/v1/tiers"} {
		w := call("POST", path, "{}", "user")
		if w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := call("GET", "/api/v1/videos/BV1xx411c7mD", "", "user")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret.mp4") || strings.Contains(w.Body.String(), "file_links") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/videos", "", "admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/v1/videos?scope=mine", "", "admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/video/getone?vid=BV1xx411c7mE", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/v1/auth/me", "", "user")
	if !strings.Contains(w.Body.String(), "download_quota") || !strings.Contains(w.Body.String(), "max_daily_downloads") {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/media/bili-saver.db", "", "")
	if w.Code != 403 {
		t.Fatal("database must not be public", w.Code)
	}
	w = call("GET", "/media/media/", "", "")
	if w.Code != 403 {
		t.Fatal("directory listing must not be public", w.Code)
	}
	// Explicit download=true must still create an owned source + quota request, never an inline download.
	w = call("POST", "/api/v1/add", `{"input":"BV1xx411c7mD","download":true,"auto_download":1,"sync_mode":"interval"}`, "user")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	list, err := st.ListJobs(1, nil)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if !strings.Contains(list[0].PayloadJSON, `"download":false`) || !strings.Contains(list[0].PayloadJSON, `"source_id":`) {
		t.Fatal(list[0])
	}
	if _, err = st.UserDownload(u.ID, "BV1xx411c7mD"); err != nil {
		t.Fatal(err)
	}
	// Other user's identical source is independent, and cannot be edited by Alice.
	other := &store.Source{Type: "video", ExtID: "BV1xx411c7mD", UserID: v.ID, Watch: true}
	id, err := st.UpsertSource(other)
	if err != nil {
		t.Fatal(err)
	}
	w = call("PATCH", fmt.Sprintf("/api/v1/sources/%d", id), `{"watch":false}`, "user")
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	// 0 is a real download prohibition, not unlimited.
	w = call("PATCH", "/api/v1/tiers/1", `{"name":"基础","max_daily_adds":10,"min_interval_minutes":60,"max_daily_downloads":0}`, "admin")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call("POST", "/api/v1/videos/BV1xx411c7mD/download", "", "user")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "waiting_quota") {
		t.Fatal(w.Code, w.Body.String())
	}
}
