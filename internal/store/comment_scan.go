package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// CommentScan contains only cursor/coverage metadata. Epoch never changes on resume.
type CommentScan struct {
	Bvid                                       string
	Aid, Epoch, StartedAt                      int64
	Session, Policy                            string
	Full, First, MainDone, Capped              bool
	Offset                                     string
	MainPages, SubPages, Requests, Seen, Total int
	Floor, HighWater, RootCursor               int64
}

type CommentScanRoot struct {
	Root                      int64
	NextPN, Expected, Fetched int
	Done, Tail                bool
}

type CommentSyncState struct {
	LastFullAt, LatestMainCtime, RootCursor int64
}

func (s *Store) CommentSyncState(bvid string) (CommentSyncState, error) {
	var out CommentSyncState
	err := s.DB.QueryRow(`SELECT last_full_at,latest_main_ctime,root_cursor FROM comment_sync_state WHERE bvid=?`, bvid).Scan(&out.LastFullAt, &out.LatestMainCtime, &out.RootCursor)
	if err == sql.ErrNoRows {
		err = nil
	}
	return out, err
}

func (s *Store) LoadCommentScan(bvid string) (*CommentScan, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT state_json FROM comment_scan_state WHERE bvid=?`, bvid).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out CommentScan
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func clearCommentScan(tx *sql.Tx, bvid string) error {
	for _, table := range []string{"comment_scan_seen", "comment_scan_roots", "comment_scan_state"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE bvid=?`, bvid); err != nil {
			return err
		}
	}
	return nil
}

func writeCommentScan(tx *sql.Tx, scan *CommentScan) error {
	raw, err := json.Marshal(scan)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO comment_scan_state(bvid,state_json) VALUES(?,?) ON CONFLICT(bvid) DO UPDATE SET state_json=excluded.state_json`, scan.Bvid, string(raw))
	return err
}

func writeScanRoot(tx *sql.Tx, bvid string, root *CommentScanRoot, insert bool) error {
	raw, err := json.Marshal(root)
	if err != nil {
		return err
	}
	q := `INSERT INTO comment_scan_roots(bvid,root,next_pn,done,state_json) VALUES(?,?,?,?,?) ON CONFLICT(bvid,root) DO NOTHING`
	if !insert {
		q = `UPDATE comment_scan_roots SET next_pn=?,done=?,state_json=? WHERE bvid=? AND root=?`
	}
	if insert {
		_, err = tx.Exec(q, bvid, root.Root, root.NextPN, root.Done, string(raw))
	} else {
		_, err = tx.Exec(q, root.NextPN, root.Done, string(raw), bvid, root.Root)
	}
	return err
}

func (s *Store) StartCommentScan(scan *CommentScan, roots []*CommentScanRoot) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := clearCommentScan(tx, scan.Bvid); err != nil {
		return err
	}
	if err := writeCommentScan(tx, scan); err != nil {
		return err
	}
	for _, root := range roots {
		if err := writeScanRoot(tx, scan.Bvid, root, true); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CommitCommentPage commits origin rows, dedup coverage, sub-work and next cursor atomically.
// The caller is the only writer; workers only return API responses.
func (s *Store) CommitCommentPage(scan *CommentScan, comments []*Comment, roots []*CommentScanRoot, sub *CommentScanRoot, ps, maxComments, maxSub int) error {
	next := *scan
	var root CommentScanRoot
	if sub != nil {
		root = *sub
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seenStmt, err := tx.Prepare(`INSERT OR IGNORE INTO comment_scan_seen(bvid,rpid,paged) VALUES(?,?,0)`)
	if err != nil {
		return err
	}
	defer seenStmt.Close()
	pagedStmt, err := tx.Prepare(`UPDATE comment_scan_seen SET paged=1 WHERE bvid=? AND rpid=? AND paged=0`)
	if err != nil {
		return err
	}
	defer pagedStmt.Close()
	commentStmt, err := tx.Prepare(upsertCommentSQL)
	if err != nil {
		return err
	}
	defer commentStmt.Close()
	for _, c := range comments {
		if maxComments > 0 && next.Seen >= maxComments {
			next.Capped = true
			break
		}
		if sub != nil && maxSub > 0 && root.Fetched >= maxSub {
			next.Capped = true
			root.Done = true
			break
		}
		result, err := seenStmt.Exec(next.Bvid, c.Rpid)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			if _, err := commentStmt.Exec(c.Rpid, c.Bvid, c.ParentRpid, c.RootRpid, c.MemberMid, c.MemberName, c.MemberFace, c.MemberLevel, c.Content, c.LikeCount, c.ReplyCount, c.Ctime, c.Location, c.FirstSeenAt, c.LastSeenAt, c.RawJSON); err != nil {
				return err
			}
			next.Seen++
		}
		if sub != nil {
			result, err := pagedStmt.Exec(next.Bvid, c.Rpid)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			root.Fetched += int(n)
		}
	}
	if maxComments > 0 && next.Seen >= maxComments {
		next.Capped = true
	}
	if sub != nil {
		if maxSub > 0 && root.Fetched >= maxSub {
			root.Done = true
			next.Capped = true
		}
		if root.Tail {
			root.Done = root.Done || len(comments) < ps || (root.Expected > 0 && (root.NextPN-1)*ps >= root.Expected)
		} else {
			root.Done = root.Done || (root.Expected > 0 && root.Fetched >= root.Expected)
			if len(comments) < ps {
				root.Done = true
				if root.Fetched < root.Expected {
					next.Capped = true
				}
			}
		}
		if root.NextPN > 1000 && !root.Done {
			root.Done = true
			next.Capped = true
		}
		if err := writeScanRoot(tx, next.Bvid, &root, false); err != nil {
			return err
		}
	}
	for _, r := range roots {
		if err := writeScanRoot(tx, next.Bvid, r, true); err != nil {
			return err
		}
	}
	if err := writeCommentScan(tx, &next); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*scan = next
	if sub != nil {
		*sub = root
	}
	return nil
}

func (s *Store) PendingCommentRoots(bvid string, limit int) ([]*CommentScanRoot, error) {
	rows, err := s.DB.Query(`SELECT state_json FROM comment_scan_roots WHERE bvid=? AND done=0 ORDER BY next_pn,root LIMIT ?`, bvid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CommentScanRoot
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r CommentScanRoot
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// HasUnseenScanComments checks page progress against this round's persisted coverage.
// Some real main responses reuse the same cursor while returning new comment IDs.
func (s *Store) HasUnseenScanComments(bvid string, ids []int64) (bool, error) {
	unique := map[int64]bool{}
	args := []any{bvid}
	ph := ""
	for _, id := range ids {
		if unique[id] {
			continue
		}
		unique[id] = true
		if ph != "" {
			ph += ","
		}
		ph += "?"
		args = append(args, id)
	}
	if len(unique) == 0 {
		return false, nil
	}
	var covered int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM comment_scan_seen WHERE bvid=? AND rpid IN (`+ph+`)`, args...).Scan(&covered)
	return covered < len(unique), err
}

