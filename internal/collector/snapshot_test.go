package collector

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func snapshotCollector(t testing.TB) *Collector {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, cfg, nil, nil)
}

func BenchmarkSnapshotRepeated(b *testing.B) {
	c := snapshotCollector(b)
	hook := c.SnapshotHook()
	body := []byte(strings.Repeat(`{"content":"原始评论快照","rpid":1234567890}`, 4096))
	params := url.Values{"oid": {"123"}}
	hook("/x/v2/reply", params, 200, body)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hook("/x/v2/reply", params, 200, body)
	}
}

func TestSnapshotDeduplicatesBodyPreservesObservations(t *testing.T) {
	c := snapshotCollector(t)
	hook := c.SnapshotHook()
	body := []byte(`{"code":0,"data":{"text":"原始响应"}}`)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hook("/x/v2/reply", url.Values{"oid": {"1"}}, 200, body) }()
	}
	wg.Wait()
	rows, err := c.St.DB.Query(`SELECT body_path, status_code, fetched_at FROM snapshots`)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	n := 0
	for rows.Next() {
		var path string
		var status int
		var ts int64
		if err := rows.Scan(&path, &status, &ts); err != nil {
			t.Fatal(err)
		}
		if status != 200 || ts <= 0 {
			t.Fatalf("invalid observation: %d %d", status, ts)
		}
		paths[path] = true
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if n != 16 || len(paths) != 1 {
		t.Fatalf("observations=%d body files=%d", n, len(paths))
	}
	for path := range paths {
		f, err := os.Open(filepath.Join(c.Cfg.DataDir, path))
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(gz)
		gz.Close()
		f.Close()
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("roundtrip: %q %v", got, err)
		}
	}
	hook("/x/v2/reply", url.Values{"oid": {"2"}}, 403, body)
	hook("/x/v2/reply", nil, 200, []byte(`{"code":1}`))
	var observations, bodies int
	if err := c.St.DB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT body_path) FROM snapshots`).Scan(&observations, &bodies); err != nil {
		t.Fatal(err)
	}
	if observations != 18 || bodies != 2 {
		t.Fatalf("observations=%d bodies=%d", observations, bodies)
	}
}

func TestSnapshotWriteFailureDoesNotIndex(t *testing.T) {
	c := snapshotCollector(t)
	if err := os.WriteFile(filepath.Join(c.Cfg.DataDir, "raw"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	c.SnapshotHook()("/x/v2/reply", nil, 200, []byte(`{}`))
	var n int
	if err := c.St.DB.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed body must not be indexed")
	}
}
