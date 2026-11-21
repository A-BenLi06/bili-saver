package archiveops

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkCheckSnapshots(b *testing.B) {
	const files = 256
	root := b.TempDir()
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		b.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "raw"), 0700); err != nil {
		st.Close()
		b.Fatal(err)
	}
	payload := bytes.Repeat([]byte("synthetic response"), 512)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	for i := range files {
		key := fmt.Sprintf("raw/%04d.gz", i)
		body := fmt.Appendf(payload[:len(payload):len(payload)], "-%d", i)
		compressed.Reset()
		z.Reset(&compressed)
		if _, err := z.Write(body); err != nil {
			st.Close()
			b.Fatal(err)
		}
		if err := z.Close(); err != nil {
			st.Close()
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, key), compressed.Bytes(), 0600); err != nil {
			st.Close()
			b.Fatal(err)
		}
		if err := st.AddSnapshot(&store.Snapshot{
			Endpoint: "/fixture", BodyPath: key, RespHash: fmt.Sprintf("%x", sha256.Sum256(body)),
			StatusCode: 200, FetchedAt: 1,
		}); err != nil {
			st.Close()
			b.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		report, err := Check(b.Context(), root)
		if err != nil || !report.DatabaseOK || report.FilesChecked != files || len(report.Issues) != 0 {
			b.Fatalf("unexpected check: %+v %v", report, err)
		}
	}
}
