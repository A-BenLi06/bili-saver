package store

// UP主动态/图文归档（v10）。语义对齐 comments：只增不删、消失显式标记；
// 大面积消失走防御（>30% 且 >50 条跳过标记，防接口截断制造假消失）。

import "time"

const spacePostCols = `id, mid, post_id, dtype, kind, bvid, title, content, pics, origin, raw, pubdate, first_seen_at, last_seen_at, deleted_at`

func scanSpacePost(row interface{ Scan(...any) error }) (*SpacePost, error) {
	var p SpacePost
	err := row.Scan(&p.ID, &p.Mid, &p.PostID, &p.Dtype, &p.Kind, &p.Bvid, &p.Title,
		&p.Content, &p.Pics, &p.Origin, &p.Raw, &p.Pubdate, &p.FirstSeenAt, &p.LastSeenAt, &p.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpsertSpacePost 保存动态/图文。first_seen_at 保持首次值；重现（曾标记消失）
// 时清除 deleted_at；内容字段随最新值。
func (s *Store) UpsertSpacePost(p *SpacePost) error {
	_, err := s.DB.Exec(`INSERT INTO space_posts(mid, post_id, dtype, kind, bvid, title, content, pics, origin, raw, pubdate, first_seen_at, last_seen_at, deleted_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(mid, post_id) DO UPDATE SET
		  dtype=excluded.dtype, kind=excluded.kind, bvid=excluded.bvid, title=excluded.title,
		  content=excluded.content, pics=excluded.pics, raw=excluded.raw, pubdate=excluded.pubdate,
		  last_seen_at=excluded.last_seen_at, deleted_at=0`,
		p.Mid, p.PostID, p.Dtype, p.Kind, p.Bvid, p.Title, p.Content, p.Pics,
		p.Origin, p.Raw, p.Pubdate, p.FirstSeenAt, p.LastSeenAt, p.DeletedAt)
	return err
}

// ListSpacePosts UP主的动态/图文列表（kind 空=全部；includeDeleted=false 只看现存）。
func (s *Store) ListSpacePosts(mid int64, kind string, includeDeleted bool, limit, offset int) ([]*SpacePost, error) {
	where := "mid=?"
	args := []any{mid}
	if kind != "" {
		where += " AND kind=?"
		args = append(args, kind)
	}
	if !includeDeleted {
		where += " AND deleted_at=0"
	}
	q := `SELECT ` + spacePostCols + ` FROM space_posts WHERE ` + where + ` ORDER BY pubdate DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SpacePost
	for rows.Next() {
		p, err := scanSpacePost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountSpacePosts 计数（kind 空=全部）。
func (s *Store) CountSpacePosts(mid int64, kind string) (visible, deleted int, err error) {
	where := "mid=?"
	args := []any{mid}
	if kind != "" {
		where += " AND kind=?"
		args = append(args, kind)
	}
	err = s.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN deleted_at=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN deleted_at>0 THEN 1 ELSE 0 END),0)
		FROM space_posts WHERE `+where, args...).Scan(&visible, &deleted)
	return
}

// MarkSpacePostsGone 本轮 feed 全量拉取后，把未再出现的现存动态标记消失。
// 防御（对齐弹幕降级检测）：消失数 >30% 且 >50 条视为接口截断异常，本轮跳过。
// fullFetch=false（分页未到底/被截断）时直接跳过——数据不完整无法判断消失。
// 返回 (标记消失数, 已跳过)。
func (s *Store) MarkSpacePostsGone(mid int64, seen map[string]bool, fullFetch bool) (int, bool) {
	var existing []string
	rows, err := s.DB.Query(`SELECT post_id FROM space_posts WHERE mid=? AND deleted_at=0`, mid)
	if err != nil {
		return 0, true
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, true
		}
		existing = append(existing, id)
	}
	if err := rows.Err(); err != nil {
		return 0, true
	}
	if !fullFetch {
		return 0, true
	}
	var gone []string
	for _, id := range existing {
		if !seen[id] {
			gone = append(gone, id)
		}
	}
	// 防御：大面积消失更可能是接口截断/风控，宁可不标也不制造假消失
	if len(gone) > 50 && len(existing) > 0 && float64(len(gone))/float64(len(existing)) > 0.3 {
		return 0, true
	}
	if len(gone) == 0 {
		return 0, false
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, true
	}
	now := time.Now().Unix()
	for _, id := range gone {
		if _, err := tx.Exec(`UPDATE space_posts SET deleted_at=? WHERE mid=? AND post_id=? AND deleted_at=0`, now, mid, id); err != nil {
			tx.Rollback()
			return 0, true
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, true
	}
	return len(gone), false
}
