package teriteri

// 弹幕、评论（含已消失评论显式标记）、WebSocket 占位实现。

import (
	"crypto/sha1"
	"encoding/base64"
	"math/rand"
	"net/http"
	"sort"
	"strconv"

	"bili-saver/internal/levels"
	"bili-saver/internal/store"
)

// wsAccept 计算 RFC6455 Sec-WebSocket-Accept。
func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// shuffle 返回 0..n-1 的随机排列。
func shuffle(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	rand.Shuffle(n, func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	return idx
}

// danmuItem 对齐 teriteri-client 的弹幕对象字段。
type danmuItem struct {
	ID         int64  `json:"id"`
	UID        int64  `json:"uid"`
	TimePoint  int    `json:"timePoint"`
	Mode       int    `json:"mode"`
	Fontsize   int    `json:"fontsize"`
	Color      int64  `json:"color"`
	Content    string `json:"content"`
	CreateDate string `json:"createDate"`
	State      int    `json:"state"`
	Active     bool   `json:"active"`
}

func (a *Adapter) danmuList(w http.ResponseWriter, r *http.Request, _ int64) {
	vid := r.PathValue("vid")
	bvid, page := splitVid(vid)
	pages, _ := a.St.ListPages(bvid)
	var cid int64
	for _, p := range pages {
		if p.Page == page {
			cid = p.Cid
			break
		}
	}
	if cid == 0 {
		a.ok(w, []danmuItem{})
		return
	}
	rows, err := a.St.ListDanmaku(cid, "visible")
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	out := make([]danmuItem, 0, len(rows)+8)
	for _, d := range rows {
		out = append(out, danmuItem{
			ID: d.Dmid, UID: 0,
			TimePoint: d.Timepoint / 1000, // teriteri 前端按秒计算
			Mode:      d.Mode, Fontsize: d.Fontsize, Color: d.Color,
			Content: d.Content, CreateDate: isoDate(d.Ctime), State: 0, Active: true,
		})
	}
	// 站内弹幕（播放页发送，site_danmaku）与 B 站归档弹幕合并重现
	if site, err := a.St.ListSiteDanmaku(bvid); err == nil {
		for _, d := range site {
			out = append(out, danmuItem{
				ID: d.ID, UID: d.UID, TimePoint: d.TimePoint,
				Mode: d.Mode, Fontsize: d.Fontsize, Color: d.Color,
				Content: d.Content, CreateDate: isoDate(d.Ctime), State: 0, Active: true,
			})
		}
	}
	a.ok(w, out)
}

// TUser 对齐 teriteri-client 的用户对象契约：组件按 avatar_url 读取头像、
// vip/auth 是数字（0无/1个人/2机构认证），字段名或类型不一致会让渲染 effect
// 抛错、整块 DOM 冻结在加载态。
type TUser struct {
	UID          int64  `json:"uid"`
	Nickname     string `json:"nickname"`
	AvatarURL    string `json:"avatar_url"`
	BgURL        string `json:"bg_url"`
	Description  string `json:"description"`
	Sign         string `json:"sign"`
	Gender       int    `json:"gender"` // 0女 1男 2保密
	Exp          int    `json:"exp"`
	Vip          int    `json:"vip"`  // 0无 1月度 2季度 3年度
	Auth         int    `json:"auth"` // 0无 1个人认证 2机构认证
	AuthMsg      string `json:"authMsg"`
	FansCount    int    `json:"fansCount"`
	FollowsCount int    `json:"followsCount"`
	LoveCount    int    `json:"loveCount"`
	PlayCount    int    `json:"playCount,omitempty"`
	WorksCount   int    `json:"worksCount,omitempty"`
}

// TComment 对齐 teriteri CommentTree 的评论对象；toUser/parentId 为楼中楼
// “回复 @某人”所需，归档未存被回复人信息，统一按直接回复主楼呈现。
type TComment struct {
	ID         int64      `json:"id"`
	Vid        string     `json:"vid"`
	RootID     int64      `json:"rootId"`
	ParentID   int64      `json:"parentId"`
	Content    string     `json:"content"`
	Love       int        `json:"love"`
	CreateTime string     `json:"createTime"`
	Count      int        `json:"count"`
	Bad        int        `json:"bad"`
	Replies    []TComment `json:"replies"`
	ToUser     TUser      `json:"toUser"`
	User       TUser      `json:"user"`
}

const defaultDeletedMarker = "【已消失·仅存档】"

func (a *Adapter) deletedMarker() string {
	if a.DeletedMarker != "" {
		return a.DeletedMarker
	}
	return defaultDeletedMarker
}

func (a *Adapter) toTComment(c *store.Comment, subs map[int64][]*store.Comment) TComment {
	content := c.Content
	nickname := c.MemberName
	if c.Status == "deleted" {
		content = a.deletedMarker() + " " + content
		if nickname == "" {
			nickname = "已消失的用户"
		}
	}
	rootID := c.RootRpid
	// 楼中楼一律按“直接回复主楼”呈现：被回复人（toUser）未归档，
	// parentId 与 rootId 保持一致可让前端跳过“回复 @某人”分支。
	parentID := c.ParentRpid
	if rootID != 0 && (parentID == 0 || parentID != rootID) {
		parentID = rootID
	}
	tc := TComment{
		ID: c.Rpid, Vid: c.Bvid, RootID: rootID, ParentID: parentID,
		Content: content, Love: c.LikeCount + a.St.CountCommentLikes(c.Rpid),
		CreateTime: isoDate(c.Ctime),
		Replies:    []TComment{},
		ToUser:     TUser{},
		User: TUser{
			UID: c.MemberMid, Nickname: nickname, AvatarURL: a.faceURL(c.MemberMid, c.MemberFace),
			Exp: levels.LevelExp(c.MemberLevel), // 前端按 exp 渲染等级徽章（VLevel）
		},
	}
	if subs != nil {
		for _, s := range subs[c.Rpid] {
			tc.Replies = append(tc.Replies, a.toTComment(s, nil))
		}
		tc.Count = len(tc.Replies)
	}
	return tc
}

// segSlice 计算全局窗口 [start,stop) 与段 [base,base+segLen) 的交集，
// 返回段内局部下标区间与是否命中（评论区合并分页用）。
func segSlice(start, stop, base, segLen int64) (int, int, bool) {
	lo, hi := start, stop
	if lo < base {
		lo = base
	}
	if hi > base+segLen {
		hi = base + segLen
	}
	if lo >= hi {
		return 0, 0, false
	}
	return int(lo - base), int(hi - base), true
}

// commentGet 翻页返回主楼评论。teriteri 语义: offset=已加载条数, type=1最热/2最新。
// 主楼走 SQL 分页、楼中楼只查页内主楼——原实现把整视频评论（含 raw_json）
// 全量加载两遍再内存排序切片，热门视频每次翻页搬运数万行。
// 合并序列与原实现一致：type=1 为 [B站主楼(现存优先→赞降→ctime)] ++ [站内主楼(旧→新)]，
// type=2 站内在前。
func (a *Adapter) commentGet(w http.ResponseWriter, r *http.Request, _ int64) {
	vid := r.FormValue("vid")
	offset, _ := strconv.Atoi(r.FormValue("offset"))
	typ, _ := strconv.Atoi(r.FormValue("type"))
	if typ == 0 {
		typ = 1
	}
	if offset < 0 {
		offset = 0
	}
	bvid, _ := splitVid(vid)

	// 站内评论（社区层，量小整载）：主楼序列 + 按主楼 id 分组的楼中楼
	siteByRoot := map[int64][]*store.SiteComment{}
	var siteMains []*store.SiteComment
	if scs, err := a.St.ListSiteComments(bvid); err == nil {
		for _, c := range scs {
			if c.RootID != 0 {
				siteByRoot[c.RootID] = append(siteByRoot[c.RootID], c)
				continue
			}
			siteMains = append(siteMains, c)
		}
	}

	biliTotal, err := a.St.CountMainComments(bvid)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	const pageSize = 20
	stop := offset + pageSize
	total := int64(len(siteMains)) + biliTotal
	more := int64(stop) < total

	// B 站主楼段在合并序列中的基座：type=2 时站内段在前
	siteLen := int64(len(siteMains))
	biliBase := int64(0)
	if typ == 2 {
		biliBase = siteLen
	}
	bLo, bHi, biliOnPage := segSlice(int64(offset), int64(stop), biliBase, biliTotal)

	var mains []*store.Comment
	subs := map[int64][]*store.Comment{}
	if biliOnPage {
		sortKey := "hot"
		if typ == 2 {
			sortKey = "new"
		}
		mains, err = a.St.ListMainCommentsPage(bvid, sortKey, bHi-bLo, bLo)
		if err != nil {
			a.fail(w, 500, err.Error())
			return
		}
		roots := make([]int64, 0, len(mains))
		for _, m := range mains {
			roots = append(roots, m.Rpid)
		}
		if subs, err = a.St.ListSubCommentsByRoots(bvid, roots); err != nil {
			subs = map[int64][]*store.Comment{}
		}
	}

	pageComments := []TComment{}
	buildBili := func(i int) TComment {
		tc := a.toTComment(mains[i], subs)
		for _, sub := range siteByRoot[mains[i].Rpid] {
			tc.Replies = append(tc.Replies, a.siteCommentToT(sub))
		}
		tc.Count = len(tc.Replies)
		return tc
	}
	buildSite := func(i int) TComment {
		tc := a.siteCommentToT(siteMains[i])
		for _, sub := range siteByRoot[siteMains[i].ID] {
			tc.Replies = append(tc.Replies, a.siteCommentToT(sub))
		}
		tc.Count = len(tc.Replies)
		return tc
	}

	// 按合并序列顺序填页（段与段相邻，一页最多跨一次边界）
	fill := func(base, segLen int64, build func(i int) TComment) {
		if l, h, ok := segSlice(int64(offset), int64(stop), base, segLen); ok {
			for i := l; i < h; i++ {
				pageComments = append(pageComments, build(i))
			}
		}
	}
	if typ == 2 {
		fill(0, siteLen, buildSite)
		fill(siteLen, biliTotal, buildBili)
	} else {
		fill(0, biliTotal, buildBili)
		fill(biliTotal, siteLen, buildSite)
	}
	a.ok(w, map[string]any{"more": more, "comments": pageComments})
}

// commentReplyMore 楼中楼展开：?id={rootId} 返回该楼全部子评论。
func (a *Adapter) commentReplyMore(w http.ResponseWriter, r *http.Request, _ int64) {
	rootID, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if rootID == 0 {
		a.ok(w, []TComment{})
		return
	}
	// 通过 rpid 找到其 bvid
	var bvid string
	if err := a.St.DB.QueryRow(`SELECT bvid FROM comments WHERE rpid=?`, rootID).Scan(&bvid); err != nil {
		// 可能是站内评论主楼：返回其站内楼中楼
		if sc, err := a.St.GetSiteComment(rootID); err == nil {
			out := make([]TComment, 0, 4)
			if scs, err := a.St.ListSiteComments(sc.Bvid); err == nil {
				for _, c := range scs {
					if c.RootID == rootID {
						out = append(out, a.siteCommentToT(c))
					}
				}
			}
			a.ok(w, out)
			return
		}
		a.ok(w, []TComment{})
		return
	}
	subs, err := a.St.ListSubComments(bvid, rootID)
	if err != nil {
		a.ok(w, []TComment{})
		return
	}
	out := make([]TComment, 0, len(subs)+4)
	for _, s := range subs {
		out = append(out, a.toTComment(s, nil))
	}
	// B站主楼下的站内楼中楼也一并提供
	if scs, err := a.St.ListSiteComments(bvid); err == nil {
		for _, c := range scs {
			if c.RootID == rootID {
				out = append(out, a.siteCommentToT(c))
			}
		}
	}
	a.ok(w, out)
}
