package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashPasswordCompatibility(t *testing.T) {
	for _, input := range [][2]string{{"bili-saver", "salt"}, {"", ""}, {"密码🔑", "random-salt"}} {
		body := []byte(input[1] + input[0])
		for i := 0; i < pwIterations; i++ {
			h := sha256.Sum256(body)
			body = h[:]
		}
		if got, want := HashPassword(input[0], input[1]), hex.EncodeToString(body); got != want {
			t.Fatalf("changed stored password hash: %s != %s", got, want)
		}
	}
}

func BenchmarkHashPassword(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = HashPassword("bili-saver", "bench-salt")
	}
}

func BenchmarkVideoCounts(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("BVbench%05d", i)
		if err := s.UpsertVideo(&Video{Bvid: id, Status: "ok", FirstArchivedAt: 1}); err != nil {
			b.Fatal(err)
		}
		ds := make([]*Danmaku, 1000)
		for j := range ds {
			ds[j] = &Danmaku{Dmid: int64(i*1000 + j + 1), Bvid: id, Cid: int64(i + 1), Content: "弹幕", FirstSeenAt: 1, LastSeenAt: 1}
		}
		if err := s.UpsertDanmakuBatch(ds); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListVideosWithCounts(0, "ok", 20, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDanmakuCountsIndexUpgrade(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BVcount", Status: "ok", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDanmakuBatch([]*Danmaku{{Dmid: 1, Bvid: "BVcount", Cid: 1, FirstSeenAt: 1, LastSeenAt: 1}}); err != nil {
		t.Fatal(err)
	}
	// 模拟存量库缺索引，重跑迁移自动补建且不触碰数据。
	if _, err := s.DB.Exec(`DROP INDEX idx_danmaku_bvid_status`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	vc, err := s.GetVideoWithCounts("BVcount")
	if err != nil || vc.DanmakuCount != 1 {
		t.Fatalf("counts: %+v %v", vc, err)
	}
	rows, err := s.DB.Query(`EXPLAIN QUERY PLAN SELECT COUNT(*) FROM danmaku WHERE bvid=? AND status='visible'`, "BVcount")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details strings.Builder
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		details.WriteString(detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(details.String(), "COVERING INDEX idx_danmaku_bvid_status") {
		t.Fatalf("not using covering index: %s", details.String())
	}
}
