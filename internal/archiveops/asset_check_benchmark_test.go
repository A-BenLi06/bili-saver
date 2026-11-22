package archiveops

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkCheckAssets(b *testing.B) {
	const entries = 120
	for _, tc := range []struct {
		name  string
		files int
	}{{"Shared", 12}, {"Unique", entries}} {
		b.Run(tc.name, func(b *testing.B) {
			root := b.TempDir()
			st, err := store.Open(filepath.Join(root, "bili-saver.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			if err := os.Mkdir(filepath.Join(root, "media"), 0700); err != nil {
				b.Fatal(err)
			}
			payload := bytes.Repeat([]byte("synthetic media "), 16384)
			hash := fmt.Sprintf("%x", sha256.Sum256(payload))
			for i := range tc.files {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("media/%03d.bin", i)), payload, 0600); err != nil {
					b.Fatal(err)
				}
			}
			for i := range entries {
				if err := st.AddAsset(&store.Asset{Bvid: fmt.Sprintf("BV%03d", i), Role: "video", Origin: "bilibili", StorageKey: fmt.Sprintf("media/%03d.bin", i%tc.files), SHA256: hash, Size: int64(len(payload))}); err != nil {
					b.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				report, err := Check(b.Context(), root)
				if err != nil || !report.DatabaseOK || report.FilesChecked != entries || len(report.Issues) != 0 {
					b.Fatalf("unexpected check: %+v %v", report, err)
				}
			}
		})
	}
}
