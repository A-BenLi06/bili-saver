package store

// 评论与弹幕：只增不删。消失检测基于 last_seen_at 对账。

func (s *Store) UpsertComment(c *Comment) error {
	_, err := s.DB.Exec(`INSERT INTO comments(rpid, bvid, parent_rpid, root_rpid, member_mid, member_name,
		member_face, member_level, content, like_count, reply_count, ctime, location, status, first_seen_at, last_seen_at, deleted_at, raw_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'visible',?,?,0,?)
		ON CONFLICT(rpid) DO UPDATE SET
		  like_count=excluded.like_count, reply_count=excluded.reply_count,
		  status='visible', deleted_at=0, last_seen_at=excluded.last_seen_at, raw_json=excluded.raw_json`,
		c.Rpid, c.Bvid, c.ParentRpid, c.RootRpid, c.MemberMid, c.MemberName,
		c.MemberFace, c.MemberLevel, c.Content, c.LikeCount, c.ReplyCount, c.Ctime, c.Location,
		c.FirstSeenAt, c.LastSeenAt, c.RawJSON)
	if err != nil {
		return err
	}
	// best-effort 维护评论全文索引（派生数据）：内容按 upsert 语义不可变，缺失即补，
	// 从库内真值取 content；索引写失败不阻断归档，下一轮重同步会自愈。
	_, _ = s.DB.Exec(`INSERT INTO comments_fts(rpid, content)
		SELECT rpid, content FROM comments
		WHERE rpid=? AND rpid NOT IN (SELECT rpid FROM comments_fts)`, c.Rpid)
	return nil
}

