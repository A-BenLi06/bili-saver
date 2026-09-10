// ops_site.go TeriTeri 站内互动数据（点赞/投币/收藏/关注/站内评论/站内弹幕）。
// 这些是归档站自己的社区数据，独立建表，绝不写入 B 站 origin 表；
// 展示时由 teriteri 适配层与 B 站快照数据合并。
package store

import (
	"strings"
)

// SetVideoAttitude 记录用户对视频的点赞/点踩态。
func (s *Store) SetVideoAttitude(uid int64, bvid string, love, unlove bool) error {
	_, err := s.DB.Exec(`INSERT INTO site_video_attitude(uid,bvid,love,unlove,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(uid,bvid) DO UPDATE SET love=excluded.love, unlove=excluded.unlove, updated_at=excluded.updated_at`,
		uid, bvid, b2i(love), b2i(unlove), Now())
	return err
}

func (s *Store) GetVideoAttitude(uid int64, bvid string) (love, unlove bool) {
	var l, u int
	if err := s.DB.QueryRow(`SELECT love, unlove FROM site_video_attitude WHERE uid=? AND bvid=?`, uid, bvid).Scan(&l, &u); err != nil {
		return false, false
	}
	return l != 0, u != 0
}

// ListLovedBvids 用户点过赞的视频（新→旧）。
func (s *Store) ListLovedBvids(uid int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT bvid FROM site_video_attitude WHERE uid=? AND love=1 ORDER BY updated_at DESC`, uid)
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

// CountVideoLikes 站内点赞数（展示时叠加在 B 站快照上）。
func (s *Store) CountVideoLikes(bvid string) int {
	return s.countInt(`SELECT COALESCE(SUM(love),0) FROM site_video_attitude WHERE bvid=?`, bvid)
}

// CoinVideo 投币（单次 1/2 枚，累计）。
func (s *Store) CoinVideo(uid int64, bvid string, n int) error {
	if n < 1 {
		n = 1
	}
	_, err := s.DB.Exec(`INSERT INTO site_video_coins(uid,bvid,coins,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(uid,bvid) DO UPDATE SET coins=coins+excluded.coins, updated_at=excluded.updated_at`,
		uid, bvid, n, Now())
	return err
}

func (s *Store) CountVideoCoins(bvid string) int {
	return s.countInt(`SELECT COALESCE(SUM(coins),0) FROM site_video_coins WHERE bvid=?`, bvid)
}

// SetCommentLove 评论点赞/取消。
func (s *Store) SetCommentLove(uid int64, rpid int64, on bool) error {
	if on {
		_, err := s.DB.Exec(`INSERT OR IGNORE INTO site_comment_attitude(uid,rpid,love,updated_at) VALUES(?,?,1,?)`, uid, rpid, Now())
		return err
	}
	_, err := s.DB.Exec(`UPDATE site_comment_attitude SET love=0, updated_at=? WHERE uid=? AND rpid=?`, Now(), uid, rpid)
	return err
}

// CountCommentLikes 站内给某条评论点的赞数。
func (s *Store) CountCommentLikes(rpid int64) int {
	return s.countInt(`SELECT COALESCE(SUM(love),0) FROM site_comment_attitude WHERE rpid=?`, rpid)
}

// ListLikedRpids 用户点过赞的评论 id 集合（前端 isLike 高亮）。
func (s *Store) ListLikedRpids(uid int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT rpid FROM site_comment_attitude WHERE uid=? AND love=1`, uid)
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

// ---- 收藏夹 ----

func (s *Store) CreateFavFolder(uid int64, name string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO site_favorite_folders(uid,name,type,ctime) VALUES(?,?,0,?)`, uid, name, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type FavFolder struct {
	Fid   int64  `json:"fid"`
	Name  string `json:"name"`
	Type  int    `json:"type"`
	Count int    `json:"count"`
}

// ListFavFolders 用户收藏夹列表（默认夹 type=1 自动补齐，收藏条目计数）。
func (s *Store) ListFavFolders(uid int64) ([]*FavFolder, error) {
	rows, err := s.DB.Query(`SELECT fid, name, type FROM site_favorite_folders WHERE uid=? ORDER BY type DESC, fid`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FavFolder
	for rows.Next() {
		f := &FavFolder{}
		if err := rows.Scan(&f.Fid, &f.Name, &f.Type); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 默认收藏夹兜底（fid=0 语义夹，物理条目存在 fid=0 下）
	hasDefault := false
	for _, f := range out {
		if f.Type == 1 {
			hasDefault = true
		}
	}
	if !hasDefault {
		out = append([]*FavFolder{{Fid: 0, Name: "默认收藏夹", Type: 1}}, out...)
	}
	for _, f := range out {
		f.Count = s.countInt(`SELECT COUNT(*) FROM site_favorite_items WHERE fid=?`, f.Fid)
	}
	return out, nil
}

func (s *Store) AddFavItem(fid int64, bvid string, uid int64) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO site_favorite_items(fid,bvid,uid,ctime) VALUES(?,?,?,?)`, fid, bvid, uid, Now())
	return err
}

