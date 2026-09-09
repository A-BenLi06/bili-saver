// Package teriteriadmin 实现 teriteri-admin（管理员端）期望的 REST 契约。
// 该项目主要暴露视频审核工作台：登录/个人资料复用站长单用户体系，
// 审核列表与详情直接映射归档库（ok=已过审，gone/private=封禁/删源），
// 审核状态变更映射为视频的展示状态（private 即从播放站隐藏）。
package teriteriadmin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/adapter/teriteri"
	"bili-saver/internal/store"
)

// Adapter 挂载在核心 HTTP mux 上（与用户端适配层共享 JWT）。
type Adapter struct {
	St        *store.Store
	DataDir   string
	AdminUser string
	AdminPass string
	JWTSecret string
	Log       *slog.Logger
}

func (a *Adapter) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

func (a *Adapter) jwtSecret() string {
	if a.JWTSecret != "" {
		return a.JWTSecret
	}
	return "bili-saver-default-jwt-secret"
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, uid int64)

// env 与用户端适配层相同的 {code, message, data} 包装。
type env struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func (a *Adapter) ok(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(env{Code: 200, Message: "success", Data: data})
}

func (a *Adapter) fail(w http.ResponseWriter, code int, msg string) {
	_ = json.NewEncoder(w).Encode(env{Code: code, Message: msg})
}

func (a *Adapter) okEmpty(w http.ResponseWriter, _ *http.Request, _ int64) { a.ok(w, true) }

func (a *Adapter) wrap(h handlerFunc, auth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var uid int64
		if tk := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); tk != "" {
			if u, err := teriteri.ParseJWT(a.jwtSecret(), tk); err == nil {
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

// Register 管理端路由（category/danmu-list 等与用户端共用的端点由 teriteri 包注册）。
func (a *Adapter) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/account/login", a.wrap(a.login, false))
	mux.HandleFunc("GET /api/admin/account/logout", a.wrap(a.okEmpty, false))
	mux.HandleFunc("GET /api/admin/personal/info", a.wrap(a.personalInfo, true))
	mux.HandleFunc("GET /api/review/video/getpage", a.wrap(a.videoPage, true))
	mux.HandleFunc("GET /api/review/video/total", a.wrap(a.videoTotal, true))
	mux.HandleFunc("GET /api/review/video/getone", a.wrap(a.videoGetone, true))
	mux.HandleFunc("POST /api/video/change/status", a.wrap(a.changeStatus, true))
}

// ---- 登录 / 资料 ----

func (a *Adapter) adminUser() teriteri.TUser {
	return teriteri.TUser{
		UID: 1, Nickname: a.AdminUser,
		Description: "bili-saver 补档站管理员", Sign: "bili-saver 补档站管理员",
		Gender: 2,
	}
}

func (a *Adapter) login(w http.ResponseWriter, r *http.Request, _ int64) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Username == "" || body.Password == "" {
		a.fail(w, 400, "账号或密码不能为空")
		return
	}
	if body.Username != a.AdminUser || body.Password != a.AdminPass {
		a.fail(w, 400, "用户名或密码错误")
		return
	}
	a.ok(w, map[string]any{"token": teriteri.SignJWT(a.jwtSecret(), 1), "user": a.adminUser()})
}

func (a *Adapter) personalInfo(w http.ResponseWriter, _ *http.Request, _ int64) {
	a.ok(w, a.adminUser())
}

// ---- 视频审核 ----

// reviewStatus 归档状态映射审核状态：ok=1 已过审；其余=2（未过审/已删源）。
// 归档站没有“待审核”概念，vstatus=0 恒为空。
func reviewStatus(v *store.Video) int {
	if v.Status == "ok" {
		return 1
	}
	return 2
}

// card 审核列表行：视频卡片 + 整型 status + 分区。
func (a *Adapter) card(v *store.Video) map[string]any {
	play, danmu := 0, 0
	if hist, err := a.St.ListStats(v.Bvid); err == nil && len(hist) > 0 {
		last := hist[len(hist)-1]
		play, danmu = last.View, last.Danmaku
	}
	return map[string]any{
		"video": map[string]any{
			"vid": v.Bvid, "title": v.Title, "coverUrl": a.coverURL(v),
			"duration": v.Duration, "uploadDate": isoDate(v.Pubdate),
			"status": reviewStatus(v),
		},
		"user":     map[string]any{"uid": v.OwnerMid, "nickname": v.OwnerName, "avatar_url": v.OwnerFace},
		"category": map[string]any{"mcId": 0, "mcName": "补档", "scId": 0, "scName": "归档"},
		"stats":    map[string]any{"play": play, "danmu": danmu},
	}
}

func isoDate(sec int64) string {
	return time.Unix(sec, 0).Format("2006-01-02T15:04:05Z07:00")
}

