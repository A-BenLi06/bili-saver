package server

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/jobs"
	"bili-saver/internal/store"
)

func TestAddPrioritySetting(t *testing.T) {
	cfg := config.Defaults()
	cfg.Admin.Token = "priority-test"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr := jobs.NewManager(st, nil, 1)
	h := New(Deps{Cfg: cfg, St: st, Mgr: mgr}).Handler()
	for _, tc := range []struct {
		body, jobType string
		enabled       bool
	}{
		{`{"input":"BV1xx411c7mD","confirm_hot":true}`, "archive", true},
		{`{"input":"BV1xx411c7mD","sync_mode":"once","confirm_hot":true}`, "sync_source", true},
		{`{"input":"https://space.bilibili.com/12345/","download":false}`, "sync_source", true},
		{`{"input":"BV1xx411c7mE","confirm_hot":true}`, "archive", false},
		{`{"input":"BV1xx411c7mE","sync_mode":"once","confirm_hot":true}`, "sync_source", false},
		{`{"input":"https://space.bilibili.com/12346/","download":false}`, "sync_source", false},
	} {
		cfg.Queue.NewAddFirst = tc.enabled
		req := httptest.NewRequest("POST", "/api/v1/add", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer priority-test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("添加失败: %d %s", w.Code, w.Body.String())
		}
		list, err := st.ListJobs(1, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if tc.enabled {
			want = 1
		}
		if len(list) != 1 || list[0].Type != tc.jobType || list[0].Priority != want {
			t.Fatalf("添加的任务未使用当前设置: %+v", list)
		}
	}
}