func (s *Store) RemoveFavItem(fid int64, bvid string) error {
	_, err := s.DB.Exec(`DELETE FROM site_favorite_items WHERE fid=? AND bvid=?`, fid, bvid)
	return err
}

// ListFavItemFids 用户把某视频收藏进了哪些夹。
func (s *Store) ListFavItemFids(uid int64, bvid string) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT fid FROM site_favorite_items WHERE uid=? AND bvid=?`, uid, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var f int64
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFavBvids 某收藏夹内视频（新→旧）。
func (s *Store) ListFavBvids(fid int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT bvid FROM site_favorite_items WHERE fid=? ORDER BY ctime DESC`, fid)
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

// ---- 关注 ----

func (s *Store) SetFollow(uid, mid int64, on bool) error {
	if on {
		_, err := s.DB.Exec(`INSERT OR IGNORE INTO site_follows(uid,mid,ctime) VALUES(?,?,?)`, uid, mid, Now())
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM site_follows WHERE uid=? AND mid=?`, uid, mid)
	return err
}

// CountSiteFollowers 某 UP 主（mid）的站内粉丝数。
func (s *Store) CountSiteFollowers(mid int64) int {
	return s.countInt(`SELECT COUNT(*) FROM site_follows WHERE mid=?`, mid)
}

// ListFollowedMids 用户关注的 mid 集合。
func (s *Store) ListFollowedMids(uid int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT mid FROM site_follows WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- 站内评论（与 B 站评论分表，展示时合并） ----

type SiteComment struct {
	ID       int64  `json:"id"`
	Bvid     string `json:"bvid"`
	UID      int64  `json:"uid"`
	RootID   int64  `json:"root_id"`
	ParentID int64  `json:"parent_id"`
	ToUser   int64  `json:"to_user"`
	Content  string `json:"content"`
	Ctime    int64  `json:"ctime"`
}

func (s *Store) AddSiteComment(uid int64, bvid string, rootID, parentID, toUser int64, content string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO site_comments(bvid,uid,root_id,parent_id,to_user,content,ctime) VALUES(?,?,?,?,?,?,?)`,
		bvid, uid, rootID, parentID, toUser, content, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetSiteComment(id int64) (*SiteComment, error) {
	c := &SiteComment{}
	err := s.DB.QueryRow(`SELECT id,bvid,uid,root_id,parent_id,to_user,content,ctime FROM site_comments WHERE id=?`, id).
		Scan(&c.ID, &c.Bvid, &c.UID, &c.RootID, &c.ParentID, &c.ToUser, &c.Content, &c.Ctime)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListSiteComments 视频的全部站内评论（主楼+楼中楼，旧→新）。
func (s *Store) ListSiteComments(bvid string) ([]*SiteComment, error) {
	rows, err := s.DB.Query(`SELECT id,bvid,uid,root_id,parent_id,to_user,content,ctime FROM site_comments WHERE bvid=? ORDER BY ctime`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SiteComment
	for rows.Next() {
		c := &SiteComment{}
		if err := rows.Scan(&c.ID, &c.Bvid, &c.UID, &c.RootID, &c.ParentID, &c.ToUser, &c.Content, &c.Ctime); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- 站内弹幕（播放页发送，独立于 B 站弹幕 origin） ----

type SiteDanmaku struct {
	ID        int64  `json:"id"`
	Bvid      string `json:"bvid"`
	Cid       int64  `json:"cid"`
	UID       int64  `json:"uid"`
	TimePoint int    `json:"timepoint"`
	Mode      int    `json:"mode"`
	Fontsize  int    `json:"fontsize"`
	Color     int64  `json:"color"`
	Content   string `json:"content"`
	Ctime     int64  `json:"ctime"`
}

func (s *Store) AddSiteDanmaku(d *SiteDanmaku) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO site_danmaku(bvid,cid,uid,timepoint,mode,fontsize,color,content,ctime) VALUES(?,?,?,?,?,?,?,?,?)`,
		d.Bvid, d.Cid, d.UID, d.TimePoint, d.Mode, d.Fontsize, d.Color, d.Content, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListSiteDanmaku 某视频的站内弹幕（与 B 站归档弹幕合并展示）。
func (s *Store) ListSiteDanmaku(bvid string) ([]*SiteDanmaku, error) {
	rows, err := s.DB.Query(`SELECT id,bvid,cid,uid,timepoint,mode,fontsize,color,content,ctime FROM site_danmaku WHERE bvid=? ORDER BY timepoint`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SiteDanmaku
	for rows.Next() {
		d := &SiteDanmaku{}
		if err := rows.Scan(&d.ID, &d.Bvid, &d.Cid, &d.UID, &d.TimePoint, &d.Mode, &d.Fontsize, &d.Color, &d.Content, &d.Ctime); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) countInt(query string, args ...any) int {
	var n int
	_ = s.DB.QueryRow(query, args...).Scan(&n)
	return n
}

// ---- 搜索词库（联想词，来自站内真实使用 + 归档内容派生） ----

// AddSearchWord 记录一次搜索词（全局词库，联想排序按新→旧）。
func (s *Store) AddSearchWord(uid int64, word string) error {
	_, err := s.DB.Exec(`INSERT INTO site_search_words(word,uid,ctime) VALUES(?,?,?)
		ON CONFLICT(word) DO UPDATE SET uid=excluded.uid, ctime=excluded.ctime`, word, uid, Now())
	return err
}

// SearchWordSuggestions 词库 + 归档标题/标签 的前缀/包含联想，去重限 10。
func (s *Store) SearchWordSuggestions(prefix string, limit int) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, limit)
	add := func(w string) {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] || len(out) >= limit {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	rows, err := s.DB.Query(`SELECT word FROM site_search_words WHERE word LIKE ? ORDER BY ctime DESC`, "%"+prefix+"%")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var w string
			if rows.Scan(&w) == nil {
				add(w)
			}
		}
	}
	if bvids, err := s.SearchVideosFTS(prefix, limit); err == nil {
		for _, b := range bvids {
			if v, err := s.GetVideo(b); err == nil {
				add(v.Title)
			}
		}
	}
	if tags, err := s.SearchTagsLike(prefix, limit); err == nil {
		for _, t := range tags {
			add(t)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---- 站长资料（teriteri 账号自助：昵称/签名/性别/头像） ----

type SiteProfile struct {
	UID         int64  `json:"uid"`
	Nickname    string `json:"nickname"`
	Description string `json:"description"`
	Gender      int    `json:"gender"`
	Avatar      string `json:"avatar"`
}

// Profile 站长资料（未设置过返回 nil）。
func (s *Store) Profile(uid int64) (*SiteProfile, error) {
	p := &SiteProfile{UID: uid}
	err := s.DB.QueryRow(`SELECT nickname, description, gender, avatar FROM site_profile WHERE uid=?`, uid).
		Scan(&p.Nickname, &p.Description, &p.Gender, &p.Avatar)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// UpsertProfile 更新昵称/简介/性别。
func (s *Store) UpsertProfile(uid int64, nickname, description string, gender int) error {
	_, err := s.DB.Exec(`INSERT INTO site_profile(uid,nickname,description,gender,avatar) VALUES(?,?,?,?, '')
		ON CONFLICT(uid) DO UPDATE SET nickname=excluded.nickname, description=excluded.description, gender=excluded.gender`,
		uid, nickname, description, gender)
	return err
}

// SetProfileAvatar 记录头像相对路径（dataDir 内）。
func (s *Store) SetProfileAvatar(uid int64, rel string) error {
	_, err := s.DB.Exec(`INSERT INTO site_profile(uid,nickname,description,gender,avatar) VALUES(?,?, '', 2, ?)
		ON CONFLICT(uid) DO UPDATE SET avatar=excluded.avatar`, uid, "", rel)
	return err
}

// DeleteSiteComment 删除站内评论（B 站 rpid 不在表内时无行受影响）。
func (s *Store) DeleteSiteComment(id, uid int64) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM site_comments WHERE id=? AND uid=?`, id, uid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSiteDanmaku 删除站内弹幕。
func (s *Store) DeleteSiteDanmaku(id, uid int64) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM site_danmaku WHERE id=? AND uid=?`, id, uid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SearchTagsLike 标签名模糊匹配（搜索联想用）。
func (s *Store) SearchTagsLike(name string, limit int) ([]string, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT name FROM tags WHERE name LIKE ? LIMIT ?`, "%"+name+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
