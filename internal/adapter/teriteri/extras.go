// extras.go 补齐 teriteri-client 会调用的其余只读端点：
// 热搜/搜索计数、登录态收藏与评论赞踩、UP主空间（真实归档数据）、消息页空数据占位。
// 原则：一律返回合法空集合而非 404——前端 axios 拦截器对任何错误响应都会弹
// “特丽丽被玩坏了” toast，且 App.vue 启动链路（收藏夹→赞踩→频道/热搜）一处失败
// 会中断后续初始化，因此这些端点必须始终返回 code=200。
package teriteri

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"bili-saver/internal/levels"
	"bili-saver/internal/store"
)

// registerExtras 注册补齐端点（登录态端点照常走 JWT 校验，未登录由前端自行处理提示）。
func (a *Adapter) registerExtras(mux *http.ServeMux) {
	// 首页/搜索
	mux.HandleFunc("GET /api/category/getall", a.wrap(a.categoryGetall, false))
	mux.HandleFunc("GET /api/search/hot/get", a.wrap(a.searchHot, false))
	mux.HandleFunc("GET /api/search/count", a.wrap(a.searchCount, false))

	// 登录态：收藏夹与评论赞踩（App.vue 启动链路依赖）
	mux.HandleFunc("POST /api/msg-unread/clear", a.wrap(a.okEmpty, true))

	// 消息页：归档站无私信，空数据占位避免报错弹窗
	mux.HandleFunc("GET /api/msg/chat/recent-list", a.wrap(a.emptyList, true))
	mux.HandleFunc("GET /api/msg/chat/create/{mid}", a.wrap(a.okEmpty, true))
	mux.HandleFunc("GET /api/msg/chat/delete/{uid}", a.wrap(a.okEmpty, true))
	mux.HandleFunc("GET /api/msg/chat/online", a.wrap(a.okEmpty, true))
	mux.HandleFunc("GET /api/msg/chat/outline", a.wrap(a.okEmpty, true))
	mux.HandleFunc("GET /api/msg/chat-detailed/get-more", a.wrap(a.emptyList, true))
	mux.HandleFunc("POST /api/msg/chat-detailed/delete", a.wrap(a.okEmpty, true))

	// UP主空间：用归档库里的真实数据呈现
	mux.HandleFunc("GET /api/user/info/get-one", a.wrap(a.userInfoGetone, false))
	mux.HandleFunc("GET /api/video/user-works", a.wrap(a.userWorks, false))
	mux.HandleFunc("GET /api/video/user-works-count", a.wrap(a.userWorksCount, false))
	// 点赞/收藏列表原站需登录，但归档站放开为空集合：guest 401 会让
	// SpaceHome.getLoveVideos 在 undefined 上读 .data 抛未捕获异常

	// IM WebSocket：App.vue 登录态启动会阻塞等待 /im 连接 open，必须可握手
	mux.HandleFunc("GET /im", a.wsDanmu)
}

// categoryGetall 返回空分区列表：频道栏组件有 v-if 守卫会整体隐藏，
// 但绝不能返回 null（前端对 null 取 length 会崩掉整棵渲染树）。
func (a *Adapter) categoryGetall(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.emptyList(w, nil, 0)
}

// searchHot 用归档库的高频 B 站原生标签充当热搜词，点击即走站内搜索。
func (a *Adapter) searchHot(w http.ResponseWriter, _ *http.Request, _ int64) {
	videos, err := a.St.ListVideos(0, "ok", 0, 0)
	if err != nil {
		a.ok(w, []any{})
		return
	}
	counts := map[string]int{}
	for _, v := range videos {
		tags, err := a.St.VideoTags(v.Bvid)
		if err != nil {
			continue
		}
		for _, t := range tags["bilibili"] {
			if t = strings.TrimSpace(t); t != "" {
				counts[t]++
			}
		}
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(counts))
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	const topN = 10
	if len(list) > topN {
		list = list[:topN]
	}
	out := make([]map[string]any, 0, len(list))
	for i, e := range list {
		typ := 0
		if i < 2 {
			typ = 2 // 前两名带“热”角标
		}
		out = append(out, map[string]any{"content": e.k, "type": typ})
	}
	a.ok(w, out)
}

// searchCount 返回 [视频命中数, 用户命中数]；归档站无站内用户，恒为 0。
func (a *Adapter) searchCount(w http.ResponseWriter, r *http.Request, _ int64) {
	q := strings.TrimSpace(clientKeyword(r.FormValue("keyword")))
	n := 0
	if q != "" {
		n = len(a.searchBvids(q, 1000))
	}
	a.ok(w, []int{n, 0})
}

