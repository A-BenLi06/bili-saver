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

// groupSubReplies 一次查询全评论并按主楼分组（避免每楼一次全表扫描的 N+1）。
func (a *Adapter) groupSubReplies(bvid string) (map[int64][]*store.Comment, error) {
	all, err := a.St.ListComments(bvid, "", 0, 0)
	if err != nil {
		return nil, err
	}
	groups := map[int64][]*store.Comment{}
	for _, c := range all {
		if c.RootRpid != 0 && c.Rpid != c.RootRpid {
			groups[c.RootRpid] = append(groups[c.RootRpid], c)
		}
	}
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].Ctime < g[j].Ctime })
	}
	return groups, nil
}

// commentGet 翻页返回主楼评论。teriteri 语义: offset=已加载条数, type=1最热/2最新。
func (a *Adapter) commentGet(w http.ResponseWriter, r *http.Request, _ int64) {
	vid := r.FormValue("vid")
	offset, _ := strconv.Atoi(r.FormValue("offset"))
	typ, _ := strconv.Atoi(r.FormValue("type"))
	if typ == 0 {
		typ = 1
	}
	bvid, _ := splitVid(vid)
	all, err := a.St.ListComments(bvid, "", 0, 0)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	subs, _ := a.groupSubReplies(bvid)
	// 仅主楼
	var mains []*store.Comment
	for _, c := range all {
		if c.RootRpid == 0 {
			mains = append(mains, c)
		}
	}
	if typ == 1 {
		sort.SliceStable(mains, func(i, j int) bool {
			if mains[i].Status != mains[j].Status {
				return mains[i].Status == "visible" // 现存评论优先
			}
			return mains[i].LikeCount > mains[j].LikeCount
		})
	} else {
		sort.SliceStable(mains, func(i, j int) bool { return mains[i].Ctime > mains[j].Ctime })
	}
	biliTcs := make([]TComment, 0, len(mains))
	for _, c := range mains {
		biliTcs = append(biliTcs, a.toTComment(c, subs))
	}

	// 站内评论合并（site_comments）：楼中楼挂到对应主楼（含 B 站主楼），
	// 主楼排序——最新(type=2)站内新评论在前；最热(type=1)追加在 B 站评论后
	siteByRoot := map[int64][]*store.SiteComment{}
	if scs, err := a.St.ListSiteComments(bvid); err == nil {
		var siteMains []TComment
		for _, c := range scs {
			if c.RootID != 0 {
				siteByRoot[c.RootID] = append(siteByRoot[c.RootID], c)
				continue
			}
			siteMains = append(siteMains, a.siteCommentToT(c))
		}
		for i := range biliTcs {
			for _, sub := range siteByRoot[biliTcs[i].ID] {
				biliTcs[i].Replies = append(biliTcs[i].Replies, a.siteCommentToT(sub))
			}
			biliTcs[i].Count = len(biliTcs[i].Replies)
		}
		// 站内主楼的楼中楼
		for i := range siteMains {
			for _, sub := range siteByRoot[siteMains[i].ID] {
				siteMains[i].Replies = append(siteMains[i].Replies, a.siteCommentToT(sub))
			}
			siteMains[i].Count = len(siteMains[i].Replies)
		}
		if typ == 2 {
			biliTcs = append(siteMains, biliTcs...)
		} else {
			biliTcs = append(biliTcs, siteMains...)
		}
	}

	const pageSize = 20
	end := offset + pageSize
	if end > len(biliTcs) {
		end = len(biliTcs)
	}
	pageComments := []TComment{}
	if offset < end {
		pageComments = append(pageComments, biliTcs[offset:end]...)
	}
	a.ok(w, map[string]any{"more": end < len(biliTcs), "comments": pageComments})
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
	groups, err := a.groupSubReplies(bvid)
	if err != nil {
		a.ok(w, []TComment{})
		return
	}
	out := make([]TComment, 0, len(groups[rootID])+4)
	for _, s := range groups[rootID] {
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
