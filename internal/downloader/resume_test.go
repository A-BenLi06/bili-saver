package downloader

import (
	"bili-saver/internal/biliapi"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestResumeRejectsChangedEqualSizeRemoteBytes(t *testing.T) {
	var revision atomic.Int32
	revision.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := revision.Load()
		body := "aaaaaa"
		if v == 2 {
			body = "bbbbbb"
		}
		w.Header().Set("ETag", fmt.Sprintf(`"revision-%d"`, v))
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/6")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(206)
			w.Write([]byte(body[:1]))
			return
		}
		if r.Header.Get("Range") != "" {
			t.Error("stale partial was resumed")
		}
		w.Header().Set("Content-Length", "6")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	d := newTestDownloader(t, 1)
	p := filepath.Join(t.TempDir(), "stream.part")
	stream := &biliapi.DashStream{ID: 80, BaseURL: srv.URL}
	if _, _, _, err := d.prepareResume(context.Background(), stream, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("aaa"), 0600); err != nil {
		t.Fatal(err)
	}
	revision.Store(2)
	if err := d.downloadFile(context.Background(), stream, p, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if string(body) != "bbbbbb" {
		t.Fatalf("mixed stream bytes %q", body)
	}
}
func TestUnvalidatedPartialIsRestarted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "6")
		w.Write([]byte("abcdef"))
	}))
	defer srv.Close()
	d := newTestDownloader(t, 4)
	p := filepath.Join(t.TempDir(), "stream.part")
	_ = os.WriteFile(p, []byte("stale"), 0600)
	if err := d.downloadFile(context.Background(), &biliapi.DashStream{BaseURL: srv.URL}, p, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if string(body) != "abcdef" {
		t.Fatal("unvalidated bytes reused")
	}
}
