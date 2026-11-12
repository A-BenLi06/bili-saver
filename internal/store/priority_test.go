package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestJobPriorityMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}',
		state TEXT NOT NULL DEFAULT 'queued', progress REAL NOT NULL DEFAULT 0,
		detail TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL DEFAULT 0
	); INSERT INTO jobs(type,created_at) VALUES('archive',1)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		j, err := st.GetJob(1)
		if err != nil {
			st.Close()
			t.Fatal(err)
		}
		if j.Priority != 0 || j.State != "queued" {
			st.Close()
			t.Fatalf("迁移不应改变旧任务: %+v", j)
		}
		if i == 0 {
			if _, err := st.CreateJobPrio("archive", `{"bvid":"new"}`, 1); err != nil {
				st.Close()
				t.Fatal(err)
			}
		}
		ids, err := st.QueuedJobIDs(10)
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 || ids[0] != 2 || ids[1] != 1 {
			t.Fatalf("迁移后应保留优先顺序: %v", ids)
		}
	}
}
