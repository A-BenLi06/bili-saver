package downloader

import (
	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditResumeServerIgnoresRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("abcdef")) }))
	defer srv.Close()
	api, e := biliapi.New("", 1, "test")
	if e != nil {
		t.Fatal(e)
	}
	d := New(nil, api, config.Defaults(), nil, nil)
	p := filepath.Join(t.TempDir(), "v.part")
	if e = os.WriteFile(p, []byte("abc"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e = d.tryDownload(context.Background(), srv.URL, p, 3, 1, nil)
	if e != nil {
		t.Fatalf("expected full restart when Range is ignored; got %v", e)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "abcdef" {
		t.Fatalf("incorrect recovered bytes: %q", got)
	}
}
func TestAuditNewCIDDoesNotReuseOldOrdinal(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte(`{"code":-1,"message":"mock"}`))
	}))
	defer srv.Close()
	api, _ := biliapi.New("", 1, "test")
	api.SetBase(srv.URL, srv.URL)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	d := New(nil, api, cfg, nil, nil)
	dir := filepath.Join(cfg.DataDir, "media", "BVtest", "p1")
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "BVtest-p1.mp4"), []byte("old CID media"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := d.downloadPage(context.Background(), "BVtest", &store.Video{Bvid: "BVtest", Aid: 1}, &store.Page{Bvid: "BVtest", Cid: 222, Page: 1}, 80, 16, func(float64) {})
	if e == nil && requests == 0 {
		t.Fatal("new CID accepted existing p1 bytes without establishing their CID")
	}
}
