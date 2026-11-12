package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkCompletionProgressBurst(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "test.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	m := NewManager(st, nil, 1)
	m.Register("burst", func(ctx context.Context, j *store.Job, progress ProgressFn) error {
		for i := 0; i < 1000; i++ {
			progress(1, "完成")
		}
		return nil
	})
	id, err := st.CreateActiveJobPrio("burst", "{}", 0)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := st.DB.Exec(`CREATE TABLE writes(n INTEGER); INSERT INTO writes VALUES(0); CREATE TRIGGER progress_writes AFTER UPDATE ON jobs BEGIN UPDATE writes SET n=n+1; END`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.process(b.Context(), id)
	}
	b.StopTimer()
	var n int
	if err := st.DB.QueryRow(`SELECT n FROM writes`).Scan(&n); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(n)/float64(b.N), "updates/op")
}
