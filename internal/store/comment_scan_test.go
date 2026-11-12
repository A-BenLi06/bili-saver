package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestCommentPageCheckpointRollsBackTogetherWithOrigin(t *testing.T) {
	s := openTest(t)
	scan := &CommentScan{Bvid: "BVAtomic", Aid: 1, Epoch: 100, Full: true}
	if err := s.StartCommentScan(scan, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_scan_write BEFORE UPDATE ON comment_scan_state BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	next := *scan
	next.Offset = "second"
	next.MainPages = 1
	c := &Comment{Bvid: scan.Bvid, Rpid: 1, Content: "origin survives only committed pages", FirstSeenAt: 100, LastSeenAt: 100}
	err := s.CommitCommentPage(&next, []*Comment{c}, []*CommentScanRoot{{Root: 1, NextPN: 1, Expected: 2}}, nil, 20, 0, 0)
	if err == nil {
		t.Fatal("expected rollback")
	}
	for _, table := range []string{"comments", "comment_scan_seen", "comment_scan_roots"} {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE bvid=?`, scan.Bvid).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	saved, err := s.LoadCommentScan(scan.Bvid)
	if err != nil || saved.Offset != "" || saved.Seen != 0 {
		t.Fatal(saved, err)
	}
	if _, err := s.DB.Exec(`DROP TRIGGER reject_scan_write`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCommentPage(&next, []*Comment{c}, nil, nil, 20, 0, 0); err != nil {
		t.Fatal(err)
	}
	if next.Seen != 1 {
		t.Fatal(next.Seen)
	}
}

func TestCommentReconciliationAcknowledgementIsAtomic(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BVAudit", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 100; i++ {
		seen := int64(1)
		if i <= 10 {
			seen = 2
		}
		if err := s.UpsertComment(&Comment{Bvid: "BVAudit", Rpid: i, FirstSeenAt: 1, LastSeenAt: seen}); err != nil {
			t.Fatal(err)
		}
	}
	scan := &CommentScan{Bvid: "BVAudit", Aid: 1, Epoch: 2, Full: true, MainDone: true, Seen: 10}
	if err := s.StartCommentScan(scan, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_scan_ack BEFORE DELETE ON comment_scan_state BEGIN SELECT RAISE(ABORT,'injected acknowledge failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteCommentScan(scan, 100); err == nil {
		t.Fatal("expected rollback")
	}
	var anomaly int
	if err := s.DB.QueryRow(`SELECT comments_anomaly FROM videos WHERE bvid='BVAudit'`).Scan(&anomaly); err != nil || anomaly != 0 {
		t.Fatal(anomaly, err)
	}
	if _, err := s.DB.Exec(`DROP TRIGGER reject_scan_ack`); err != nil {
		t.Fatal(err)
	}
	n, anomalous, err := s.CompleteCommentScan(scan, 100)
	if err != nil || n != 0 || !anomalous {
		t.Fatal(n, anomalous, err)
	}
	if _, _, err := s.CompleteCommentScan(scan, 100); err == nil {
		t.Fatal("same round must not confirm twice")
	}
	if err := s.DB.QueryRow(`SELECT comments_anomaly FROM videos WHERE bvid='BVAudit'`).Scan(&anomaly); err != nil || anomaly != 1 {
		t.Fatal(anomaly, err)
	}
	scan.Epoch = 3
	for i := int64(1); i <= 10; i++ {
		if err := s.UpsertComment(&Comment{Bvid: "BVAudit", Rpid: i, FirstSeenAt: 1, LastSeenAt: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StartCommentScan(scan, nil); err != nil {
		t.Fatal(err)
	}
	n, anomalous, err = s.CompleteCommentScan(scan, 101)
	if err != nil || n != 90 || anomalous {
		t.Fatal(n, anomalous, err)
	}
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE bvid='BVAudit'`).Scan(&total); err != nil || total != 100 {
		t.Fatal("origin rows physically removed", total, err)
	}
}

func TestCommentScanTablesUpgradeOldDatabaseIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComment(&Comment{Bvid: "BVOld", Rpid: 1, Content: "preserved", FirstSeenAt: 1, LastSeenAt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"comment_scan_seen", "comment_scan_roots", "comment_scan_state", "comment_sync_state"} {
		if _, err := s.DB.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`UPDATE schema_meta SET v='12' WHERE k='schema'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var content, version string
		if err := s.DB.QueryRow(`SELECT content FROM comments WHERE rpid=1`).Scan(&content); err != nil || content != "preserved" {
			t.Fatal(content, err)
		}
		if err := s.DB.QueryRow(`SELECT v FROM schema_meta WHERE k='schema'`).Scan(&version); err != nil || version != fmt.Sprint(SchemaVersion) {
			t.Fatal(version, err)
		}
		scan := &CommentScan{Bvid: "BVOld", Epoch: 2}
		if err := s.StartCommentScan(scan, nil); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}
