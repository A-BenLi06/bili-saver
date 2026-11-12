package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkStatsSweepProjection(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "test.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO videos(bvid,description,pubdate,last_stats_at,first_archived_at) VALUES(?,?,?,77,1)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("BV%05d", i), strings.Repeat("x", 4096), i); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rows, err := s.ListVideos(0, "", 0, 0)
			if err != nil || len(rows) != 10000 {
				b.Fatal(err)
			}
		}
	})
	b.Run("paged", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			total, before, err := s.StatsSweepWindow(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			n := 0
			for before > 0 {
				rows, err := s.StatsSweepPage(b.Context(), before, 400)
				if err != nil {
					b.Fatal(err)
				}
				if len(rows) == 0 {
					break
				}
				n += len(rows)
				before = rows[len(rows)-1].RowID - 1
			}
			if n != total {
				b.Fatal(n, total)
			}
		}
	})
}

func BenchmarkMediaTagsBatch(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "test.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO tags(id,name) VALUES(1,'原始A'),(2,'原始B'),(3,'派生')`); err != nil {
		b.Fatal(err)
	}
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = fmt.Sprintf("BV%04d", i)
		for tag := 1; tag <= 3; tag++ {
			origin := "bilibili"
			if tag == 3 {
				origin = "plugin:test"
			}
			if _, err := tx.Exec(`INSERT INTO video_tags(bvid,tag_id,origin,created_at) VALUES(?,?,?,1)`, ids[i], tag, origin); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, id := range ids {
				if _, err := s.VideoTags(id); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.ReportMetric(1000, "queries/op")
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rows, err := s.BilibiliTagsByBvids(ids)
			if err != nil || len(rows) != 1000 {
				b.Fatal(err)
			}
		}
		b.ReportMetric(3, "queries/op")
	})
}
