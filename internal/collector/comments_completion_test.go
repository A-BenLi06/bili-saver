package collector

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestMainEndCannotOverrideIncompleteSubReplies(t *testing.T) {
	for _, limited := range []bool{true, false} {
		t.Run(map[bool]string{true: "limited", false: "failed"}[limited], func(t *testing.T) {
			mock := newMockBili(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/x/v2/reply/wbi/main":
					writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"cursor": map[string]any{"is_end": true}, "replies": []map[string]any{{"rpid": 1, "rcount": 2, "content": map[string]any{"message": "主楼"}, "replies": []map[string]any{{"rpid": 2, "root": 1, "content": map[string]any{"message": "预览子楼"}}}}}}})
				case "/x/v2/reply/reply":
					writeJSON(w, map[string]any{"code": -400, "message": "模拟楼中楼失败"})
				default:
					mock.srv.Config.Handler.ServeHTTP(w, r)
				}
			}))
			defer srv.Close()
			cfg := config.Defaults()
			cfg.DataDir = t.TempDir()
			cfg.Collector.CommentsMax = 0
			if limited {
				cfg.Collector.SubRepliesMax = 1
			} else {
				cfg.Collector.SubRepliesMax = 0
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			api, err := biliapi.New(writeLoginCookie(t), 1, "test")
			if err != nil {
				t.Fatal(err)
			}
			api.SetBase(srv.URL, srv.URL)
			col := New(st, api, cfg, nil, nil)
			api.SetSnapshotHook(col.SnapshotHook())
			seen, complete, err := col.collectComments(t.Context(), 111, "BVChild", 100, false)
			if err != nil || complete || seen != 2 {
				t.Fatalf("seen=%d complete=%v err=%v", seen, complete, err)
			}
			rows, err := st.ListComments("BVChild", "", 0, 0)
			if err != nil || len(rows) != 2 {
				t.Fatalf("partial origin lost: %d %v", len(rows), err)
			}
		})
	}
}
