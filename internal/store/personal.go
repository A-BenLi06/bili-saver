package store

import (
	"bili-saver/internal/archivepath"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) AddUserVideo(userID int64, bvid string) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO user_videos(user_id,bvid,created_at) VALUES(?,?,?)`, userID, bvid, Now())
	return err
}

func (s *Store) UserHasVideo(userID int64, bvid string) (bool, error) {
	var yes bool
	err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_videos WHERE user_id=? AND bvid=?)`, userID, bvid).Scan(&yes)
	return yes, err
}

func (s *Store) ListUserVideoSources(userID int64, bvid string) ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT `+sourceCols+` FROM sources WHERE user_id=? AND id IN (SELECT source_id FROM source_videos WHERE bvid=?) ORDER BY id`, userID, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

type DownloadRequest struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"-"`
	Bvid          string `json:"bvid"`
	State         string `json:"state"`
	JobID         int64  `json:"job_id,omitempty"`
	Error         string `json:"error,omitempty"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
}

type DownloadQuota struct {
	Used     int `json:"used"`
	Reserved int `json:"reserved"`
	Waiting  int `json:"waiting"`
}

func DownloadDay(now int64) string { return time.Unix(now, 0).In(time.Local).Format("2006-01-02") }

func (s *Store) DownloadQuota(userID, now int64) (DownloadQuota, error) {
	var q DownloadQuota
	err := s.DB.QueryRow(`SELECT
	 (SELECT COUNT(*) FROM user_download_usage WHERE user_id=? AND day=? AND state='charged'),
	 (SELECT COUNT(*) FROM user_download_usage WHERE user_id=? AND day=? AND state='reserved'),
	 (SELECT COUNT(*) FROM user_download_requests WHERE user_id=? AND state='waiting')`,
		userID, DownloadDay(now), userID, DownloadDay(now), userID).Scan(&q.Used, &q.Reserved, &q.Waiting)
	return q, err
}

// RequestUserDownload never resets an automatic failure loop; only a deliberate retry does.
func (s *Store) RequestUserDownload(userID int64, bvid string, priority int, retry bool) (*DownloadRequest, error) {
	_, err := s.DB.Exec(`INSERT INTO user_download_requests(user_id,bvid,priority,created_at,updated_at)
	 SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM user_videos WHERE user_id=? AND bvid=?)
	 ON CONFLICT(user_id,bvid) DO UPDATE SET
	 state=CASE WHEN ? AND user_download_requests.state IN ('failed','done') THEN 'waiting' ELSE user_download_requests.state END,
	 attempts=CASE WHEN ? AND user_download_requests.state IN ('failed','done') THEN 0 ELSE user_download_requests.attempts END,
	 next_attempt_at=CASE WHEN ? THEN 0 ELSE user_download_requests.next_attempt_at END,
	 priority=MAX(user_download_requests.priority,excluded.priority),updated_at=excluded.updated_at`,
		userID, bvid, priority, Now(), Now(), userID, bvid, retry, retry, retry)
	if err != nil {
		return nil, err
	}
	return s.UserDownload(userID, bvid)
}

func (s *Store) UserDownload(userID int64, bvid string) (*DownloadRequest, error) {
	var r DownloadRequest
	err := s.DB.QueryRow(`SELECT id,user_id,bvid,state,job_id,error,next_attempt_at FROM user_download_requests WHERE user_id=? AND bvid=?`, userID, bvid).
		Scan(&r.ID, &r.UserID, &r.Bvid, &r.State, &r.JobID, &r.Error, &r.NextAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// VideoFilesPresent checks every current part, rather than trusting downloaded or P1 alone.
func (s *Store) VideoFilesPresent(dataDir, bvid string) bool {
	_, err := s.GetVideo(bvid)
	if err != nil {
		return false
	}
	pages, err := s.ListPages(bvid)
	if err != nil || len(pages) == 0 {
		return false
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return false
	}
	for _, p := range pages {
		rel, err := s.PartLocalPath(bvid, p.Page)
		if err != nil {
			return false
		}
		full, err := archivepath.Resolve(root, rel)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(full) {
			full = filepath.Join(root, rel)
		}
		abs, err := filepath.Abs(full)
		if err != nil {
			return false
		}
		inside, err := filepath.Rel(root, abs)
		if err != nil || inside == ".." || filepath.IsAbs(inside) || len(inside) > 3 && inside[:3] == ".."+string(filepath.Separator) {
			return false
		}
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return false
		}
	}
	return true
}

// Exhausted requests are deferred to midnight, so each batch makes progress to other users.
func (s *Store) PendingUserDownloads(now int64) ([]DownloadRequest, error) {
	rows, err := s.DB.Query(`SELECT r.id,r.user_id,r.bvid FROM user_download_requests r
	 JOIN users u ON u.id=r.user_id JOIN user_tiers t ON t.id=u.tier_id JOIN videos v ON v.bvid=r.bvid
	 WHERE r.state='waiting' AND r.next_attempt_at<=? AND u.enabled=1 AND v.status='ok'
	 AND NOT EXISTS(SELECT 1 FROM user_download_requests active WHERE active.bvid=r.bvid AND active.state IN ('queued','running'))
	 ORDER BY r.id LIMIT 200`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadRequest
	for rows.Next() {
		var r DownloadRequest
		if err := rows.Scan(&r.ID, &r.UserID, &r.Bvid); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueueUserDownload atomically reserves quota AND creates the durable job.
// Manager calls this while holding its queue lock, then installs the returned ID in memory.
func (s *Store) QueueUserDownload(id, now int64, complete bool) (int64, string, int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, "", 0, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer lock before reading quota (avoids WAL read-upgrade races).
	if _, err = tx.Exec(`UPDATE user_download_requests SET updated_at=updated_at WHERE id=?`, id); err != nil {
		return 0, "", 0, err
	}
	var uid int64
	var nextAttempt int64
	var bvid, state string
	var enabled, cap, priority int
	err = tx.QueryRow(`SELECT r.user_id,r.bvid,r.state,r.priority,u.enabled,t.max_daily_downloads,r.next_attempt_at
	 FROM user_download_requests r JOIN users u ON u.id=r.user_id JOIN user_tiers t ON t.id=u.tier_id WHERE r.id=?`, id).
		Scan(&uid, &bvid, &state, &priority, &enabled, &cap, &nextAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", 0, nil
	}
	if err != nil {
		return 0, "", 0, err
	}
	if state != "waiting" || enabled != 1 || (!complete && nextAttempt > now) {
		return 0, "", 0, nil
	}
	if complete {
		if _, err = tx.Exec(`UPDATE videos SET downloaded=1,
		 local_path=CASE WHEN local_path='' THEN printf('media/%s/p%d/%s-p%d.mp4',bvid,
		 (SELECT page FROM pages WHERE bvid=videos.bvid ORDER BY page LIMIT 1),bvid,
		 (SELECT page FROM pages WHERE bvid=videos.bvid ORDER BY page LIMIT 1)) ELSE local_path END WHERE bvid=?`, bvid); err != nil {
			return 0, "", 0, err
		}
		if _, err = tx.Exec(`UPDATE user_download_requests SET next_attempt_at=0 WHERE bvid=? AND state='waiting'`, bvid); err != nil {
			return 0, "", 0, err
		}
		_, err = tx.Exec(`UPDATE user_download_requests SET state='done',error='',updated_at=? WHERE id=?`, now, id)
		if err != nil {
			return 0, "", 0, err
		}
		return 0, bvid, priority, tx.Commit()
	}
	var used int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM user_download_usage WHERE user_id=? AND day=? AND state IN ('reserved','charged')`, uid, DownloadDay(now)).Scan(&used); err != nil {
		return 0, "", 0, err
	}
	if used >= cap {
		t := time.Unix(now, 0).In(time.Local)
		next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.Local).Unix()
		if _, err = tx.Exec(`UPDATE user_download_requests SET next_attempt_at=?,error='' WHERE id=?`, next, id); err != nil {
			return 0, "", 0, err
		}
		return 0, "", 0, tx.Commit()
	}
	// There can be only one prepared file download per BV, across all requesters.
	var active bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_download_requests WHERE bvid=? AND state IN ('queued','running'))`, bvid).Scan(&active); err != nil {
		return 0, "", 0, err
	}
	if active {
		return 0, "", 0, nil
	}
	payload, _ := json.Marshal(map[string]any{"request_id": id, "bvid": bvid})
	var jobID int64
	err = tx.QueryRow(`INSERT INTO jobs(type,payload_json,state,priority,created_at,updated_at) VALUES('user_download',?,'queued',?,?,?) RETURNING id`, string(payload), priority, now, now).Scan(&jobID)
	if err != nil {
		return 0, "", 0, err
	}
	if _, err = tx.Exec(`INSERT INTO user_download_usage(request_id,user_id,day,state,created_at) VALUES(?,?,?,'reserved',?)`, id, uid, DownloadDay(now), now); err != nil {
		return 0, "", 0, err
	}
	if _, err = tx.Exec(`UPDATE user_download_requests SET state='queued',job_id=?,updated_at=? WHERE id=?`, jobID, now, id); err != nil {
		return 0, "", 0, err
	}
	return jobID, bvid, priority, tx.Commit()
}

func (s *Store) BeginUserDownload(id, jobID int64) (bool, error) {
	res, err := s.DB.Exec(`UPDATE user_download_requests SET state='running',updated_at=? WHERE id=? AND job_id=?
	 AND state IN ('queued','running') AND EXISTS(SELECT 1 FROM users u WHERE u.id=user_download_requests.user_id AND u.enabled=1)`, Now(), id, jobID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// A replayed old job must not release a newer reservation or reopen a completed request.
func (s *Store) SkipUserDownload(id, jobID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE user_download_usage SET state='released' WHERE request_id=? AND state='reserved'
	 AND EXISTS(SELECT 1 FROM user_download_requests r WHERE r.id=? AND r.job_id=? AND r.state IN ('queued','running'))`, id, id, jobID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE user_download_requests SET state='waiting',job_id=0,next_attempt_at=0,updated_at=?
	 WHERE id=? AND job_id=? AND state IN ('queued','running')`, Now(), id, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishUserDownload charges the reservation day, even when a long download crosses midnight.
func (s *Store) FinishUserDownload(id int64, complete, charge bool, downloadErr error) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	usageState := "released"
	if complete && charge {
		usageState = "charged"
	}
	if _, err = tx.Exec(`UPDATE user_download_usage SET state=? WHERE request_id=? AND state='reserved'`, usageState, id); err != nil {
		return err
	}
	state, msg := "waiting", ""
	next := int64(0)
	if complete {
		state = "done"
	}
	if downloadErr != nil {
		var attempts int
		if err = tx.QueryRow(`SELECT attempts FROM user_download_requests WHERE id=?`, id).Scan(&attempts); err != nil {
			return err
		}
		attempts++
		msg = downloadErr.Error()
		delay := int64(60) * (1 << min(attempts, 6))
		next = Now() + delay
		if attempts >= 5 {
			state = "failed"
		}
		if _, err = tx.Exec(`UPDATE user_download_requests SET attempts=? WHERE id=?`, attempts, id); err != nil {
			return err
		}
	}
	if !complete {
		// A released slot is immediately usable by another request from this user.
		if _, err = tx.Exec(`UPDATE user_download_requests SET next_attempt_at=0 WHERE state='waiting' AND user_id=(SELECT user_id FROM user_download_requests WHERE id=?)`, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE user_download_requests SET state=?,error=?,job_id=0,next_attempt_at=?,updated_at=? WHERE id=? AND state!='cancelled'`, state, msg, next, Now(), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ReconcileDownloadRequests handles worker panics/old terminal jobs without replaying charges.
func (s *Store) ReconcileDownloadRequests() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE user_download_usage SET state='released' WHERE state='reserved' AND request_id IN
	 (SELECT r.id FROM user_download_requests r LEFT JOIN jobs j ON j.id=r.job_id
	 WHERE r.state IN ('queued','running') AND (j.id IS NULL OR j.state NOT IN ('queued','running')))`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE user_download_requests SET state='failed',error='下载任务异常结束，请重试',job_id=0,updated_at=?
	 WHERE state IN ('queued','running') AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=job_id AND j.state IN ('queued','running'))`, Now())
	if err != nil {
		return err
	}
	return tx.Commit()
}