// MarkMissingComments 对账：把本轮未出现且此前 visible 的评论标为 deleted。
// beforeTs 为本轮同步开始时间；first=true 时跳过（首次归档不存在消失）。
func (s *Store) MarkMissingComments(bvid string, beforeTs int64, first bool) (int64, error) {
	if first {
		return 0, nil
	}
	res, err := s.DB.Exec(`UPDATE comments SET status='deleted', deleted_at=?
		WHERE bvid=? AND status='visible' AND last_seen_at<?`,
		Now(), bvid, beforeTs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkMissingCommentsGuarded 防御式对账：B 站接口偶发截断翻页（风控降级）会造成
// 大批“假消失”。单轮消失占比 >30% 且绝对数 >50 时视为异常：
// 需连续两轮（anomaly 计数）同样异常才真正标删，否则跳过本轮并累加计数。
// 返回 (本轮标记数, 是否处于异常待确认状态)。
func (s *Store) MarkMissingCommentsGuarded(bvid string, beforeTs int64, first bool, seenThisRound int) (int64, bool, error) {
	if first {
		return 0, false, nil
	}
	var pending int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE bvid=? AND status='visible' AND last_seen_at<?`,
		bvid, beforeTs).Scan(&pending); err != nil {
		return 0, false, err
	}
	total := pending + int64(seenThisRound)
	anomalous := pending > 50 && total > 0 && float64(pending)/float64(total) > 0.3
	var prevAnomaly int64
	_ = s.DB.QueryRow(`SELECT comments_anomaly FROM videos WHERE bvid=?`, bvid).Scan(&prevAnomaly)
	if anomalous && prevAnomaly < 1 {
		// 首次异常：不标删，等待下一轮确认
		_, err := s.DB.Exec(`UPDATE videos SET comments_anomaly=comments_anomaly+1 WHERE bvid=?`, bvid)
		return 0, true, err
	}
	// 正常（或已连续两轮异常，确认标删）
	n, err := s.MarkMissingComments(bvid, beforeTs, first)
	if err != nil {
		return 0, false, err
	}
	_, err = s.DB.Exec(`UPDATE videos SET comments_anomaly=0 WHERE bvid=?`, bvid)
	return n, false, err
}

func scanComment(row interface{ Scan(...any) error }) (*Comment, error) {
	var c Comment
	err := row.Scan(&c.Rpid, &c.Bvid, &c.ParentRpid, &c.RootRpid, &c.MemberMid, &c.MemberName,
		&c.MemberFace, &c.MemberLevel, &c.Content, &c.LikeCount, &c.ReplyCount, &c.Ctime, &c.Location,
		&c.Status, &c.FirstSeenAt, &c.LastSeenAt, &c.DeletedAt, &c.RawJSON)
	if err != nil && err.Error() == "sql: no rows in result set" {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

const commentCols = `rpid, bvid, parent_rpid, root_rpid, member_mid, member_name, member_face, member_level,
content, like_count, reply_count, ctime, location, status, first_seen_at, last_seen_at, deleted_at, raw_json`

// ListComments status 可为空（全部）；limit<=0 不限。
func (s *Store) ListComments(bvid, status string, limit, offset int) ([]*Comment, error) {
	where, args := []string{"bvid=?"}, []any{bvid}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	q := `SELECT ` + commentCols + ` FROM comments WHERE ` + where[0]
	if len(where) > 1 {
		q += ` AND ` + where[1]
	}
	q += ` ORDER BY like_count DESC, ctime ASC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCommentsByStatus 返回 (visible, deleted) 计数。
func (s *Store) CountCommentsByStatus(bvid string) (visible int, deleted int, err error) {
	err = s.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status='visible' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='deleted' THEN 1 ELSE 0 END),0)
		FROM comments WHERE bvid=?`, bvid).Scan(&visible, &deleted)
	return
}

// ListCommentFaces 返回按 mid 去重的评论者头像；bvid 为空时全库。
// 只增不删语义下已消失评论的头像一并保留（归档价值）。
func (s *Store) ListCommentFaces(bvid string) ([]Face, error) {
	q := `SELECT member_mid, member_face FROM comments
		WHERE member_mid>0 AND member_face<>''`
	args := []any{}
	if bvid != "" {
		q += ` AND bvid=?`
		args = append(args, bvid)
	}
	q += ` GROUP BY member_mid`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Face
	for rows.Next() {
		var f Face
		if err := rows.Scan(&f.Mid, &f.URL); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReindexCommentFTS 维护评论全文索引。
func (s *Store) ReindexCommentFTS(rpid int64, content string) error {
	if _, err := s.DB.Exec(`DELETE FROM comments_fts WHERE rpid=?`, rpid); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO comments_fts(rpid, content) VALUES(?,?)`, rpid, content)
	return err
}