// videoPage ?vstatus=(0待审/1过审/2未过审/3封禁删源)&page=&quantity=
func (a *Adapter) videoPage(w http.ResponseWriter, r *http.Request, _ int64) {
	vstatus, _ := strconv.Atoi(r.FormValue("vstatus"))
	page, _ := strconv.Atoi(r.FormValue("page"))
	quantity, _ := strconv.Atoi(r.FormValue("quantity"))
	if page < 1 {
		page = 1
	}
	if quantity < 1 || quantity > 100 {
		quantity = 10
	}
	videos := a.byVstatus(vstatus)
	start := (page - 1) * quantity
	if start > len(videos) {
		start = len(videos)
	}
	end := start + quantity
	if end > len(videos) {
		end = len(videos)
	}
	out := make([]map[string]any, 0, end-start)
	for _, v := range videos[start:end] {
		out = append(out, a.card(v))
	}
	a.ok(w, out)
}

func (a *Adapter) videoTotal(w http.ResponseWriter, r *http.Request, _ int64) {
	vstatus, _ := strconv.Atoi(r.FormValue("vstatus"))
	a.ok(w, len(a.byVstatus(vstatus)))
}

// byVstatus vstatus→归档状态过滤：1=ok，3=gone/private，其余（0/2）=无。
func (a *Adapter) byVstatus(vstatus int) []*store.Video {
	videos, err := a.St.ListVideos(0, "", 0, 0)
	if err != nil {
		return nil
	}
	var out []*store.Video
	for _, v := range videos {
		switch vstatus {
		case 1:
			if v.Status == "ok" {
				out = append(out, v)
			}
		case 3:
			if v.Status != "ok" {
				out = append(out, v)
			}
		}
	}
	return out
}

// videoGetone 审核详情：与用户端 getone 同构，另附整型 status。
func (a *Adapter) videoGetone(w http.ResponseWriter, r *http.Request, _ int64) {
	vid := r.FormValue("vid")
	bvid := vid
	if i := strings.LastIndex(vid, "-p"); i > 0 {
		bvid = vid[:i]
	}
	v, err := a.St.GetVideo(bvid)
	if err != nil {
		a.fail(w, 404, "视频不存在或未归档")
		return
	}
	tags, _ := a.St.VideoTags(bvid)
	descr := v.Description
	if model, summary, _, err := a.St.GetAIConclusion(bvid); err == nil && summary != "" {
		descr += fmt.Sprintf("\n\n—— B站官方AI小结（%s）——\n%s", model, summary)
	}
	a.ok(w, map[string]any{
		"video": map[string]any{
			"vid": vid, "title": v.Title, "descr": descr,
			"videoUrl": a.mediaURL(v), "coverUrl": a.coverURL(v),
			"duration": v.Duration, "uploadDate": isoDate(v.Pubdate),
			"tags": strings.Join(tags["bilibili"], "\r\n"),
			"type": 1, "auth": 1, "top": false,
			"status": reviewStatus(v),
		},
		"user":     map[string]any{"uid": v.OwnerMid, "nickname": v.OwnerName, "avatar_url": v.OwnerFace},
		"category": map[string]any{"mcId": 0, "mcName": "补档", "scId": 0, "scName": "归档"},
	})
}

// coverURL 封面优先本地化（同源必达，规避 B 站 CDN 防盗链）。
func (a *Adapter) coverURL(v *store.Video) string {
	local := "media/" + v.Bvid + "/cover.jpg"
	if _, err := os.Stat(filepath.Join(a.DataDir, filepath.FromSlash(local))); err == nil {
		return "/media/" + local
	}
	return v.Pic
}

// mediaURL 本地媒体播放地址（local_path 以 media/ 开头）。
func (a *Adapter) mediaURL(v *store.Video) string {
	rel := v.LocalPath
	if rel == "" {
		rel = "media/" + v.Bvid + "/p1/" + v.Bvid + "-p1.mp4"
		if _, err := os.Stat(filepath.Join(a.DataDir, filepath.FromSlash(rel))); err != nil {
			a.log().Debug("媒体文件缺失", "bvid", v.Bvid)
		}
	}
	return "/media/" + rel
}

// changeStatus 审核结论映射展示状态：1→ok（恢复展示）；0/2/3→private（播放站隐藏）。
// 只动本站展示状态，不伪造 B 站原始状态（gone 语义仍由归档器维护）。
func (a *Adapter) changeStatus(w http.ResponseWriter, r *http.Request, _ int64) {
	_ = r.ParseMultipartForm(1 << 20)
	vid := r.FormValue("vid")
	status, _ := strconv.Atoi(r.FormValue("status"))
	if vid == "" {
		a.fail(w, 400, "缺少 vid")
		return
	}
	newStatus := "private"
	if status == 1 {
		newStatus = "ok"
	}
	res, err := a.St.DB.Exec(`UPDATE videos SET status=? WHERE bvid=?`, newStatus, vid)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, 404, "视频不存在或未归档")
		return
	}
	a.ok(w, true)
}
