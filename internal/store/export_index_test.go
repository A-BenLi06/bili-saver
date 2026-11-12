package store

import (
	"strings"
	"testing"
)

func TestExportAndFacesPlansUpgradeIdempotently(t *testing.T) {
	s := openTest(t)
	if _, err := s.DB.Exec(`DROP INDEX idx_danmaku_export; DROP INDEX idx_comments_faces`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`EXPLAIN QUERY PLAN SELECT dmid,timepoint,mode,fontsize,color,content,ctime FROM danmaku WHERE cid=1 ORDER BY timepoint,dmid`,
		`EXPLAIN QUERY PLAN SELECT member_mid,member_face FROM comments WHERE bvid='BV' AND member_mid>0 AND member_face<>'' GROUP BY member_mid`,
	} {
		rows, err := s.DB.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		rows.Close()
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("temporary sort: %s", plan)
		}
		if !strings.Contains(plan, "idx_danmaku_export") && !strings.Contains(plan, "COVERING INDEX idx_comments_faces") {
			t.Fatalf("index unused: %s", plan)
		}
	}
}

func TestQueuedJobsHandlesMalformedKeys(t *testing.T) {
	s := openTest(t)
	for _, payload := range []string{`not-json`, `{"bvid":42}`, `{"bvid":"BVvalid"}`} {
		if _, err := s.CreateJob("archive", payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.QueuedJobs()
	if err != nil || len(rows) != 3 {
		t.Fatalf("keys: %+v %v", rows, err)
	}
	if rows[0].Bvid != "" || rows[1].Bvid != "" || rows[2].Bvid != "BVvalid" {
		t.Fatal(rows)
	}
}