func (s *Store) SearchCommentsFTS(query string, limit int) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT rpid FROM comments_fts WHERE comments_fts MATCH ? LIMIT ?`, ftsQuery(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CommentHit 全文搜索命中：评论本体 + 所属视频展示字段（不含 raw_json，响应保持轻量）。
type CommentHit struct {
	Comment
	VideoTitle string `json:"video_title"`
	OwnerName  string `json:"owner_name"`
}

// SearchCommentsFull 评论全文检索（FTS 联表 videos），按评论时间倒序返回，
// status 传 visible/deleted 过滤、空为全部。只读查询，不触碰消失语义与原始数据。
func (s *Store) SearchCommentsFull(query, status string, limit int) ([]*CommentHit, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := `SELECT c.rpid,c.bvid,c.parent_rpid,c.root_rpid,c.member_mid,c.member_name,c.member_face,
		c.member_level,c.content,c.like_count,c.reply_count,c.ctime,c.location,c.status,
		c.first_seen_at,c.last_seen_at,c.deleted_at, v.title, v.owner_name
		FROM comments_fts JOIN comments c ON c.rpid=comments_fts.rpid
		LEFT JOIN videos v ON v.bvid=c.bvid
		WHERE comments_fts MATCH ?`
	args := []any{ftsQuery(query)}
	if status == "visible" || status == "deleted" {
		q += ` AND c.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY c.ctime DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CommentHit
	for rows.Next() {
		var h CommentHit
		if err := rows.Scan(&h.Rpid, &h.Bvid, &h.ParentRpid, &h.RootRpid, &h.MemberMid, &h.MemberName,
			&h.MemberFace, &h.MemberLevel, &h.Content, &h.LikeCount, &h.ReplyCount, &h.Ctime,
			&h.Location, &h.Status, &h.FirstSeenAt, &h.LastSeenAt, &h.DeletedAt,
			&h.VideoTitle, &h.OwnerName); err != nil {
			return nil, err
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// ---- danmaku ----

func (s *Store) UpsertDanmaku(d *Danmaku) error {
	_, err := s.DB.Exec(`INSERT INTO danmaku(dmid, bvid, cid, timepoint, mode, fontsize, color, content, ctime,
		status, first_seen_at, last_seen_at, deleted_at)
		VALUES(?,?,?,?,?,?,?,?,?, 'visible', ?, ?, 0)
		ON CONFLICT(dmid) DO UPDATE SET status='visible', deleted_at=0, last_seen_at=excluded.last_seen_at`,
		d.Dmid, d.Bvid, d.Cid, d.Timepoint, d.Mode, d.Fontsize, d.Color, d.Content, d.Ctime,
		d.FirstSeenAt, d.LastSeenAt)
	return err
}

// MarkMissingDanmaku 对账标记消失弹幕。
func (s *Store) MarkMissingDanmaku(cid int64, beforeTs int64, first bool) (int64, error) {
	if first {
		return 0, nil
	}
	res, err := s.DB.Exec(`UPDATE danmaku SET status='deleted', deleted_at=?
		WHERE cid=? AND status='visible' AND last_seen_at<?`, Now(), cid, beforeTs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkMissingDanmakuGuarded 弹幕版防御：seg.so 被风控回退 list.so 时有约 1200 条
// 上限，会造成大批假消失（如库内 2000 条只见回 1200 条）。占比 >30% 且 >100 条
// 视为异常，需连续两轮确认（videos.danmaku_anomaly）才真正标删。
func (s *Store) MarkMissingDanmakuGuarded(bvid string, cid int64, beforeTs int64, first bool, seenThisRound int) (int64, bool, error) {
	if first {
		return 0, false, nil
	}
	var pending int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM danmaku WHERE cid=? AND status='visible' AND last_seen_at<?`,
		cid, beforeTs).Scan(&pending); err != nil {
		return 0, false, err
	}
	total := pending + int64(seenThisRound)
	anomalous := pending > 100 && total > 0 && float64(pending)/float64(total) > 0.3
	var prevAnomaly int64
	_ = s.DB.QueryRow(`SELECT danmaku_anomaly FROM videos WHERE bvid=?`, bvid).Scan(&prevAnomaly)
	if anomalous && prevAnomaly < 1 {
		_, err := s.DB.Exec(`UPDATE videos SET danmaku_anomaly=danmaku_anomaly+1 WHERE bvid=?`, bvid)
		return 0, true, err
	}
	n, err := s.MarkMissingDanmaku(cid, beforeTs, first)
	if err != nil {
		return 0, false, err
	}
	_, err = s.DB.Exec(`UPDATE videos SET danmaku_anomaly=0 WHERE bvid=?`, bvid)
	return n, false, err
}

func (s *Store) ListDanmaku(cid int64, status string) ([]*Danmaku, error) {
	q := `SELECT dmid, bvid, cid, timepoint, mode, fontsize, color, content, ctime, status, first_seen_at, last_seen_at, deleted_at FROM danmaku WHERE cid=?`
	args := []any{cid}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY timepoint`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Danmaku
	for rows.Next() {
		var d Danmaku
		if err := rows.Scan(&d.Dmid, &d.Bvid, &d.Cid, &d.Timepoint, &d.Mode, &d.Fontsize, &d.Color,
			&d.Content, &d.Ctime, &d.Status, &d.FirstSeenAt, &d.LastSeenAt, &d.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}
