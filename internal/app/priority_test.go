package app

import (
	"fmt"
	"log/slog"
	"testing"

	"bili-saver/internal/store"
)

func TestSourceArchiveInheritsPriority(t *testing.T) {
	for _, priority := range []int{0, 1} {
		t.Run(fmt.Sprint(priority), func(t *testing.T) {
			a := newTestApp(t)
			a.Log = slog.Default()
			src := &store.Source{Type: "video", ExtID: "BV1Test00001", SyncMode: "once"}
			if _, err := a.St.UpsertSource(src); err != nil {
				t.Fatal(err)
			}
			j := &store.Job{PayloadJSON: fmt.Sprintf(`{"source_id":%d,"download":false}`, src.ID), Priority: priority}
			if err := a.jobSyncSource(t.Context(), j, func(float64, string) {}); err != nil {
				t.Fatal(err)
			}
			queued, err := a.St.ListJobs(10, []string{"queued"})
			if err != nil {
				t.Fatal(err)
			}
			if len(queued) != 1 || queued[0].Type != "archive" || queued[0].Priority != priority {
				t.Fatalf("首次源同步生成的归档必须继承优先级: %+v", queued)
			}
		})
	}
}