// RootReplyCounts uses one indexed query for a main page, never an entire comment load.
func (s *Store) RootReplyCounts(bvid string, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := ""
	args := []any{bvid}
	for _, id := range ids {
		if ph != "" {
			ph += ","
		}
		ph += "?"
		args = append(args, id)
	}
	rows, err := s.DB.Query(`SELECT rpid,reply_count FROM comments WHERE bvid=? AND root_rpid=0 AND rpid IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RotatingCommentRoots probes old threads even if their parent isn't in the recent head.
func (s *Store) RotatingCommentRoots(bvid string, after int64, limit int) ([]*CommentScanRoot, error) {
	query := func(after int64) ([]*CommentScanRoot, error) {
		rows, err := s.DB.Query(`SELECT rpid,reply_count FROM comments WHERE bvid=? AND root_rpid=0 AND reply_count>0 AND rpid>? ORDER BY rpid LIMIT ?`, bvid, after, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []*CommentScanRoot
		for rows.Next() {
			r := &CommentScanRoot{Tail: true}
			if err := rows.Scan(&r.Root, &r.Expected); err != nil {
				return nil, err
			}
			r.NextPN = max(1, (r.Expected-1)/20)
			out = append(out, r)
		}
		return out, rows.Err()
	}
	out, err := query(after)
	if err == nil && len(out) == 0 && after != 0 {
		return query(0)
	}
	return out, err
}

func (s *Store) FinishCommentScan(scan *CommentScan, fullAt int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO comment_sync_state(bvid,last_full_at,latest_main_ctime,root_cursor) VALUES(?,?,?,?)
		ON CONFLICT(bvid) DO UPDATE SET last_full_at=MAX(last_full_at,excluded.last_full_at),latest_main_ctime=MAX(latest_main_ctime,excluded.latest_main_ctime),root_cursor=excluded.root_cursor`, scan.Bvid, fullAt, scan.HighWater, scan.RootCursor); err != nil {
		return err
	}
	if err := clearCommentScan(tx, scan.Bvid); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DiscardCommentScan(bvid string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := clearCommentScan(tx, bvid); err != nil {
		return err
	}
	return tx.Commit()
}

// The entire reconciliation and checkpoint acknowledgement form one transaction.
// A restarted round cannot count twice as the two-round anomaly confirmation.
func (s *Store) CompleteCommentScan(scan *CommentScan, now int64) (int64, bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRow(`SELECT state_json FROM comment_scan_state WHERE bvid=?`, scan.Bvid).Scan(&raw); err != nil {
		return 0, false, err
	}
	var saved CommentScan
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return 0, false, err
	}
	if saved.Epoch != scan.Epoch || !saved.Full || !saved.MainDone || saved.Capped {
		return 0, false, fmt.Errorf("评论核对轮次不完整")
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM comment_scan_roots WHERE bvid=? AND done=0`, scan.Bvid).Scan(&pending); err != nil {
		return 0, false, err
	}
	if pending != 0 {
		return 0, false, fmt.Errorf("楼中楼尚未抓取完成")
	}
	n, anomalous, err := markMissingCommentsGuarded(tx, saved.Bvid, saved.Epoch, saved.First, saved.Seen)
	if err != nil {
		return 0, false, err
	}
	fullAt := now
	if anomalous {
		fullAt = 0
	}
	if _, err := tx.Exec(`INSERT INTO comment_sync_state(bvid,last_full_at,latest_main_ctime,root_cursor) VALUES(?,?,?,?) ON CONFLICT(bvid) DO UPDATE SET last_full_at=CASE WHEN ? THEN 0 ELSE excluded.last_full_at END,latest_main_ctime=MAX(latest_main_ctime,excluded.latest_main_ctime),root_cursor=excluded.root_cursor`, saved.Bvid, fullAt, saved.HighWater, saved.RootCursor, anomalous); err != nil {
		return 0, false, err
	}
	if err := clearCommentScan(tx, saved.Bvid); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return n, anomalous, nil
}
