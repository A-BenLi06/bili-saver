package collector

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"bili-saver/internal/taskflow"
)

func scanCollector(t *testing.T, h http.Handler) *Collector {
	t.Helper()
	mock := newMockBili(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/reply/") {
			h.ServeHTTP(w, r)
		} else {
			mock.srv.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
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
	api.SetBase(srv.URL, srv.URL)
	return New(st, api, cfg, nil, nil)
}

func TestBoundedResyncDoesNotAffectFirstArchive(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			calls := 0
			c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"cursor": map[string]any{"is_end": calls == 7, "all_count": 7, "pagination_reply": map[string]any{"next_offset": fmt.Sprint(calls)}}, "replies": []map[string]any{{"rpid": calls, "content": map[string]any{"message": "comment"}}}}})
			}))
			c.Cfg.Collector.FullResyncComments = false
			seen, complete, err := c.collectComments(t.Context(), 1, "BVScan", 100, first)
			want := 5
			if first {
				want = 7
			}
			if err != nil || calls != want || seen != want || complete != first {
				t.Fatalf("seen=%d complete=%v calls=%d err=%v", seen, complete, calls, err)
			}
		})
	}
}

func TestSubRepliesPreviewDoesNotTruncateFullPagesAndTopDuplicates(t *testing.T) {
	subCalls := 0
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			preview := []map[string]any{}
			for i := 2; i <= 4; i++ {
				preview = append(preview, map[string]any{"rpid": i}) // 部分预览省略 root，父楼仍可明确定位。
			}
			root := map[string]any{"rpid": 1, "rcount": 23, "replies": preview}
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"cursor": map[string]any{"is_end": true, "all_count": 24}, "top_replies": []any{root}, "replies": []any{root}}})
			return
		}
		subCalls++
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		replies := []map[string]any{}
		for i := 2 + (pn-1)*20; i <= min(24, 1+pn*20); i++ {
			replies = append(replies, map[string]any{"rpid": i, "root": 1})
		}
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"page": map[string]any{"count": 23}, "replies": replies}})
	}))
	details := []string{}
	ctx := taskflow.WithHooks(t.Context(), func(f float64, d string) { details = append(details, d) }, nil)
	seen, complete, err := c.collectComments(ctx, 1, "BVScan", 100, false)
	if err != nil || seen != 24 || !complete || subCalls != 2 {
		t.Fatalf("seen=%d complete=%v calls=%d err=%v", seen, complete, subCalls, err)
	}
	if len(details) == 0 || !strings.Contains(details[len(details)-1], "评论 24 条") {
		t.Fatal(details)
	}
	rows, err := c.St.ListComments("BVScan", "", 0, 0)
	if err != nil || len(rows) != 24 {
		t.Fatalf("origin rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.LastSeenAt != 100 {
			t.Fatal("scan epoch changed")
		}
		if r.Rpid > 1 && (r.ParentRpid != 1 || r.RootRpid != 1) {
			t.Fatalf("sub reply hierarchy lost: %+v", r)
		}
	}
}

func TestBoundedResyncAlsoBoundsHugeSubReplyThreads(t *testing.T) {
	calls := 0
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"cursor": map[string]any{"is_end": true}, "replies": []any{map[string]any{"rpid": 1, "rcount": 20000}}}})
			return
		}
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		replies := []map[string]any{}
		for i := 0; i < 20; i++ {
			replies = append(replies, map[string]any{"rpid": pn*20 + i + 2, "root": 1})
		}
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"replies": replies}})
	}))
	c.Cfg.Collector.FullResyncComments = false
	seen, complete, err := c.collectComments(t.Context(), 1, "BVScan", 100, false)
	if err != nil || complete || calls != 10 || seen != 181 {
		t.Fatalf("seen=%d complete=%v calls=%d err=%v", seen, complete, calls, err)
	}
}
