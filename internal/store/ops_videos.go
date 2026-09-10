package store

import (
	"database/sql"
	"errors"
	"strings"
)

var ErrNotFound = errors.New("not found")

func scanVideo(row interface{ Scan(...any) error }) (*Video, error) {
	var v Video
	var downloaded int
	err := row.Scan(&v.Bvid, &v.Aid, &v.Title, &v.Description, &v.Pic,
		&v.OwnerMid, &v.OwnerName, &v.OwnerFace, &v.OwnerLevel, &v.Pubdate, &v.Duration,
		&v.PagesCount, &v.Status, &v.FirstArchivedAt, &v.LastSyncAt,
		&downloaded, &v.LocalPath,
		&v.LastStatsAt, &v.LastCommentsAt, &v.LastDanmakuAt, &v.LastSubsAt, &v.SourceID,
		&v.Tid, &v.Tname, &v.Orientation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Downloaded = downloaded == 1
	return &v, nil
}

const videoCols = `bvid, aid, title, description, pic, owner_mid, owner_name, owner_face, owner_level,
pubdate, duration, pages_count, status, first_archived_at, last_sync_at, downloaded, local_path,
last_stats_at, last_comments_at, last_danmaku_at, last_subtitles_at, source_id,
tid, tname, orientation`

// UpsertVideo 保存/更新视频主记录。first_archived_at 保持首次值。
func (s *Store) UpsertVideo(v *Video) error {
	if v.Status == "" {
		v.Status = "ok"
	}
	if v.SourceID != 0 {
		// 保留首次关联的源（换源同步时避免覆盖）
		_ = v.SourceID
	}
	_, err := s.DB.Exec(`INSERT INTO videos(`+videoCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(bvid) DO UPDATE SET
		  aid=excluded.aid, title=excluded.title, description=excluded.description, pic=excluded.pic,
		  owner_mid=excluded.owner_mid, owner_name=excluded.owner_name, owner_face=excluded.owner_face,
		  pubdate=excluded.pubdate, duration=excluded.duration, pages_count=excluded.pages_count,
		  status=excluded.status, last_sync_at=excluded.last_sync_at,
		  last_stats_at=excluded.last_stats_at, last_comments_at=excluded.last_comments_at,
		  last_danmaku_at=excluded.last_danmaku_at, last_subtitles_at=excluded.last_subtitles_at,
		  tid=excluded.tid, tname=excluded.tname, orientation=excluded.orientation,
		  source_id=CASE WHEN videos.source_id=0 THEN excluded.source_id ELSE videos.source_id END`,
		v.Bvid, v.Aid, v.Title, v.Description, v.Pic,
		v.OwnerMid, v.OwnerName, v.OwnerFace, v.OwnerLevel, v.Pubdate, v.Duration,
		v.PagesCount, v.Status, v.FirstArchivedAt, v.LastSyncAt,
		boolInt(v.Downloaded), v.LocalPath,
		v.LastStatsAt, v.LastCommentsAt, v.LastDanmakuAt, v.LastSubsAt, v.SourceID,
		v.Tid, v.Tname, v.Orientation)
	return err
}

// UpdateVideoSyncTimes 按类型刷新 videos 的同步时间戳（只更新传 true 的项）。
func (s *Store) UpdateVideoSyncTimes(bvid string, stats, comments, danmaku, subtitles bool, ts int64) error {
	sets, args := []string{}, []any{}
	if stats {
		sets = append(sets, "last_stats_at=?")
		args = append(args, ts)
	}
	if comments {
		sets = append(sets, "last_comments_at=?")
		args = append(args, ts)
	}
	if danmaku {
		sets = append(sets, "last_danmaku_at=?")
		args = append(args, ts)
	}
	if subtitles {
		sets = append(sets, "last_subtitles_at=?")
		args = append(args, ts)
	}
	if len(sets) == 0 {
		return nil
	}
	_, err := s.DB.Exec(`UPDATE videos SET `+strings.Join(sets, ",")+` WHERE bvid=?`, append(args, bvid)...)
	return err
}

// MarkVideoGone 把已消失/不可见的视频标 gone，保留已有数据。
func (s *Store) MarkVideoGone(bvid string, status string) error {
	if status == "" {
		status = "gone"
	}
	_, err := s.DB.Exec(`UPDATE videos SET status=?, last_sync_at=? WHERE bvid=?`, status, Now(), bvid)
	return err
}

func (s *Store) GetVideo(bvid string) (*Video, error) {
	return scanVideo(s.DB.QueryRow(`SELECT `+videoCols+` FROM videos WHERE bvid=?`, bvid))
}

// ListVideos 支持按 owner/status 过滤与简单分页（limit<=0 表示不分页）。
func (s *Store) ListVideos(ownerMid int64, status string, limit, offset int) ([]*Video, error) {
	where, args := []string{"1=1"}, []any{}
	if ownerMid > 0 {
		where = append(where, "owner_mid=?")
		args = append(args, ownerMid)
	}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	q := `SELECT ` + videoCols + ` FROM videos WHERE ` + strings.Join(where, " AND ") + ` ORDER BY pubdate DESC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SearchVideosFTS 用 FTS5 关键词搜索，返回 bvid 列表。
func (s *Store) SearchVideosFTS(query string, limit int) ([]string, error) {
	rows, err := s.DB.Query(`SELECT bvid FROM videos_fts WHERE videos_fts MATCH ? ORDER BY rank LIMIT ?`, ftsQuery(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SearchVideosByTag 按标签名模糊匹配视频（videos_fts 只索引标题/简介，
// 而 teriteri 热搜词全部来自标签，只有标题搜索会基本落空）。
func (s *Store) SearchVideosByTag(name string, limit int) ([]string, error) {
	name = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(name)
	rows, err := s.DB.Query(
		`SELECT DISTINCT vt.bvid FROM video_tags vt JOIN tags t ON t.id = vt.tag_id
		 WHERE t.name LIKE ? ESCAPE '\' LIMIT ?`, "%"+name+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReindexVideoFTS 重建单个视频的 FTS 行。
func (s *Store) ReindexVideoFTS(v *Video) error {
	if _, err := s.DB.Exec(`DELETE FROM videos_fts WHERE bvid=?`, v.Bvid); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO videos_fts(bvid, title, description) VALUES(?,?,?)`, v.Bvid, v.Title, v.Description)
	return err
}

// ---- pages ----

func (s *Store) UpsertPage(p *Page) error {
	_, err := s.DB.Exec(`INSERT INTO pages(bvid, cid, page, part, duration, first_seen_at, last_seen_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(bvid, cid) DO UPDATE SET page=excluded.page, part=excluded.part,
		  duration=excluded.duration, last_seen_at=excluded.last_seen_at`,
		p.Bvid, p.Cid, p.Page, p.Part, p.Duration, p.FirstSeenAt, p.LastSeenAt)
	return err
}

func (s *Store) ListPages(bvid string) ([]*Page, error) {
	rows, err := s.DB.Query(`SELECT bvid, cid, page, part, duration, first_seen_at, last_seen_at FROM pages WHERE bvid=? ORDER BY page`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Page
	for rows.Next() {
		var p Page
		if err := rows.Scan(&p.Bvid, &p.Cid, &p.Page, &p.Part, &p.Duration, &p.FirstSeenAt, &p.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ---- stats history ----

func (s *Store) AppendStats(st *StatsSnapshot) error {
	_, err := s.DB.Exec(`INSERT INTO video_stats_history(bvid, ts, view, danmaku, reply, favorite, coin, share, like)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		st.Bvid, st.Ts, st.View, st.Danmaku, st.Reply, st.Favorite, st.Coin, st.Share, st.Like)
	return err
}

func (s *Store) ListStats(bvid string) ([]*StatsSnapshot, error) {
	rows, err := s.DB.Query(`SELECT bvid, ts, view, danmaku, reply, favorite, coin, share, like FROM video_stats_history WHERE bvid=? ORDER BY ts`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StatsSnapshot
	for rows.Next() {
		var t StatsSnapshot
		if err := rows.Scan(&t.Bvid, &t.Ts, &t.View, &t.Danmaku, &t.Reply, &t.Favorite, &t.Coin, &t.Share, &t.Like); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// LatestStats 某视频最新一条统计快照（statsOf/卡片/NFO 等只取末值的读路径用，
// 避免为一条记录加载全量历史；历史明细仅详情接口走 ListStats）。
func (s *Store) LatestStats(bvid string) (*StatsSnapshot, error) {
	var t StatsSnapshot
	err := s.DB.QueryRow(`SELECT bvid, ts, view, danmaku, reply, favorite, coin, share, like
		FROM video_stats_history WHERE bvid=? ORDER BY ts DESC LIMIT 1`, bvid).
		Scan(&t.Bvid, &t.Ts, &t.View, &t.Danmaku, &t.Reply, &t.Favorite, &t.Coin, &t.Share, &t.Like)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// RecentStats 返回某视频最近 n 条统计快照（按时间升序返回），供自适应冷却估增速。
func (s *Store) RecentStats(bvid string, n int) ([]*StatsSnapshot, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(`SELECT bvid, ts, view, danmaku, reply, favorite, coin, share, like
		FROM video_stats_history WHERE bvid=? ORDER BY ts DESC LIMIT ?`, bvid, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StatsSnapshot
	for rows.Next() {
		var t StatsSnapshot
		if err := rows.Scan(&t.Bvid, &t.Ts, &t.View, &t.Danmaku, &t.Reply, &t.Favorite, &t.Coin, &t.Share, &t.Like); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // DESC 查出，反转为升序
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// FreshStatsDue 新发加速扫描：发布未满 freshHours 小时、统计快照已超过 freshMinutes
// 分钟未刷新的视频。pubdate=0（未知）与已消失视频不参与。
func (s *Store) FreshStatsDue(now int64, freshHours, freshMinutes, limit int) ([]string, error) {
	rows, err := s.DB.Query(`SELECT bvid FROM videos
		WHERE status='ok' AND pubdate > ? AND last_stats_at < ?
		ORDER BY pubdate DESC LIMIT ?`,
		now-int64(freshHours)*3600, now-int64(freshMinutes)*60, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- videos marked gone before download ----

func (s *Store) SetVideoDownloaded(bvid, localPath string) error {
	_, err := s.DB.Exec(`UPDATE videos SET downloaded=1, local_path=? WHERE bvid=?`, localPath, bvid)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ftsQuery 把普通关键词转成 FTS5 可用查询（前缀匹配）。
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	for i, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		fields[i] = `"` + f + `"*`
	}
	return strings.Join(fields, " ")
}

// VideoWithCounts 视频主记录 + 各部分归档完整度计数（管理控制台视角）。
type VideoWithCounts struct {
	Video
	DanmakuCount    int `json:"danmaku_count"`
	CommentsVisible int `json:"comments_visible"`
	CommentsDeleted int `json:"comments_deleted"`
	SubtitleCount   int `json:"subtitle_count"`
}

const countExpr = `,
 (SELECT COUNT(*) FROM danmaku d WHERE d.bvid=v.bvid AND d.status='visible') AS dm_n,
 (SELECT COUNT(*) FROM comments c WHERE c.bvid=v.bvid AND c.status='visible') AS cm_v,
 (SELECT COUNT(*) FROM comments c WHERE c.bvid=v.bvid AND c.status='deleted') AS cm_d,
 (SELECT COUNT(*) FROM subtitles sb WHERE sb.bvid=v.bvid) AS sb_n`

// qualifiedVideoCols 给 videoCols 加 v. 前缀（联表/子查询场景）。
func qualifiedVideoCols() string {
	parts := strings.Split(videoCols, ",")
	for i, p := range parts {
		parts[i] = "v." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func scanVideoWithCounts(row interface{ Scan(...any) error }) (*VideoWithCounts, error) {
	var vc VideoWithCounts
	var downloaded int
	err := row.Scan(&vc.Bvid, &vc.Aid, &vc.Title, &vc.Description, &vc.Pic,
		&vc.OwnerMid, &vc.OwnerName, &vc.OwnerFace, &vc.OwnerLevel, &vc.Pubdate, &vc.Duration,
		&vc.PagesCount, &vc.Status, &vc.FirstArchivedAt, &vc.LastSyncAt,
		&downloaded, &vc.LocalPath,
		&vc.LastStatsAt, &vc.LastCommentsAt, &vc.LastDanmakuAt, &vc.LastSubsAt, &vc.SourceID,
		&vc.Tid, &vc.Tname, &vc.Orientation,
		&vc.DanmakuCount, &vc.CommentsVisible, &vc.CommentsDeleted, &vc.SubtitleCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	vc.Downloaded = downloaded == 1
	return &vc, nil
}

// ListVideosWithCounts 列表 + 完整度计数（owner/status/limit 语义同 ListVideos）。
func (s *Store) ListVideosWithCounts(ownerMid int64, status string, limit, offset int) ([]*VideoWithCounts, error) {
	where, args := []string{"1=1"}, []any{}
	if ownerMid > 0 {
		where = append(where, "owner_mid=?")
		args = append(args, ownerMid)
	}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	q := `SELECT ` + qualifiedVideoCols() + countExpr + `
		FROM videos v WHERE ` + strings.Join(where, " AND ") + ` ORDER BY v.pubdate DESC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VideoWithCounts
	for rows.Next() {
		vc, err := scanVideoWithCounts(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, vc)
	}
	return out, rows.Err()
}

// GetVideoWithCounts 单个 + 计数。
func (s *Store) GetVideoWithCounts(bvid string) (*VideoWithCounts, error) {
	q := `SELECT ` + qualifiedVideoCols() + countExpr + ` FROM videos v WHERE v.bvid=?`
	return scanVideoWithCounts(s.DB.QueryRow(q, bvid))
}

// VideoStatusCounts 各状态视频数（管理端分类 Tab 徽标用）。
func (s *Store) VideoStatusCounts() (map[string]int64, error) {
	rows, err := s.DB.Query(`SELECT status, COUNT(*) FROM videos GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ListVideosPage 归档项目分页版：status（ok|gone|private）与 dl（downloaded=1|0）过滤，
// q 模糊匹配 标题/简介/UP主/bvid；sort ∈ {title,pubdate,last_sync} 白名单，order=asc|desc
// （title 默认升序，时间列默认降序）；返回 (页内记录, 过滤后总数)。
func (s *Store) ListVideosPage(status, dl, q, sort, order string, limit, offset int) ([]*VideoWithCounts, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where, args := []string{"1=1"}, []any{}
	switch status {
	case "ok", "gone", "private":
		where = append(where, "v.status=?")
		args = append(args, status)
	}
	if dl == "1" || dl == "0" {
		where = append(where, "v.downloaded=?")
		args = append(args, dl)
	}
	if qs := strings.TrimSpace(q); qs != "" {
		pat := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(qs) + "%"
		where = append(where, `(v.title LIKE ? ESCAPE '\' OR v.description LIKE ? ESCAPE '\'
			OR v.owner_name LIKE ? ESCAPE '\' OR v.bvid LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat, pat)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM videos v WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sortCol := map[string]string{"title": "v.title", "pubdate": "v.pubdate", "last_sync": "v.last_sync_at"}[sort]
	if sortCol == "" {
		sortCol = "v.pubdate"
	}
	dir := "DESC"
	if sort == "title" {
		dir = "ASC"
	}
	if order == "asc" {
		dir = "ASC"
	} else if order == "desc" {
		dir = "DESC"
	}
	collate := ""
	if sortCol == "v.title" {
		collate = " COLLATE NOCASE"
	}
	q2 := `SELECT ` + qualifiedVideoCols() + countExpr + ` FROM videos v WHERE ` + whereSQL +
		` ORDER BY ` + sortCol + collate + ` ` + dir + `, v.pubdate DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q2, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*VideoWithCounts
	for rows.Next() {
		vc, err := scanVideoWithCounts(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, vc)
	}
	return out, total, rows.Err()
}
