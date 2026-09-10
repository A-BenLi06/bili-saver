// Package teriteri 实现 teriteri-client 期望的 REST/WS 契约，
// 数据全部来自本地归档库（origin 层）；已消失的评论按可配置标记显式呈现。
package teriteri

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/images"
	"bili-saver/internal/levels"
	"bili-saver/internal/store"
)

// Adapter 挂载在核心 HTTP mux 上。
type Adapter struct {
	St        *store.Store
	DataDir   string
	AdminUser string
	AdminPass string
	JWTSecret string
	// DeletedMarker 前缀注入已消失评论正文。
	DeletedMarker string
	// PasswordHook 密码修改持久化回调（写回配置文件）；nil 仅改内存。
	PasswordHook func(npw string) error
	Log          *slog.Logger
}

func (a *Adapter) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Register 把适配层路由注册到 mux（Go 1.22 方法+通配模式）。
func (a *Adapter) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/video/getone", a.wrap(a.videoGetone, false))
	mux.HandleFunc("GET /api/video/random/visitor", a.wrap(a.videoRandom, false))
	mux.HandleFunc("GET /api/video/cumulative/visitor", a.wrap(a.videoCumulative, false))
	mux.HandleFunc("POST /api/video/play/visitor", a.wrap(a.okEmpty, false))
	mux.HandleFunc("POST /api/video/play/user", a.wrap(a.okEmpty, false))
	mux.HandleFunc("GET /api/danmu-list/{vid}", a.wrap(a.danmuList, false))
	mux.HandleFunc("GET /api/comment/get", a.wrap(a.commentGet, false))
	mux.HandleFunc("GET /api/comment/reply/get-more", a.wrap(a.commentReplyMore, false))
	mux.HandleFunc("GET /api/comment/get-up-like", a.wrap(a.emptyList, false))
	mux.HandleFunc("GET /api/search/video/only-pass", a.wrap(a.search, false))
	mux.HandleFunc("POST /api/user/account/login", a.wrap(a.login, false))
	mux.HandleFunc("POST /api/user/account/register", a.wrap(a.register, false))
	mux.HandleFunc("GET /api/user/personal/info", a.wrap(a.personalInfo, true))
	mux.HandleFunc("GET /api/user/account/logout", a.wrap(a.okEmpty, false))
	mux.HandleFunc("GET /api/msg-unread/all", a.wrap(a.msgUnread, true))
	mux.HandleFunc("GET /ws/danmu/{vid}", a.wsDanmu)
	a.registerExtras(mux)
	a.registerSite(mux)
}

// env 是 teriteri 的响应包装 {code, message, data}。
type env struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type ctxKey int

type handlerFunc func(w http.ResponseWriter, r *http.Request, uid int64)

// wrap 统一响应头/鉴权。auth=true 时要求有效 JWT（teriteri 用 header message=not login 识别）。
func (a *Adapter) wrap(h handlerFunc, auth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var uid int64
		if tok := bearerToken(r); tok != "" {
			if u, err := a.parseJWT(tok); err == nil {
				uid = u
			}
		}
		if auth && uid == 0 {
			w.Header().Set("Message", "not login")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(env{Code: 400, Message: "not login"})
			return
		}
		h(w, r, uid)
	}
}

func (a *Adapter) ok(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(env{Code: 200, Message: "success", Data: data})
}

func (a *Adapter) fail(w http.ResponseWriter, code int, msg string) {
	_ = json.NewEncoder(w).Encode(env{Code: code, Message: msg})
}