// likeDislike 归档站不记录用户的赞踩行为，恒为空集合。
func (a *Adapter) likeDislike(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.ok(w, map[string]any{"userLike": []any{}, "userDislike": []any{}})
}

// spaceUser 对齐 teriteri-client SpaceView 所需的用户对象字段（vip/auth 为数字）。
func (a *Adapter) spaceUser(uid int64, nickname, avatar, descr string, level, works, playSum, likeSum int) map[string]any {
	return map[string]any{
		"uid": uid, "state": 0, "auth": 0, "authMsg": "",
		"nickname": nickname, "avatar_url": avatar, "description": descr,
		"bg_url": "", "gender": 2, "exp": levels.LevelExp(level), "vip": 0,
		"worksCount": works, "fansCount": 0, "followsCount": 0,
		"playCount": playSum, "loveCount": likeSum,
	}
}

// videosByOwner 汇总某 UP 主的全部归档视频与累计播放/点赞。
func (a *Adapter) videosByOwner(mid int64) ([]*store.Video, int, int) {
	videos, err := a.St.ListVideos(mid, "", 0, 0)
	if err != nil {
		return nil, 0, 0
	}
	playSum, likeSum := 0, 0
	for _, v := range videos {
		play, _, _, _, _, _, _ := a.statsOf(v.Bvid)
		playSum += play
		if hist, err := a.St.ListStats(v.Bvid); err == nil && len(hist) > 0 {
			likeSum += hist[len(hist)-1].Like
		}
	}
	return videos, playSum, likeSum
}

// userInfoGetone UP主空间资料：取该 mid 最新一条归档的身份信息合成；
// 站内管理员（JWT uid=1）也能呈现自己的空间。
func (a *Adapter) userInfoGetone(w http.ResponseWriter, r *http.Request, _ int64) {
	mid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	if mid == 0 {
		a.fail(w, 400, "缺少 uid")
		return
	}
	videos, playSum, likeSum := a.videosByOwner(mid)
	if len(videos) == 0 {
		if mid == 1 { // 站长（无投稿）
			a.ok(w, a.spaceUser(1, a.AdminUser, "", "bili-saver 补档站管理员", 0, 0, 0, 0))
			return
		}
		a.fail(w, 404, "用户不存在")
		return
	}
	newest := videos[0]
	for _, v := range videos[1:] {
		if v.Pubdate > newest.Pubdate {
			newest = v
		}
	}
	a.ok(w, a.spaceUser(mid, newest.OwnerName, a.faceURL(newest.OwnerMid, newest.OwnerFace), "",
		newest.OwnerLevel, len(videos), playSum, likeSum))
}

// userWorks UP主空间投稿列表：?uid=&rule=(1最新发布/2最多播放/3最多点赞)&page=&quantity=
// 返回 {count, list:[feed卡片]}，与首页推荐卡片同构。rule 语义以客户端
// SpaceVideo.vue 的 tab 顺序为准。
func (a *Adapter) userWorks(w http.ResponseWriter, r *http.Request, _ int64) {
	mid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	rule, _ := strconv.Atoi(r.FormValue("rule"))
	page, _ := strconv.Atoi(r.FormValue("page"))
	quantity, _ := strconv.Atoi(r.FormValue("quantity"))
	if page < 1 {
		page = 1
	}
	if quantity < 1 || quantity > 50 {
		quantity = 30
	}
	videos, _, _ := a.videosByOwner(mid)
	type row struct {
		v    *store.Video
		play int
		like int
	}
	rows := make([]row, 0, len(videos))
	for _, v := range videos {
		play, _, like, _, _, _, _ := a.statsOf(v.Bvid) // good 即点赞
		rows = append(rows, row{v, play, like})
	}
	switch rule {
	case 2: // 最多播放
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].play > rows[j].play })
	case 3: // 最多点赞
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].like > rows[j].like })
	default: // rule 1：最新发布
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].v.Pubdate > rows[j].v.Pubdate })
	}
	start := (page - 1) * quantity
	if start > len(rows) {
		start = len(rows)
	}
	end := start + quantity
	if end > len(rows) {
		end = len(rows)
	}
	list := make([]tFeedCard, 0, end-start)
	for _, rw := range rows[start:end] {
		list = append(list, a.card(rw.v))
	}
	a.ok(w, map[string]any{"count": len(rows), "list": list})
}

// userWorksCount 投稿数（SpaceView 顶部 tab）。
func (a *Adapter) userWorksCount(w http.ResponseWriter, r *http.Request, _ int64) {
	mid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	videos, _, _ := a.videosByOwner(mid)
	a.ok(w, len(videos))
}
