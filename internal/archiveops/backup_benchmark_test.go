package archiveops

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkBackupIO(b *testing.B) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{{"Media", 4, 8 << 20}, {"ManyFiles", 96, 32 << 10}} {
		b.Run(tc.name, func(b *testing.B) {
			root, parent := b.TempDir(), b.TempDir()
			st, err := store.Open(filepath.Join(root, "bili-saver.db"))
			if err != nil {
				b.Fatal(err)
			}
			if err := st.Close(); err != nil {
				b.Fatal(err)
			}
			media := filepath.Join(root, "media")
			if err := os.Mkdir(media, 0700); err != nil {
				b.Fatal(err)
			}
			payload := bytes.Repeat([]byte{47}, tc.size)
			for i := range tc.count {
				if err := os.WriteFile(filepath.Join(media, fmt.Sprintf("%d.bin", i)), payload, 0600); err != nil {
					b.Fatal(err)
				}
			}
			dest := filepath.Join(parent, "backup")
			b.ReportAllocs()
			b.SetBytes(int64(tc.count) * int64(tc.size))
			for b.Loop() {
				m, err := Backup(b.Context(), root, dest)
				if err != nil || len(m.Files) != tc.count+1 {
					b.Fatalf("backup: %v", err)
				}
				if err := os.RemoveAll(dest); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
