package collector

import (
	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditFallbackDanmakuCannotEstablishCompleteCoverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "seg.so") {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`<i><d p="1,1,25,16777215,1,0,1,0">sample</d></i>`))
	}))
	defer srv.Close()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	st, e := store.Open(filepath.Join(cfg.DataDir, "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	api, e := biliapi.New("", 1, "test")
	if e != nil {
		t.Fatal(e)
	}
	api.SetBase(srv.URL, srv.URL)
	c := New(st, api, cfg, nil, nil)
	_, complete, e := c.collectDanmaku(context.Background(), "BVtest", 1, 60, 2, false)
	if e != nil {
		t.Fatal(e)
	}
	if complete {
		t.Fatal("seg->list fallback incorrectly grants full-coverage reconciliation permission")
	}
}
