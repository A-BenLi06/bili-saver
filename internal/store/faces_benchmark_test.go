package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkCommentFaces(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "test.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO comments(rpid,bvid,member_mid,member_face,raw_json,first_seen_at,last_seen_at) VALUES(?,?,?,?,?,1,1)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		if _, err := stmt.Exec(i+1, "BVFaces", i%1000+1, "https://cdn.example/avatar.jpg", strings.Repeat("x", 4096)); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"legacy", "covering"} {
		b.Run(mode, func(b *testing.B) {
			ddl := `DROP INDEX IF EXISTS idx_comments_faces`
			if mode == "covering" {
				ddl = `CREATE INDEX IF NOT EXISTS idx_comments_faces ON comments(bvid,member_mid,member_face) WHERE member_mid>0 AND member_face<>''`
			}
			if _, err := s.DB.Exec(ddl); err != nil {
				b.Fatal(err)
			}
			if _, err := s.ListCommentFaces("BVFaces"); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := s.ListCommentFaces("BVFaces")
				if err != nil || len(rows) != 1000 {
					b.Fatalf("faces %d: %v", len(rows), err)
				}
			}
			b.StopTimer()
			var bytes int64
			if err := s.DB.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name='idx_comments_faces'`).Scan(&bytes); err == nil {
				b.ReportMetric(float64(bytes), "index-bytes")
			}
		})
	}
}
