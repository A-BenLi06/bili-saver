package archiveops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkRelocatePaths(b *testing.B) {
	const videos = 4_000
	for _, tc := range []struct {
		name   string
		legacy int
	}{
		{"Portable", 0},
		{"Mixed", videos / 10},
		{"Legacy", videos},
	} {
		b.Run(tc.name, func(b *testing.B) {
			root, stage := b.TempDir(), b.TempDir()
			dbName := "bili-saver.db"
			st, err := store.Open(filepath.Join(root, dbName))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			tx, err := st.DB.BeginTx(b.Context(), nil)
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			for i := range videos {
				id := fmt.Sprintf("BV%05d", i)
				media := "media/" + id + "/part.mp4"
				subtitle := "derived/" + id + "/zh.bcc"
				if i < tc.legacy {
					media = `D:\old\data\media\` + id + `\part.mp4`
					subtitle = `D:\old\data\derived\` + id + `\zh.bcc`
				}
				if _, err := tx.Exec(`INSERT INTO videos(bvid,first_archived_at,local_path) VALUES(?,1,?)`, id, media); err != nil {
					b.Fatal(err)
				}
				if _, err := tx.Exec(`INSERT INTO subtitles(bvid,cid,created_at,file_path) VALUES(?,1,1,?)`, id, subtitle); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if err := st.Close(); err != nil {
				b.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, dbName))
			if err != nil {
				b.Fatal(err)
			}
			destination := filepath.Join(b.TempDir(), "archive")
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if err := os.WriteFile(filepath.Join(stage, dbName), body, 0600); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := relocatePaths(b.Context(), stage, root, destination); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
