package images

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedImageDownloadAndCancelableWait(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		<-gate
		w.Write(fakeJPEG())
	}))
	defer srv.Close()
	l := New(nil, t.TempDir(), "test", nil)
	l.Pace = 0
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	var wg sync.WaitGroup
	var added atomic.Int32
	get := func() {
		defer wg.Done()
		ok, err := l.fetchMissing(t.Context(), FaceRel(42), srv.URL)
		if err != nil {
			t.Error(err)
		}
		if ok {
			added.Add(1)
		}
	}
	wg.Add(1)
	go get()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request stalled")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := l.fetchMissing(ctx, FaceRel(42), srv.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation: %v", err)
	}
	for i := 0; i < 31; i++ {
		wg.Add(1)
		go get()
	}
	release()
	wg.Wait()
	if requests.Load() != 1 || added.Load() != 1 {
		t.Fatalf("requests=%d added=%d", requests.Load(), added.Load())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.inflight) != 0 {
		t.Fatal("flight cache leaked")
	}
}

func TestImageOversizeFailsAndRetrySucceeds(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			io.Copy(w, io.LimitReader(&repeatReader{}, maxImageBytes+1))
			return
		}
		w.Write(fakeJPEG())
	}))
	defer srv.Close()
	l := New(nil, t.TempDir(), "test", nil)
	l.Pace = 0
	rel := FaceRel(42)
	if ok, err := l.fetchMissing(t.Context(), rel, srv.URL); ok || err == nil {
		t.Fatal("oversize committed")
	}
	path := filepath.Join(l.DataDir, filepath.FromSlash(rel))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("oversize file left on disk")
	}
	if ok, err := l.fetchMissing(t.Context(), rel, srv.URL); !ok || err != nil {
		t.Fatalf("failed flight cached: %v %v", ok, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, fakeJPEG()) {
		t.Fatal("retry corrupted")
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".output-*"))
	if len(left) != 0 {
		t.Fatal(left)
	}
}

type repeatReader struct{}

func (*repeatReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
