package downloader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
)

// newRangeServer 提供确定性 payload 的 Range 文件服务，统计最大并发连接数。
// chunkDelay 放大每块写入间隔，用于制造并发窗口。
func newRangeServer(t *testing.T, size int64, chunkDelay time.Duration, supportRange bool) (*httptest.Server, *int64) {
	t.Helper()
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	var cur, max int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&cur, 1)
		for {
			m := atomic.LoadInt64(&max)
			if n <= m || atomic.CompareAndSwapInt64(&max, m, n) {
				break
			}
		}
		defer atomic.AddInt64(&cur, -1)

		start, end := int64(0), size-1
		status := http.StatusOK
		if rng := r.Header.Get("Range"); supportRange && rng != "" {
			if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			if start >= size {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= size {
				end = size - 1
			}
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(status)
		buf := make([]byte, 64<<10)
		pos := start
		for pos <= end {
			nw := int64(len(buf))
			if pos+nw > end+1 {
				nw = end + 1 - pos
			}
			if _, err := w.Write(payload[pos : pos+nw]); err != nil {
				return
			}
			pos += nw
			if chunkDelay > 0 {
				time.Sleep(chunkDelay)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &max
}

func newTestDownloader(t *testing.T, segments int) *Downloader {
	t.Helper()
	api, err := biliapi.New("", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Download.Segments = segments
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Downloader{API: api, Cfg: cfg, Log: log}
}

func checkContent(t *testing.T, path string, size int64) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != size {
		t.Fatalf("大小不符 %d != %d", len(got), size)
	}
	for i := range got {
		if got[i] != byte(i*7) {
			t.Fatalf("内容不符 @%d", i)
		}
	}
}

func TestSegmentedDownloadConcurrent(t *testing.T) {
	const size = 12 << 20
	srv, maxc := newRangeServer(t, size, time.Millisecond, true)
	d := newTestDownloader(t, 4)
	path := filepath.Join(t.TempDir(), "video.m4s.part")
	stream := &biliapi.DashStream{BaseURL: srv.URL}
	if err := d.downloadFile(context.Background(), stream, path, nil); err != nil {
		t.Fatal(err)
	}
	checkContent(t, path, size)
	if *maxc < 2 {
		t.Fatalf("期望分段并发拉取（最大并发=%d）", *maxc)
	}
	if _, err := os.Stat(path + segExt); !os.IsNotExist(err) {
		t.Fatal("下载完成后 .segs 断点文件应被清理")
	}
}

func TestSegmentedResume(t *testing.T) {
	const size = 12 << 20
	srv, _ := newRangeServer(t, size, 0, true)
	d := newTestDownloader(t, 4)
	path := filepath.Join(t.TempDir(), "video.m4s.part")

	// 预置段 0 已完成的断点与对应数据，验证续传补齐剩余部分
	sf := &segFile{Size: size, Segs: make([]segState, 4)}
	chunk := int64(size) / 4
	for i := range sf.Segs {
		sf.Segs[i].Start = int64(i) * chunk
		sf.Segs[i].End = int64(i+1) * chunk
	}
	sf.Segs[0].Done = chunk
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	pre := make([]byte, chunk)
	for i := range pre {
		pre[i] = byte(i * 7)
	}
	if _, err := f.Write(pre); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := saveSegs(path+segExt, sf); err != nil {
		t.Fatal(err)
	}

	stream := &biliapi.DashStream{BaseURL: srv.URL}
	if err := d.downloadFile(context.Background(), stream, path, nil); err != nil {
		t.Fatal(err)
	}
	checkContent(t, path, size)
	if _, err := os.Stat(path + segExt); !os.IsNotExist(err) {
		t.Fatal("续传完成后 .segs 应被清理")
	}
}

// 小文件不应分段；不支持 Range 的源应退回单流且内容正确。
func TestFallbackSingleStream(t *testing.T) {
	t.Run("small file", func(t *testing.T) {
		const size = 1 << 20
		srv, _ := newRangeServer(t, size, 0, true)
		d := newTestDownloader(t, 4)
		path := filepath.Join(t.TempDir(), "audio.m4s.part")
		if err := d.downloadFile(context.Background(), &biliapi.DashStream{BaseURL: srv.URL}, path, nil); err != nil {
			t.Fatal(err)
		}
		checkContent(t, path, size)
	})
	t.Run("no range support", func(t *testing.T) {
		const size = 12 << 20
		srv, _ := newRangeServer(t, size, 0, false)
		d := newTestDownloader(t, 4)
		path := filepath.Join(t.TempDir(), "video.m4s.part")
		if err := d.downloadFile(context.Background(), &biliapi.DashStream{BaseURL: srv.URL}, path, nil); err != nil {
			t.Fatal(err)
		}
		checkContent(t, path, size)
	})
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-0/21770732": 21770732,
		"bytes 0-0/0":        0,
		"bytes 0-0/*":        0,
		"garbage":            0,
		"":                   0,
	}
	for in, want := range cases {
		if got := parseContentRangeTotal(in); got != want {
			t.Errorf("parseContentRangeTotal(%q) = %d, want %d", in, got, want)
		}
	}
}

// 空闲 watchdog：服务端发完头部后挂死，传输应在 segIdleTimeout 内失败而非永久阻塞。
func TestIdleWatchdog(t *testing.T) {
	old := segIdleTimeout
	segIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { segIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(5 * time.Second) // 挂死不继续发
	}))
	t.Cleanup(srv.Close)

	d := newTestDownloader(t, 4)
	sf := &segFile{Size: 1000, Segs: []segState{{Start: 0, End: 1000}}}
	path := filepath.Join(t.TempDir(), "v.part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_ = f.Truncate(1000)

	start := time.Now()
	err = trySegment(context.Background(), d.API.DownloadClient(), d.API.UserAgent(), srv.URL, f, &sf.Segs[0], func(int64) {})
	if err == nil {
		t.Fatal("挂死连接应导致错误")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("watchdog 未及时取消，耗时 %v", el)
	}
}
