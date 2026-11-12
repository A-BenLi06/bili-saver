package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAuditDanmakuDifferentCIDsAreNotTwoConfirmationRounds(t *testing.T) {
	st, e := Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = st.UpsertVideo(&Video{Bvid: "BVtest", Status: "ok", FirstArchivedAt: 1}); e != nil {
		t.Fatal(e)
	}
	var ds []*Danmaku
	for part := int64(1); part <= 2; part++ {
		for i := int64(1); i <= 150; i++ {
			ds = append(ds, &Danmaku{Dmid: part*1000 + i, Bvid: "BVtest", Cid: part, Content: "origin", FirstSeenAt: 1, LastSeenAt: 1})
		}
	}
	if e = st.UpsertDanmakuBatch(ds); e != nil {
		t.Fatal(e)
	}
	n, an, e := st.MarkMissingDanmakuGuarded("BVtest", 1, 2, false, 0)
	if e != nil || n != 0 || !an {
		t.Fatalf("first CID n=%d anomalous=%v err=%v", n, an, e)
	}
	n, an, e = st.MarkMissingDanmakuGuarded("BVtest", 2, 2, false, 0)
	if e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("same scan's second CID counted as second round: falsely marked %d deleted", n)
	}
}

func TestFutureSchemaRefusedWithoutRewritingVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_meta(k TEXT PRIMARY KEY,v TEXT);INSERT INTO schema_meta VALUES('schema',?)`, strconv.Itoa(SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("future schema was accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err = db.QueryRow(`SELECT v FROM schema_meta WHERE k='schema'`).Scan(&v); err != nil || v != SchemaVersion+1 {
		t.Fatalf("future version changed: %d %v", v, err)
	}
}
func TestDanmakuRoundReplayIsIdempotent(t *testing.T) {
	st := openTest(t)
	if err := st.UpsertVideo(&Video{Bvid: "BVround", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var ds []*Danmaku
	for i := int64(1); i <= 150; i++ {
		ds = append(ds, &Danmaku{Dmid: i, Bvid: "BVround", Cid: 1, FirstSeenAt: 1, LastSeenAt: 1})
	}
	if err := st.UpsertDanmakuBatch(ds); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		n, a, err := st.ReconcileDanmaku("BVround", 1, 2, false, 0)
		if err != nil || n != 0 || !a {
			t.Fatalf("replayed epoch: %d %v %v", n, a, err)
		}
	}
	n, a, err := st.ReconcileDanmaku("BVround", 1, 3, false, 0)
	if err != nil || n != 150 || a {
		t.Fatalf("independent full round: %d %v %v", n, a, err)
	}
}
