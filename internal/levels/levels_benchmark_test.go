package levels

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkCommentLevelsFromSnapshots(b *testing.B) {
	for _, tc := range []struct {
		name     string
		files    int
		comments int
		pending  int
	}{
		{"Small", 16, 16, 16 * 16},
		{"Backfill", 256, 40, 256 * 40},
		{"Complete", 256, 40, 0},
		{"Sparse", 256, 40, 1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			st, err := store.Open(filepath.Join(dir, "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { st.Close() })
			raw := filepath.Join(dir, "raw", "x_v2_reply_reply", "abcd")
			if err := os.MkdirAll(raw, 0o755); err != nil {
				b.Fatal(err)
			}
			tx, err := st.DB.Begin()
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			firstPending := tc.files*tc.comments - tc.pending + 1
			msg := strings.Repeat("这是合成评论正文。", 12)
			for page := range tc.files {
				var body strings.Builder
				body.WriteString(`{"data":{"replies":[`)
				for i := range tc.comments {
					id := page*tc.comments + i + 1
					level := 5
					if id >= firstPending {
						level = 0
					}
					if _, err := tx.Exec(`INSERT INTO comments(rpid,bvid,member_level,first_seen_at,last_seen_at,raw_json)
					VALUES(?, 'BVBENCH', ?, 1, 1, ?)`, id, level, msg); err != nil {
						b.Fatal(err)
					}
					if i > 0 {
						body.WriteByte(',')
					}
					fmt.Fprintf(&body, `{"rpid":%d,"member":{"level_info":{"current_level":5}},"content":{"message":%q}}`, id, msg)
				}
				body.WriteString(`]}}`)
				f, err := os.Create(filepath.Join(raw, fmt.Sprintf("%04d.json.gz", page)))
				if err != nil {
					b.Fatal(err)
				}
				w := gzip.NewWriter(f)
				_, writeErr := w.Write([]byte(body.String()))
				gzipErr, fileErr := w.Close(), f.Close()
				if writeErr != nil || gzipErr != nil || fileErr != nil {
					b.Fatalf("write snapshot: %v, %v, %v", writeErr, gzipErr, fileErr)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}

			backfill := New(st, dir, nil, nil)
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				got, err := backfill.CommentLevelsFromSnapshots(ctx, nil)
				if err != nil || got != tc.pending {
					b.Fatalf("updated %d comments, want %d: %v", got, tc.pending, err)
				}
				if tc.pending > 0 {
					b.StopTimer()
					if _, err := st.DB.Exec(`UPDATE comments SET member_level=0 WHERE rpid>=?`, firstPending); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
			}
		})
	}
}