func (a *Adapter) okEmpty(w http.ResponseWriter, _ *http.Request, _ int64) { a.ok(w, true) }
func (a *Adapter) emptyList(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.ok(w, []any{})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// ---- JWT（HS256，仅本服务签发/校验） ----

// SignJWT 导出的签发工具，teriteriadmin 适配层共用同一套 token 体系。
func SignJWT(secret string, uid int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"uid":%d,"exp":%d}`, uid, time.Now().Add(30*24*time.Hour).Unix())))
	signing := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ParseJWT 校验并取回 uid。
func ParseJWT(secret, token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("格式错误")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return 0, fmt.Errorf("签名不符")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, err
	}
	var p struct {
		UID int64 `json:"uid"`
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(pb, &p); err != nil {
		return 0, err
	}
	if p.Exp < time.Now().Unix() {
		return 0, fmt.Errorf("过期")
	}
	return p.UID, nil
}

func (a *Adapter) signJWT(uid int64) string {
	return SignJWT(a.jwtSecret(), uid)
}

func (a *Adapter) parseJWT(tok string) (int64, error) {
	return ParseJWT(a.jwtSecret(), tok)
}

func (a *Adapter) jwtSecret() string {
	if a.JWTSecret != "" {
		return a.JWTSecret
	}
	return "bili-saver-default-jwt-secret"
}

func (a *Adapter) login(w http.ResponseWriter, r *http.Request, _ int64) {
	_ = r.ParseMultipartForm(1 << 20)
	user := firstOf(r, "username", "email")
	pass := firstOf(r, "password")
	if user == "" || pass == "" {
		// 真实 teriteri 前端用 axios 发 JSON（LoginRegister.vue），表单解析不到时回退 JSON
		var body struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if user == "" {
				user = firstStr(body.Username, body.Email)
			}
			if pass == "" {
				pass = body.Password
			}
		}
	}
	if user == "" || pass == "" {
		a.fail(w, 400, "账号或密码不能为空")
		return
	}
	if user != a.AdminUser || pass != a.AdminPass {
		a.fail(w, 400, "账号或密码错误（归档站单用户：见 bili-saver 配置 teriteri.admin_user/admin_pass）")
		return
	}
	// 响应必须带 user 对象：LoginRegister.vue 会 updateUser(result.data.data.user)，
	// 缺失会让 state.user 变 undefined，登录链路在 isLogin=true 之前就抛错中断
	a.ok(w, map[string]any{"token": a.signJWT(1), "user": a.adminUser()})
}

// adminUser 站长资料（登录响应 / personal info 共用）。
func (a *Adapter) adminUser() TUser {
	u := TUser{
		UID: 1, Nickname: a.AdminUser,
		Description: "bili-saver 补档站管理员", Sign: "bili-saver 补档站管理员",
		Gender: 2,
	}
	if p, err := a.St.Profile(1); err == nil {
		if p.Nickname != "" {
			u.Nickname = p.Nickname
		}
		if p.Description != "" {
			u.Description, u.Sign = p.Description, p.Description
		}
		if p.Avatar != "" {
			u.AvatarURL = "/media/" + p.Avatar
		}
		u.Gender = p.Gender
	}
	return u
}

func (a *Adapter) register(w http.ResponseWriter, r *http.Request, _ int64) {
	a.fail(w, 400, "归档站不开放注册，请使用配置中的管理员账号登录")
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstOf(r *http.Request, keys ...string) string {
	for _, k := range keys {
		if v := r.FormValue(k); v != "" {
			return v
		}
	}
	return ""
}

func (a *Adapter) personalInfo(w http.ResponseWriter, _ *http.Request, uid int64) {
	a.ok(w, a.adminUser())
}

func (a *Adapter) msgUnread(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.ok(w, map[string]int{"reply": 0, "at": 0, "love": 0, "system": 0, "whisper": 0, "dynamic": 0})
}

// ---- 视频卡片 / 详情 ----
// TUser 定义见 comments.go（客户端契约：avatar_url + 数字型 vip/auth）。

type tVideoCard struct {
	Vid        string `json:"vid"`
	Title      string `json:"title"`
	CoverUrl   string `json:"coverUrl"`
	Duration   int    `json:"duration"`
	UploadDate string `json:"uploadDate"`
}

type tFeedCard struct {
	Video tVideoCard `json:"video"`
	User  TUser      `json:"user"`
	Stats struct {
		Play  int `json:"play"`
		Danmu int `json:"danmu"`
	} `json:"stats"`
}

// mediaURL 把本地文件转成可播的 /media URL（Path 缺省时用规则路径）。
func (a *Adapter) mediaURL(v *store.Video, page int) string {
	rel := ""
	if page <= 1 && v.LocalPath != "" {
		rel = filepath.ToSlash(v.LocalPath)
	} else {
		rel = fmt.Sprintf("media/%s/p%d/%s-p%d.mp4", v.Bvid, page, v.Bvid, page)
		// p1 也允许回退到库内记录
		if page <= 1 {
			rel = fmt.Sprintf("media/%s/p1/%s-p1.mp4", v.Bvid, v.Bvid)
		}
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, filepath.FromSlash(rel))); err != nil {
		// 文件尚未下载：仍然返回 URL（前端会 404，管理端可触发下载）
		a.log().Debug("媒体文件缺失", "rel", rel)
	}
	return "/media/" + rel
}

// coverURL 本地封面优先：B 站图床对第三方 Referer 返回 403 防盗链，
// 且消失内容的图床文件会下线；缺失时回退 CDN 原始 URL。
func (a *Adapter) coverURL(v *store.Video) string {
	if u := images.CoverURL(a.DataDir, v.Bvid); u != "" {
		return u
	}
	return v.Pic
}

// faceURL 本地头像优先，缺失时回退 CDN 原始 URL。
func (a *Adapter) faceURL(mid int64, cdn string) string {
	if u := images.FaceURL(a.DataDir, mid); u != "" {
		return u
	}
	return cdn
}

func isoDate(sec int64) string {
	return time.Unix(sec, 0).Format("2006-01-02T15:04:05Z07:00")
}

// vid 形如 BV1xx 或 BV1xx-p3。
func splitVid(vid string) (bvid string, page int) {
	if i := strings.LastIndex(vid, "-p"); i > 0 {
		if n, err := strconv.Atoi(vid[i+2:]); err == nil && n > 0 {
			return vid[:i], n
		}
	}
	return vid, 1
}

func (a *Adapter) videoGetone(w http.ResponseWriter, r *http.Request, _ int64) {
	vid := r.FormValue("vid")
	if vid == "" {
		a.fail(w, 400, "缺少 vid")
		return
	}
	bvid, page := splitVid(vid)
	v, err := a.St.GetVideo(bvid)
	if err != nil {
		a.fail(w, 404, "视频不存在或未归档")
		return
	}
	pages, _ := a.St.ListPages(bvid)
	var cur *store.Page
	for _, p := range pages {
		if p.Page == page {
			cur = p
			break
		}
	}
	duration := v.Duration
	if cur != nil && cur.Duration > 0 {
		duration = cur.Duration
	}
	tagsBili, _ := a.St.VideoTags(bvid)
	tags := append([]string{}, tagsBili["bilibili"]...)
	descr := v.Description
	// 官方 AI 小结（B 站原生）附加在简介尾部，显式标注来源
	if model, summary, _, err := a.St.GetAIConclusion(bvid); err == nil && summary != "" {
		descr += fmt.Sprintf("\n\n—— B站官方AI小结（%s）——\n%s", model, summary)
	}

	play, danmu, good, coin, collect, share, comment := a.statsOf(bvid)

	a.ok(w, map[string]any{
		"video": map[string]any{
			"vid": vid, "title": v.Title, "descr": descr,
			"videoUrl": a.mediaURL(v, page),
			"coverUrl": a.coverURL(v),
			"duration": duration, "uploadDate": isoDate(v.Pubdate),
			"tags": strings.Join(tags, "\r\n"),
			"type": 1, "auth": 1, "top": false,
		},
		"user": TUser{
			UID: v.OwnerMid, Nickname: v.OwnerName, AvatarURL: a.faceURL(v.OwnerMid, v.OwnerFace),
			Exp: levels.LevelExp(v.OwnerLevel), FansCount: 0, Auth: 0,
		},
		"category": map[string]any{"mcId": 0, "mcName": "补档", "scId": 0, "scName": "归档"},
		"stats": map[string]any{
			"play": play, "danmu": danmu, "good": good,
			"coin": coin, "collect": collect, "share": share, "comment": comment,
		},
	})
}

// statsOf 返回最新一次统计快照与库内计数。
func (a *Adapter) statsOf(bvid string) (play, danmu, good, coin, collect, share, comment int) {
	if hist, err := a.St.ListStats(bvid); err == nil && len(hist) > 0 {
		last := hist[len(hist)-1]
		play, danmu, good, coin, collect, share = last.View, last.Danmaku, last.Like, last.Coin, last.Favorite, last.Share
	}
	vis, del, err := a.St.CountCommentsByStatus(bvid)
	if err == nil {
		comment = vis + del // 补档站展示“现存+已消失”总数
	}
	// 站内互动叠加（site_* 表，社区层）：点赞/投币/收藏 = B站快照 + 站内
	good += a.St.CountVideoLikes(bvid)
	coin += a.St.CountVideoCoins(bvid)
	if folders, err := a.St.ListFavFolders(1); err == nil {
		for _, f := range folders {
			if bvids, err := a.St.ListFavBvids(f.Fid); err == nil {
				for _, b := range bvids {
					if b == bvid {
						collect++
						break
					}
				}
			}
		}
	}
	return
}

func (a *Adapter) card(v *store.Video) tFeedCard {
	c := tFeedCard{
		Video: tVideoCard{
			Vid: v.Bvid, Title: v.Title, CoverUrl: a.coverURL(v),
			Duration: v.Duration, UploadDate: isoDate(v.Pubdate),
		},
		User: TUser{UID: v.OwnerMid, Nickname: v.OwnerName, AvatarURL: a.faceURL(v.OwnerMid, v.OwnerFace),
			Exp: levels.LevelExp(v.OwnerLevel)},
	}
	play, danmu, _, _, _, _, _ := a.statsOf(v.Bvid)
	c.Stats.Play, c.Stats.Danmu = play, danmu
	return c
}

// randomVideos 随机取样（排除 vids）。
func (a *Adapter) randomVideos(exclude map[string]bool, n int) []tFeedCard {
	videos, err := a.St.ListVideos(0, "ok", 0, 0)
	if err != nil {
		return nil
	}
	var pool []*store.Video
	for _, v := range videos {
		if !exclude[v.Bvid] && v.Status == "ok" {
			pool = append(pool, v)
		}
	}
	rnd := shuffle(len(pool))
	out := make([]tFeedCard, 0, n)
	for _, idx := range rnd {
		if len(out) >= n {
			break
		}
		out = append(out, a.card(pool[idx]))
	}
	return out
}

func (a *Adapter) videoRandom(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.ok(w, a.randomVideos(nil, 11))
}

func (a *Adapter) videoCumulative(w http.ResponseWriter, r *http.Request, _ int64) {
	// 带_vid 参数时是播放页的播放计数上报；否则是首页累加推荐
	if vid := r.FormValue("vid"); vid != "" && !strings.Contains(vid, ",") {
		bvid, _ := splitVid(vid)
		play, _, _, _, _, _, _ := a.statsOf(bvid)
		a.ok(w, map[string]any{"play": play + 1})
		return
	}
	exclude := map[string]bool{}
	if vs := r.FormValue("vids"); vs != "" {
		for _, s := range strings.Split(vs, ",") {
			exclude[strings.TrimSpace(s)] = true
		}
	}
	cards := a.randomVideos(exclude, 20)
	vids := make([]string, 0, len(cards))
	for _, c := range cards {
		vids = append(vids, c.Video.Vid)
	}
	a.ok(w, map[string]any{"videos": cards, "vids": vids, "more": true})
}

// clientKeyword teriteri-client 搜索前会自行 encodeURIComponent 一次，
// axios 参数序列化又编码一次；net/http 只解一层，这里补解第二层。
// 本身含 % 的合法关键词解不出合法 %XX 序列时原样返回，不受影响。
func clientKeyword(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// searchBvids 标题/简介 FTS 优先，命不中时回退标签匹配（热搜词全部来自标签）。
func (a *Adapter) searchBvids(q string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, limit)
	if bvids, err := a.St.SearchVideosFTS(q, limit); err == nil {
		for _, b := range bvids {
			if !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	if len(out) < limit {
		if bvids, err := a.St.SearchVideosByTag(q, limit); err == nil {
			for _, b := range bvids {
				if !seen[b] {
					seen[b] = true
					out = append(out, b)
				}
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a *Adapter) search(w http.ResponseWriter, r *http.Request, _ int64) {
	q := strings.TrimSpace(clientKeyword(r.FormValue("keyword")))
	if q == "" {
		a.ok(w, []tFeedCard{})
		return
	}
	bvids := a.searchBvids(q, 50)
	out := make([]tFeedCard, 0, len(bvids))
	for _, b := range bvids {
		if v, err := a.St.GetVideo(b); err == nil {
			out = append(out, a.card(v))
		}
	}
	a.ok(w, out)
}
