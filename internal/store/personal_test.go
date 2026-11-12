package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func personalUser(t *testing.T, s *Store, name string, cap int) *User {
	t.Helper()
	tier := &UserTier{Name: name, MaxDailyAdds: 50, MaxDailyDownloads: cap, MinIntervalMin: 60}
	id, err := s.UpsertTier(tier)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(name, "password", "user", id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func personalRequest(t *testing.T, s *Store, uid int64, bvid string) *DownloadRequest {
	t.Helper()
	if err := s.UpsertVideo(&Video{Bvid: bvid, Title: bvid, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserVideo(uid, bvid); err != nil {
		t.Fatal(err)
	}
	r, err := s.RequestUserDownload(uid, bvid, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPersonalSourceIsolationAndRetention(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "alice", 2)
	v := personalUser(t, s, "bob", 2)
	a := &Source{Type: "space", ExtID: "12345", UserID: u.ID, Watch: true, CreatedAt: Now()}
	b := &Source{Type: "space", ExtID: "12345", UserID: v.ID, Watch: false, CreatedAt: Now()}
	first, err := s.UpsertSource(a)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertSource(b)
	if err != nil || first == other {
		t.Fatal(first, other, err)
	}
	a.Watch = false
	again, err := s.UpsertSource(a)
	if err != nil || again != first {
		t.Fatal(again, err)
	}
	old, err := s.GetSource(other)
	if err != nil || old.UserID != v.ID || old.Watch {
		t.Fatal(old, err)
	}
	personalRequest(t, s, u.ID, "BVShared")
	personalRequest(t, s, v.ID, "BVShared")
	if err = s.DeleteSource(first); err != nil {
		t.Fatal(err)
	}
	if yes, err := s.UserHasVideo(u.ID, "BVShared"); err != nil || !yes {
		t.Fatal(yes, err)
	}
	items, total, err := s.ListVideosPageForUser(&u.ID, "", "", "", "", "", 20, 0)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatal(items, total, err)
	}
	counts, err := s.VideoStatusCountsForUser(&u.ID)
	if err != nil || counts["ok"] != 1 {
		t.Fatal(counts, err)
	}
}

func TestPersonalMigrationPreservesIDsAndBackfillsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := personalUser(t, s, "old-user", 1)
	src := &Source{ID: 0, Type: "video", ExtID: "BVOld", UserID: u.ID, CreatedAt: Now()}
	id, err := s.UpsertSource(src)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertVideo(&Video{Bvid: "BVOld", SourceID: id, Title: "origin"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateJob("sync_source", fmt.Sprintf(`{"source_id":%d}`, id))
	if err != nil {
		t.Fatal(err)
	}
	var ddl string
	if err = s.DB.QueryRow(`SELECT sql FROM sqlite_master WHERE name='sources'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(ddl[strings.Index(ddl, "("):], "UNIQUE(user_id, type, ext_id)", "UNIQUE(type, ext_id)")
	for _, q := range []string{`CREATE TABLE sources_old ` + body, `INSERT INTO sources_old SELECT * FROM sources`, `DROP TABLE sources`, `ALTER TABLE sources_old RENAME TO sources`, `DELETE FROM schema_meta WHERE k='personal_v14'`, `DELETE FROM user_videos`, `DELETE FROM source_videos`, `UPDATE sqlite_sequence SET seq=99 WHERE name='sources'`} {
		if _, err = s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSource(id)
		if err != nil || got.UserID != u.ID {
			t.Fatal(got, err)
		}
		j, err := s.GetJob(job)
		if err != nil || !strings.Contains(j.PayloadJSON, fmt.Sprint(id)) {
			t.Fatal(j, err)
		}
		if has, err := s.UserHasVideo(u.ID, "BVOld"); err != nil || !has {
			t.Fatal(has, err)
		}
		tier, err := s.GetTier(u.TierID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if tier.MaxDailyDownloads != tier.MaxDailyAdds {
				t.Fatal(tier)
			}
			tier.MaxDailyDownloads = 3
			if _, err = s.UpsertTier(tier); err != nil {
				t.Fatal(err)
			}
		} else if tier.MaxDailyDownloads != 3 {
			t.Fatal("migration overwrote quota", tier)
		}
		if i == 1 {
			newID, err := s.UpsertSource(&Source{Type: "space", ExtID: "new-after-migration", UserID: u.ID})
			if err != nil || newID <= 99 {
				t.Fatal("source sequence lost", newID, err)
			}
		}
		s.Close()
	}
}

func TestDownloadQuotaConcurrentReservationsAndNextDay(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "quota-user", 2)
	now := time.Date(2026, 10, 22, 23, 59, 0, 0, time.Local).Unix()
	var reqs []*DownloadRequest
	for i := 0; i < 12; i++ {
		reqs = append(reqs, personalRequest(t, s, u.ID, fmt.Sprintf("BVQuota%d", i)))
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(reqs))
	for _, r := range reqs {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); _, _, _, err := s.QueueUserDownload(id, now, false); errs <- err }(r.ID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	q, err := s.DownloadQuota(u.ID, now)
	if err != nil || q.Reserved != 2 {
		t.Fatal(q, err)
	}
	var chargedID int64
	if err = s.DB.QueryRow(`SELECT id FROM user_download_requests WHERE state='queued' ORDER BY id LIMIT 1`).Scan(&chargedID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishUserDownload(chargedID, true, true, nil); err != nil {
		t.Fatal(err)
	}
	q, err = s.DownloadQuota(u.ID, now)
	if err != nil || q.Used != 1 || q.Reserved != 1 {
		t.Fatal(q, err)
	}
	var other int64
	if err = s.DB.QueryRow(`SELECT id FROM user_download_requests WHERE state='queued' LIMIT 1`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishUserDownload(other, false, false, errors.New("network")); err != nil {
		t.Fatal(err)
	}
	q, err = s.DownloadQuota(u.ID, now)
	if err != nil || q.Used != 1 || q.Reserved != 0 {
		t.Fatal(q, err)
	}
	var waiting int64
	if err = s.DB.QueryRow(`SELECT id FROM user_download_requests WHERE state='waiting' AND error='' LIMIT 1`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.QueueUserDownload(waiting, now+120, false); err != nil {
		t.Fatal(err)
	}
	q, err = s.DownloadQuota(u.ID, now+120)
	if err != nil || q.Reserved != 1 || q.Used != 0 {
		t.Fatal(q, err)
	}
}

func TestSharedFilesNoDoubleChargeAndDisabledUsers(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "shared-a", 1)
	v := personalUser(t, s, "shared-b", 0)
	a := personalRequest(t, s, u.ID, "BVShare")
	b := personalRequest(t, s, v.ID, "BVShare")
	job, _, _, err := s.QueueUserDownload(a.ID, Now(), false)
	if err != nil || job == 0 {
		t.Fatal(job, err)
	}
	if err = s.FinishUserDownload(a.ID, true, true, nil); err != nil {
		t.Fatal(err)
	}
	job, _, _, err = s.QueueUserDownload(b.ID, Now(), true)
	if err != nil || job != 0 {
		t.Fatal(job, err)
	}
	q, err := s.DownloadQuota(v.ID, Now())
	if err != nil || q.Reserved+q.Used != 0 {
		t.Fatal(q, err)
	}
	r, err := s.UserDownload(v.ID, "BVShare")
	if err != nil || r.State != "done" {
		t.Fatal(r, err)
	}
	disabled := false
	if err = s.UpdateUser(u.ID, nil, nil, &disabled); err != nil {
		t.Fatal(err)
	}
	c := personalRequest(t, s, u.ID, "BVDisabled")
	job, _, _, err = s.QueueUserDownload(c.ID, Now(), false)
	if err != nil || job != 0 {
		t.Fatal(job, err)
	}
}

func TestFilesPresentRequiresAllParts(t *testing.T) {
	s := openTest(t)
	root := t.TempDir()
	personalRequest(t, s, 0, "BVParts")
	for i := 1; i <= 2; i++ {
		if err := s.UpsertPage(&Page{Bvid: "BVParts", Cid: int64(i), Page: i}); err != nil {
			t.Fatal(err)
		}
	}
	write := func(i int) {
		p := filepath.Join(root, "media", "BVParts", fmt.Sprintf("p%d", i), fmt.Sprintf("BVParts-p%d.mp4", i))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(1)
	if s.VideoFilesPresent(root, "BVParts") {
		t.Fatal("P2 missing")
	}
	write(2)
	if !s.VideoFilesPresent(root, "BVParts") {
		t.Fatal("both parts present")
	}
}

func TestDeleteUserCancelsRequestsWithDuplicateSources(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "removed", 2)
	for _, uid := range []int64{0, u.ID} {
		if _, err := s.UpsertSource(&Source{UserID: uid, Type: "space", ExtID: "12345"}); err != nil {
			t.Fatal(err)
		}
	}
	r := personalRequest(t, s, u.ID, "BVRemoved")
	if _, _, _, err := s.QueueUserDownload(r.ID, Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	q, err := s.DownloadQuota(u.ID, Now())
	if err != nil || q.Reserved != 0 {
		t.Fatal(q, err)
	}
	got, err := s.UserDownload(u.ID, "BVRemoved")
	if err != nil || got.State != "cancelled" {
		t.Fatal(got, err)
	}
	if _, err = s.GetVideo("BVRemoved"); err != nil {
		t.Fatal("raw video lost", err)
	}
}
