package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSyncBatchMatchesSingleQueries(t *testing.T) {
	s := openTest(t)
	ids := make([]string, 405)
	for i := range ids {
		ids[i] = fmt.Sprintf("BVbatch%d", i)
		if err := s.UpsertVideo(&Video{Bvid: ids[i], FirstArchivedAt: 1, Pubdate: 50}); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < i%4; j++ {
			if err := s.AppendStats(&StatsSnapshot{Bvid: ids[i], Ts: int64(j + 1), View: j * 10}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids = append(ids, ids[0], ids[1], "BVabsent")
	vs, err := s.VideosByBvids(ids)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentStatsBatch(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 405 || vs["BVabsent"] != nil {
		t.Fatalf("video set: %d", len(vs))
	}
	for _, id := range ids {
		want, err := s.RecentStats(id, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(recent[id], want) {
			t.Fatalf("recent %s: %+v != %+v", id, recent[id], want)
		}
	}
}

func TestStatsMetadataPreservesArchiveAndMonotonicClock(t *testing.T) {
	s := openTest(t)
	v := &Video{Bvid: "BVstats", Title: "original", Description: "origin description", Status: "gone", FirstArchivedAt: 1, LastSyncAt: 200, LastStatsAt: 200, LastCommentsAt: 190, LastDanmakuAt: 180, LastSubsAt: 170, Tid: 5, Tname: "old", Orientation: "horizontal"}
	if err := s.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatsMetadata(v.Bvid, 100, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVideo(v.Bvid)
	if err != nil || got.Status != "ok" || got.LastSyncAt != 200 || got.LastStatsAt != 200 || got.LastCommentsAt != 190 || got.LastDanmakuAt != 180 || got.LastSubsAt != 170 || got.Title != v.Title || got.Description != v.Description || got.Tid != 5 || got.Tname != "old" || got.Orientation != "horizontal" {
		t.Fatalf("archive changed: %+v %v", got, err)
	}
	if err := s.UpdateStatsMetadata(v.Bvid, 300, 7, "new", "vertical"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetVideo(v.Bvid)
	if err != nil || got.LastSyncAt != 300 || got.LastStatsAt != 300 || got.Tid != 7 || got.Tname != "new" || got.Orientation != "vertical" {
		t.Fatalf("stats update: %+v %v", got, err)
	}
	if err := s.UpdateStatsMetadata("BVabsent", 300, 0, "", ""); err != ErrNotFound {
		t.Fatalf("missing video: %v", err)
	}
}

func TestLegacyCommentFTSUpgradeAndScopedRepair(t *testing.T) {
	s := openTest(t)
	for i, id := range []string{"BVa", "BVb"} {
		if err := s.UpsertComment(&Comment{Rpid: int64(100 + i), Bvid: id, Content: "历史搜索内容", FirstSeenAt: 1, LastSeenAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.MarkMissingComments("BVb", 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`DELETE FROM schema_meta WHERE k='comments_fts_rowid'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO comments_fts(rowid,rpid,content) VALUES(1,100,'旧索引'),(2,100,'重复旧索引')`); err != nil {
		t.Fatal(err)
	}
	// 标记提交失败必须把 FTS 重建一起回滚，不能留“半迁移”索引。
	if _, err := s.DB.Exec(`CREATE TRIGGER fail_fts_marker BEFORE INSERT ON schema_meta WHEN NEW.k='comments_fts_rowid' BEGIN SELECT RAISE(ABORT,'test marker failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateCommentFTS(); err == nil {
		t.Fatal("expected migration failure")
	}
	var legacy int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments_fts WHERE rowid=1 AND content='旧索引'`).Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("failed migration did not rollback: %d %v", legacy, err)
	}
	if _, err := s.DB.Exec(`DROP TRIGGER fail_fts_marker`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	var aligned int
	if err := s.DB.QueryRow(`SELECT count(*) FROM comments_fts WHERE rowid=rpid`).Scan(&aligned); err != nil || aligned != 2 {
		t.Fatalf("migration: %d %v", aligned, err)
	}
	var status, content string
	if err := s.DB.QueryRow(`SELECT status,content FROM comments WHERE rpid=101`).Scan(&status, &content); err != nil || status != "deleted" || content != "历史搜索内容" {
		t.Fatalf("origin changed: %s %s %v", status, content, err)
	}
	for i, id := range []string{"BVa", "BVb"} {
		if err := s.UpsertComment(&Comment{Rpid: int64(200 + i), Bvid: id, Content: "新增自愈", FirstSeenAt: 2, LastSeenAt: 2}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int64{1, 0} {
		n, err := s.HealVideoCommentsFTS("BVa")
		if err != nil || n != want {
			t.Fatalf("scoped repair: %d %v", n, err)
		}
	}
	got, err := s.SearchCommentsFTS("新增自愈", 10)
	if err != nil || len(got) != 1 || got[0] != 200 {
		t.Fatalf("search: %v %v", got, err)
	}
	if n, err := s.HealCommentsFTS(); err != nil || n != 1 {
		t.Fatalf("global repair: %d %v", n, err)
	}
	if err := s.ReindexCommentFTS(200, "重新索引"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM comments_fts WHERE rpid=200`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate reindex: %d %v", n, err)
	}
}

func TestReconcileIndexesAndHealthyReset(t *testing.T) {
	s := openTest(t)
	// 存量索引自动替换，重复迁移不增加索引/改动任何原始行。
	if _, err := s.DB.Exec(`CREATE INDEX idx_comments_seen ON comments(bvid,last_seen_at); CREATE INDEX idx_danmaku_seen ON danmaku(cid,last_seen_at)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	var old int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN('idx_comments_seen','idx_danmaku_seen')`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("old indexes: %d %v", old, err)
	}
	if err := s.UpsertVideo(&Video{Bvid: "BVhealthy", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE videos SET comments_anomaly=1,danmaku_anomaly=1 WHERE bvid='BVhealthy'`); err != nil {
		t.Fatal(err)
	}
	if n, a, err := s.MarkMissingCommentsGuarded("BVhealthy", 2, false, 0); err != nil || n != 0 || a {
		t.Fatalf("comments: %d %v %v", n, a, err)
	}
	if n, a, err := s.MarkMissingDanmakuGuarded("BVhealthy", 1, 2, false, 0); err != nil || n != 0 || a {
		t.Fatalf("danmaku: %d %v %v", n, a, err)
	}
	var c, d int
	if err := s.DB.QueryRow(`SELECT comments_anomaly,danmaku_anomaly FROM videos WHERE bvid='BVhealthy'`).Scan(&c, &d); err != nil || c != 0 || d != 0 {
		t.Fatalf("reset: %d %d %v", c, d, err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM comments WHERE bvid='BVhealthy' AND status='visible' AND last_seen_at<2`,
		`SELECT COUNT(*) FROM danmaku WHERE cid=1 AND status='visible' AND last_seen_at<2`,
	} {
		rows, err := s.DB.Query("EXPLAIN QUERY PLAN " + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(d)
		}
		rows.Close()
		if !strings.Contains(plan.String(), "reconcile") {
			t.Fatalf("wrong index: %s", plan.String())
		}
	}
}

// 相同合成数据比较原查询与当前查询，不依赖生产库或网络。
func BenchmarkSyncPaths(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("BVperf%d", i)
		if err := s.UpsertVideo(&Video{Bvid: ids[i], FirstArchivedAt: 1}); err != nil {
			b.Fatal(err)
		}
		cs := make([]*Comment, 1000)
		for j := range cs {
			cs[j] = &Comment{Rpid: int64(i*1000 + j + 1), Bvid: ids[i], Content: "comment benchmark", FirstSeenAt: 1, LastSeenAt: 2}
		}
		if err := s.UpsertCommentsBatch(cs); err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 10; j++ {
			if err := s.AppendStats(&StatsSnapshot{Bvid: ids[i], Ts: int64(j + 1), View: j}); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := s.CreateJob("archive", fmt.Sprintf(`{"bvid":"%s"}`, ids[i])); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := s.HealCommentsFTS(); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"legacy-global-fts", "scoped-fts", "single-source-read", "batch-source-read", "reconcile-old-index", "reconcile-range-index", "queue-old-index", "queue-active-index"} {
		b.Run(mode, func(b *testing.B) {
			// 基线使用原来的完整索引集合和相同 SQL，由 SQLite 自己选择执行计划。
			if mode == "reconcile-old-index" {
				if _, err := s.DB.Exec(`DROP INDEX idx_comments_reconcile`); err != nil {
					b.Fatal(err)
				}
				if _, err := s.DB.Exec(`CREATE INDEX idx_comments_seen ON comments(bvid,last_seen_at)`); err != nil {
					b.Fatal(err)
				}
				defer s.DB.Exec(`DROP INDEX idx_comments_seen`)
				defer s.DB.Exec(`CREATE INDEX idx_comments_reconcile ON comments(bvid,last_seen_at) WHERE status='visible'`)
			}
			if mode == "queue-old-index" {
				if _, err := s.DB.Exec(`DROP INDEX idx_jobs_active_payload`); err != nil {
					b.Fatal(err)
				}
				defer s.DB.Exec(`CREATE INDEX idx_jobs_active_payload ON jobs(type,payload_json) WHERE state IN ('queued','running')`)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				switch mode {
				case "legacy-global-fts":
					_, err = s.DB.Exec(`INSERT INTO comments_fts(rpid,content) SELECT rpid,content FROM comments WHERE rpid NOT IN(SELECT rpid FROM comments_fts)`)
				case "scoped-fts":
					_, err = s.HealVideoCommentsFTS(ids[0])
				case "single-source-read":
					for _, id := range ids {
						if _, err = s.GetVideo(id); err != nil {
							b.Fatal(err)
						}
						if _, err = s.RecentStats(id, 2); err != nil {
							b.Fatal(err)
						}
					}
				case "batch-source-read":
					if _, err = s.VideosByBvids(ids); err != nil {
						b.Fatal(err)
					}
					_, err = s.RecentStatsBatch(ids)
				case "reconcile-old-index", "reconcile-range-index":
					var n int
					err = s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE bvid=? AND status='visible' AND last_seen_at<2`, ids[0]).Scan(&n)
				case "queue-old-index", "queue-active-index":
					var n int
					err = s.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='archive' AND payload_json=? AND state IN('queued','running')`, fmt.Sprintf(`{"bvid":"%s"}`, ids[99])).Scan(&n)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
		})
	}
}
