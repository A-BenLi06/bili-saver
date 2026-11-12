package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestUserDownloadCrashRecoveryRetainsReservationDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recover.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("recover-user", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertVideo(&store.Video{Bvid: "BVRecover", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err = st.AddUserVideo(u.ID, "BVRecover"); err != nil {
		t.Fatal(err)
	}
	r, err := st.RequestUserDownload(u.ID, "BVRecover", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	yesterday := store.Now() - 86400
	job, _, _, err := st.QueueUserDownload(r.ID, yesterday, false)
	if err != nil || job == 0 {
		t.Fatal(job, err)
	}
	if _, err = st.BeginUserDownload(r.ID, job); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(`UPDATE jobs SET state='running' WHERE id=?`, job); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewManager(st, nil, 1)
	done := make(chan error, 1)
	m.RegisterVideo("user_download", func(ctx context.Context, j *store.Job, _ ProgressFn) error {
		active, err := st.BeginUserDownload(r.ID, j.ID)
		if err == nil && !active {
			err = fmt.Errorf("replayed job did not own request")
		}
		if err == nil {
			err = st.FinishUserDownload(r.ID, true, true, nil)
		}
		done <- err
		return err
	})
	m.Start(context.Background())
	defer m.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job was not recovered")
	}
	old, err := st.DownloadQuota(u.ID, yesterday)
	if err != nil || old.Used != 1 || old.Reserved != 0 {
		t.Fatal(old, err)
	}
	today, err := st.DownloadQuota(u.ID, store.Now())
	if err != nil || today.Used != 0 || today.Reserved != 0 {
		t.Fatal(today, err)
	}
}

func TestUserDownloadPumpDoesNotQueueExhaustedUsers(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pump.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tier := &store.UserTier{Name: "no-download", MaxDailyAdds: 10, MaxDailyDownloads: 0, MinIntervalMin: 60}
	tierID, err := st.UpsertTier(tier)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("waiting-user", "password", "user", tierID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		bvid := fmt.Sprintf("BVWait%d", i)
		if err = st.UpsertVideo(&store.Video{Bvid: bvid, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
		if err = st.AddUserVideo(u.ID, bvid); err != nil {
			t.Fatal(err)
		}
		if _, err = st.RequestUserDownload(u.ID, bvid, 1, false); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(st, nil, 1)
	for i := 0; i < 3; i++ {
		if err = m.PumpUserDownloads(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.ListJobs(20, nil)
	if err != nil || len(list) != 0 {
		t.Fatal("quota wait created worker jobs", list, err)
	}
	q, err := st.DownloadQuota(u.ID, store.Now())
	if err != nil || q.Waiting != 3 || q.Reserved != 0 {
		t.Fatal(q, err)
	}
}
