package downloader

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
)

func TestCoverRetryAtomicAndRepeatedDownloadSkipsHTTP(t *testing.T) {
	var calls atomic.Int32
	body := bytes.Repeat([]byte("jpeg"), 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "128")
			w.Write([]byte("partial"))
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	api, err := biliapi.New(filepath.Join(t.TempDir(), "cookie.json"), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	d := New(nil, api, cfg, nil, nil)
	path := filepath.Join(cfg.DataDir, "media", "BVcover", "cover.jpg")
	if _, err := d.downloadCover(t.Context(), "BVcover", srv.URL); err == nil {
		t.Fatal("partial response succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial cover exposed")
	}
	if _, err := d.downloadCover(t.Context(), "BVcover", srv.URL); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, body) {
		t.Fatal("retry corrupted")
	}
	if _, err := d.downloadCover(t.Context(), "BVcover", srv.URL); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("repeated cover requested %d times", calls.Load())
	}
}
