package server

// 管理 REST API（/api/v1/*）。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"bili-saver/internal/metrics"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/resolver"
	"bili-saver/internal/store"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// hotReason 热门视频二次确认文案；空串=未达阈值（或提示已禁用）。
// 评论全量翻页的耗时随评论数线性增长，热门视频手动补档前应让用户知情。
func (s *Server) hotReason(view, reply int) string {
	cfg := s.Deps.Cfg.Collector
	hot := false
	parts := []string{}
	if cfg.HotVideoViews > 0 && view >= cfg.HotVideoViews {
		hot = true
		parts = append(parts, fmt.Sprintf("播放 %s", hotHumanize(view)))
	}
	if cfg.HotVideoReplies > 0 && reply >= cfg.HotVideoReplies {
		hot = true
		parts = append(parts, fmt.Sprintf("评论 %s", hotHumanize(reply)))
	}
	if !hot {
		return ""
	}
	return fmt.Sprintf("该视频为热门视频（%s）：全量归档评论可能耗时较久，确认继续？", strings.Join(parts, " / "))
}

func hotHumanize(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	}
	return strconv.Itoa(n)
}

func (s *Server) registerAdmin() {
	m := s.mux
	m.HandleFunc("GET /api/v1/stats", s.handleStats)
	m.HandleFunc("GET /api/v1/live", s.handleLive)
	m.HandleFunc("POST /api/v1/add", s.handleAdd)
	m.HandleFunc("GET /api/v1/resolve", s.handleResolve)
	m.HandleFunc("GET /api/v1/sources", s.handleListSources)
	m.HandleFunc("DELETE /api/v1/sources/{id}", s.handleDeleteSource)
	m.HandleFunc("POST /api/v1/sources/{id}/sync", s.handleSyncSource)
	m.HandleFunc("PATCH /api/v1/sources/{id}", s.handlePatchSource)
	m.HandleFunc("GET /api/v1/videos", s.handleListVideos)
	m.HandleFunc("GET /api/v1/videos/{bvid}", s.handleVideoDetail)
	m.HandleFunc("GET /api/v1/videos/{bvid}/comments", s.handleVideoComments)
	m.HandleFunc("GET /api/v1/videos/{bvid}/danmaku", s.handleVideoDanmaku)
	m.HandleFunc("POST /api/v1/jobs/{id}/retry", s.handleJobRetry)
	m.HandleFunc("GET /api/v1/videos/{bvid}/card", s.handleVideoCard)
	m.HandleFunc("POST /api/v1/videos/{bvid}/archive", s.handleArchiveVideo)
	m.HandleFunc("POST /api/v1/videos/{bvid}/download", s.handleDownloadVideo)
	m.HandleFunc("GET /api/v1/comments/search", s.handleCommentSearch)
	m.HandleFunc("GET /api/v1/jobs", s.handleJobs)
	m.HandleFunc("POST /api/v1/sync-now", s.handleSyncNow)
	m.HandleFunc("GET /api/v1/config", s.handleConfig)
	m.HandleFunc("PATCH /api/v1/config", s.handlePatchConfig)
	m.HandleFunc("GET /api/v1/login/qr", s.handleLoginQR)
	m.HandleFunc("GET /api/v1/login/qr/poll", s.handleLoginPoll)
	m.HandleFunc("GET /api/v1/plugins", s.handlePlugins)
	m.HandleFunc("POST /api/v1/plugins/reload", s.handlePluginsReload)
	m.HandleFunc("POST /api/v1/plugins/{id}/enabled", s.handlePluginEnabled)
	m.HandleFunc("POST /api/v1/media/rebuild", s.handleMediaRebuild)
	m.HandleFunc("POST /api/v1/images/backfill", s.handleImagesBackfill)
	m.HandleFunc("POST /api/v1/levels/backfill", s.handleLevelsBackfill)
	m.HandleFunc("GET /api/v1/tracks", s.handleTracks)
	m.HandleFunc("GET /api/v1/media-servers/status", s.handleMediaServersStatus)
	m.HandleFunc("GET /api/v1/libraries", s.handleListLibraries)
	m.HandleFunc("POST /api/v1/libraries", s.handleCreateLibrary)
	m.HandleFunc("PATCH /api/v1/libraries/{id}", s.handlePatchLibrary)
	m.HandleFunc("DELETE /api/v1/libraries/{id}", s.handleDeleteLibrary)
	m.HandleFunc("POST /api/v1/libraries/{id}/rebuild", s.handleLibraryRebuild)
	m.HandleFunc("GET /api/v1/videos/{bvid}/similar", s.handleSimilarVideos)
	m.HandleFunc("GET /api/v1/relations/{type}/{id}", s.handleRelations)
	m.HandleFunc("GET /api/v1/teriteri/status", s.handleTeriteriStatus)
	m.HandleFunc("POST /api/v1/teriteri/install", s.handleTeriteriInstall)
}

// ---- TeriTeri 多端（一键安装 / 站点状态） ----

// handleTeriteriStatus 多端站点状态 + 安装任务进度。
func (s *Server) handleTeriteriStatus(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Teri == nil {
		writeJSON(w, 200, map[string]any{"available": false, "reason": "未启用"})
		return
	}
	st := s.Deps.Teri.Status()
	st["available"] = true
	writeJSON(w, 200, st)
}

// handleTeriteriInstall 启动一键安装（克隆+构建用户端与管理员端）。
func (s *Server) handleTeriteriInstall(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Teri == nil {
		errJSON(w, 503, fmt.Errorf("TeriTeri 多端未启用"))
		return
	}
	if err := s.Deps.Teri.Installer().Start(s.Deps.Cfg.Teriteri.ClientAddr); err != nil {
		errJSON(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- 概览 ----

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	st, err := s.Deps.St.ArchiveStats()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	sources, _ := s.Deps.St.ListSources()
	writeJSON(w, 200, map[string]any{
		"archive": st,
		"sources": len(sources),
		"login":   s.Deps.API.CachedLogin(reqCtx()),
		"ffmpeg":  s.ffmpegOK(),
	})
}

func (s *Server) ffmpegOK() bool {
	// 简单探测：能拿到路径即可（下载失败会再报错）
	return s.Deps.Dl != nil
}

// ---- 添加源 / 直接归档 ----

type addReq struct {
	Input            string `json:"input"`
	Watch            *bool  `json:"watch"`
	Download         *bool  `json:"download"`
	IntervalMin      int    `json:"interval_minutes"`  // 完整重同步间隔，0=全局默认
	SyncMode         string `json:"sync_mode"`         // once | interval（默认 interval）
	IntervalComments int    `json:"interval_comments"` // 0=跟随主间隔，-1=从不
	IntervalDanmaku  int    `json:"interval_danmaku"`
	IntervalSubs     int    `json:"interval_subtitles"`
	ConfirmHot       bool   `json:"confirm_hot"` // 热门视频二次确认后的重提交
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req addReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err)
		return
	}
	in, err := resolver.Normalize(r.Context(), req.Input)
	if err != nil {
		errJSON(w, 400, err)
		return
	}
	watch := true
	if req.Watch != nil {
		watch = *req.Watch
	}
	download := s.Deps.Cfg.Download.AutoDownload
	if req.Download != nil {
		download = *req.Download
	}
	// 热门视频二次确认：手动补档前告知评论全量翻页的耗时代价（自动化路径不打扰）
	if in.Type == resolver.TypeVideo && !req.ConfirmHot {
		if vv, err := s.Deps.API.VideoView(r.Context(), in.ExtID); err == nil {
			if reason := s.hotReason(vv.StatStat.View, vv.StatStat.Reply); reason != "" {
				writeJSON(w, 200, map[string]any{
					"need_confirm": true, "reason": reason,
					"view": vv.StatStat.View, "reply": vv.StatStat.Reply,
				})
				return
			}
		}
	}

	// 视频源：登记（可选监控）并直接归档；其余建立监控源并展开
	syncMode := req.SyncMode
	if syncMode != "once" {
		syncMode = "interval"
	}
	src := &store.Source{
		Type: string(in.Type), ExtID: in.ExtID, Extra: in.ExtraJSON(),
		Title: in.String(), Watch: watch,
		SyncIntervalMinutes: req.IntervalMin, SyncMode: syncMode,
		IntervalCommentsMin: req.IntervalComments,
		IntervalDanmakuMin:  req.IntervalDanmaku,
		IntervalSubsMin:     req.IntervalSubs,
		CreatedAt:           store.Now(),
	}
	// 勾选态即源级触发器初值：勾=1 开启自动下载，不勾=-1 明确关闭。
	// 否则源保持 0（跟随全局，默认开），出现"本次不下载、日后新视频仍被自动下载"的语义陷阱。
	if req.Download != nil {
		if *req.Download {
			src.AutoDownload = 1
		} else {
			src.AutoDownload = -1
		}
	}
	if _, err := s.Deps.St.UpsertSource(src); err != nil {
		errJSON(w, 500, err)
		return
	}
	if in.Type == resolver.TypeVideo {
		if syncMode == "once" {
			// 仅一次模式走 sync_source：归档完成后自动停止监控
			if _, err := s.Deps.Mgr.Submit("sync_source", map[string]any{"source_id": src.ID, "download": download}); err != nil {
				errJSON(w, 500, err)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "type": string(in.Type), "ext_id": in.ExtID, "queued": true, "mode": "once"})
			return
		}
		if _, err := s.Deps.Mgr.Submit("archive", map[string]any{"bvid": in.ExtID, "download": download}); err != nil {
			errJSON(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "type": string(in.Type), "ext_id": in.ExtID, "queued": true})
		return
	}
	if _, err := s.Deps.Mgr.Submit("sync_source", map[string]any{"source_id": src.ID, "download": download}); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "type": string(in.Type), "ext_id": in.ExtID, "queued": true})
}

// ---- 源管理 ----

func (s *Server) handleListSources(w http.ResponseWriter, _ *http.Request) {
	srcs, err := s.Deps.St.ListSources()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if srcs == nil {
		srcs = []*store.Source{}
	}
	writeJSON(w, 200, srcs)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Deps.St.DeleteSource(id); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	jobID, err := s.Deps.Mgr.Submit("sync_source", map[string]any{"source_id": id})
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": jobID})
}

// ---- 视频库 ----

func (s *Server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	owner, _ := strconv.ParseInt(q.Get("owner"), 10, 64)
	limit, _ := strconv.Atoi(orDefaultStr(q.Get("limit"), "50"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	var out []*store.VideoWithCounts
	var err error
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		var bvids []string
		bvids, err = s.Deps.St.SearchVideosFTS(kw, limit)
		if err == nil {
			out = []*store.VideoWithCounts{}
			for _, b := range bvids {
				if v, e := s.Deps.St.GetVideoWithCounts(b); e == nil {
					out = append(out, v)
				}
			}
		}
	} else {
		out, err = s.Deps.St.ListVideosWithCounts(owner, q.Get("status"), limit, offset)
	}
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if out == nil {
		out = []*store.VideoWithCounts{}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleVideoDetail(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	v, err := s.Deps.St.GetVideo(bvid)
	if err != nil {
		errJSON(w, 404, err)
		return
	}
	pages, _ := s.Deps.St.ListPages(bvid)
	stats, _ := s.Deps.St.ListStats(bvid)
	subs, _ := s.Deps.St.ListSubtitles(bvid, 0)
	tags, _ := s.Deps.St.VideoTags(bvid)
	summaries, _ := s.Deps.St.ListSummaries(bvid)
	vis, del, _ := s.Deps.St.CountCommentsByStatus(bvid)
	model, summary, outline, conclErr := s.Deps.St.GetAIConclusion(bvid)
	// 观看渠道定位（本体只管状态；播放交给 teriteri / 媒体库）
	mediaRel := ""
	if v.LocalPath != "" {
		mediaRel = "/media/" + filepath.ToSlash(v.LocalPath)
	}
	var source any
	if v.SourceID > 0 {
		if src, err := s.Deps.St.GetSource(v.SourceID); err == nil {
			source = src
		}
	}
	writeJSON(w, 200, map[string]any{
		"video": v, "pages": pages, "stats_history": stats,
		"media_url": mediaRel, "source": source,
		"subtitles": subs, "tags": tags, "summaries": summaries,
		"comments": map[string]int{"visible": vis, "deleted": del},
		"ai_conclusion": func() any {
			if conclErr != nil {
				return nil
			}
			return map[string]string{"model": model, "summary": summary, "outline": outline, "origin": "bilibili:ai-conclusion"}
		}(),
	})
}

func (s *Server) handleArchiveVideo(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	var body struct {
		Download   *bool `json:"download"`
		ConfirmHot bool  `json:"confirm_hot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dl := s.Deps.Cfg.Download.AutoDownload
	if body.Download != nil {
		dl = *body.Download
	}
	// 热门视频二次确认：按库内最新统计判定（重同步重拉评论同样耗时）
	if !body.ConfirmHot {
		if stats, err := s.Deps.St.ListStats(bvid); err == nil && len(stats) > 0 {
			last := stats[len(stats)-1]
			if reason := s.hotReason(last.View, last.Reply); reason != "" {
				writeJSON(w, 200, map[string]any{
					"need_confirm": true, "reason": reason,
					"view": last.View, "reply": last.Reply,
				})
				return
			}
		}
	}
	jobID, err := s.Deps.Mgr.Submit("archive", map[string]any{"bvid": bvid, "download": dl})
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": jobID})
}

func (s *Server) handleDownloadVideo(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	jobID, err := s.Deps.Mgr.Submit("download", map[string]any{"bvid": bvid})
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": jobID})
}

// handleVideoCard 快速预览卡片 PNG（derived 层缓存，?refresh=1 强制重渲染，
// ?download=1 以附件形式下发，方便日后分享功能直接取用）。
func (s *Server) handleVideoCard(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Card == nil {
		errJSON(w, 500, errStr("卡片生成器未初始化"))
		return
	}
	bvid := r.PathValue("bvid")
	if _, err := s.Deps.St.GetVideo(bvid); err != nil {
		errJSON(w, 404, err)
		return
	}
	if r.URL.Query().Get("refresh") == "1" || !s.Deps.Card.Exists(bvid) {
		if _, err := s.Deps.Card.RenderToFile(r.Context(), bvid); err != nil {
			errJSON(w, 500, err)
			return
		}
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=card-%s.png", bvid))
	}
	http.ServeFile(w, r, s.Deps.Card.PathFor(bvid))
}

func (s *Server) handleCommentSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 200, map[string]any{"query": "", "total": 0, "hits": []any{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.Deps.St.SearchCommentsFull(q, r.URL.Query().Get("status"), limit)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if hits == nil {
		hits = []*store.CommentHit{}
	}
	writeJSON(w, 200, map[string]any{"query": q, "total": len(hits), "hits": hits})
}

// ---- 任务 ----

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(orDefaultStr(q.Get("limit"), "100"))
	var states []string
	if st := q.Get("state"); st != "" {
		states = strings.Split(st, ",")
	}
	jl, err := s.Deps.St.ListJobs(limit, states)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if jl == nil {
		jl = []*store.Job{}
	}
	writeJSON(w, 200, jl)
}

func (s *Server) handleSyncNow(w http.ResponseWriter, _ *http.Request) {
	srcs, err := s.Deps.St.ListSources()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	n := 0
	for _, src := range srcs {
		if src.Watch {
			if _, err := s.Deps.Mgr.Submit("sync_source", map[string]any{"source_id": src.ID}); err == nil {
				n++
			}
		}
	}
	writeJSON(w, 200, map[string]any{"queued": n})
}

// ---- 配置（脱敏） ----

// configPayload GET/PATCH config 共用的响应形状（login 由 GET 单独附加）。
func configPayload(c *config.Config) map[string]any {
	return map[string]any{
		"data_dir": c.DataDir, "listen": c.Listen,
		"quality": c.Download.Quality, "qualities": c.Download.Qualities,
		"audio_qualities": c.Download.AudioQualities,
		"concurrency":     c.Download.Concurrency,
		"segments":        c.Download.Segments,
		"allow_av1":       c.Download.AllowAV1, "allow_4k": c.Download.Allow4K,
		"auto_download":    c.Download.AutoDownload,
		"download_danmaku": c.Download.DownloadDanmaku,
		"comments_max":     c.Collector.CommentsMax, "sub_replies_max": c.Collector.SubRepliesMax,
		"full_resync_comments": c.Collector.FullResyncComments,
		"sync_default_minutes": c.Collector.SyncDefaultMinutes,
		"stats_resync_hours":   c.Collector.StatsResyncHours,
		"hot_video_views":      c.Collector.HotVideoViews, "hot_video_replies": c.Collector.HotVideoReplies,
		"adaptive_sync": c.Collector.AdaptiveSync,
		"min_req_ms":    c.Bilibili.MinReqMs,
		"media_enabled": c.Media.Enabled, "media_layout": c.Media.Layout, "media_root": c.Media.Root,
		"media_servers":   c.Media.Servers,
		"media_link_mode": c.Media.LinkMode, "media_public_url": c.Media.PublicURL,
		"teriteri_enabled": c.Teriteri.Enabled,
		"frontend_url":     c.Teriteri.FrontendURL,
		"plugins_enabled":  c.Plugins.Enabled, "plugins_dir": c.Plugins.Dir,
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	p := configPayload(s.Deps.Cfg)
	p["login"] = s.Deps.API.CachedLogin(reqCtx())
	writeJSON(w, 200, p)
}

// handlePatchConfig 更新运行配置（设置页）。全部字段可选、先全量校验后提交；
// 提交后 Normalize+Validate，再写回配置文件（.bak 备份，路径未知时仅改内存）。
// min_req_ms / teriteri_enabled / concurrency 等组件启动时捕获的项需重启完全生效。
func (s *Server) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	c := s.Deps.Cfg
	var req struct {
		SyncDefaultMinutes *int      `json:"sync_default_minutes"`
		StatsResyncHours   *int      `json:"stats_resync_hours"`
		AdaptiveSync       *bool     `json:"adaptive_sync"`
		CommentsMax        *int      `json:"comments_max"`
		SubRepliesMax      *int      `json:"sub_replies_max"`
		FullResyncComments *bool     `json:"full_resync_comments"`
		HotVideoViews      *int      `json:"hot_video_views"`
		HotVideoReplies    *int      `json:"hot_video_replies"`
		AutoDownload       *bool     `json:"auto_download"`
		DownloadDanmaku    *bool     `json:"download_danmaku"`
		Concurrency        *int      `json:"concurrency"`
		Segments           *int      `json:"segments"`
		AllowAV1           *bool     `json:"allow_av1"`
		Allow4K            *bool     `json:"allow_4k"`
		Qualities          *[]string `json:"qualities"`
		AudioQualities     *[]string `json:"audio_qualities"`
		MinReqMs           *int      `json:"min_req_ms"`
		MediaEnabled       *bool     `json:"media_enabled"`
		MediaLayout        *string   `json:"media_layout"`
		MediaLinkMode      *string   `json:"media_link_mode"`
		MediaPublicURL     *string   `json:"media_public_url"`
		TeriteriEnabled    *bool     `json:"teriteri_enabled"`
		FrontendURL        *string   `json:"frontend_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err)
		return
	}
	// ---- 先校验后提交：任何非法值直接 400，不产生半套变更 ----
	bad := func(msg string) { errJSON(w, 400, errStr(msg)) }
	if req.SyncDefaultMinutes != nil && *req.SyncDefaultMinutes < 0 {
		bad("sync_default_minutes 不能为负")
		return
	}
	if req.StatsResyncHours != nil && *req.StatsResyncHours < 0 {
		bad("stats_resync_hours 不能为负")
		return
	}
	if req.CommentsMax != nil && *req.CommentsMax < 0 {
		bad("comments_max 不能为负")
		return
	}
	if req.SubRepliesMax != nil && *req.SubRepliesMax < 0 {
		bad("sub_replies_max 不能为负")
		return
	}
	if req.HotVideoViews != nil && *req.HotVideoViews < 0 {
		bad("hot_video_views 不能为负")
		return
	}
	if req.HotVideoReplies != nil && *req.HotVideoReplies < 0 {
		bad("hot_video_replies 不能为负")
		return
	}
	if req.Concurrency != nil && *req.Concurrency < 1 {
		bad("concurrency 至少为 1")
		return
	}
	if req.Segments != nil && *req.Segments < 1 {
		bad("segments 至少为 1")
		return
	}
	if req.MinReqMs != nil && *req.MinReqMs < 0 {
		bad("min_req_ms 不能为负")
		return
	}
	if req.MediaLayout != nil && !oneOf(*req.MediaLayout, "movie", "show", "collection") {
		bad("media_layout 必须是 movie/show/collection")
		return
	}
	if req.MediaLinkMode != nil && !oneOf(*req.MediaLinkMode, "auto", "symlink", "hardlink", "copy", "strm") {
		bad("media_link_mode 必须是 auto/symlink/hardlink/copy/strm")
		return
	}
	// ---- 提交 ----
	apply := func() {
		if req.SyncDefaultMinutes != nil {
			c.Collector.SyncDefaultMinutes = *req.SyncDefaultMinutes
		}
		if req.StatsResyncHours != nil {
			c.Collector.StatsResyncHours = *req.StatsResyncHours
		}
		if req.AdaptiveSync != nil {
			c.Collector.AdaptiveSync = *req.AdaptiveSync
		}
		if req.CommentsMax != nil {
			c.Collector.CommentsMax = *req.CommentsMax
		}
		if req.SubRepliesMax != nil {
			c.Collector.SubRepliesMax = *req.SubRepliesMax
		}
		if req.FullResyncComments != nil {
			c.Collector.FullResyncComments = *req.FullResyncComments
		}
		if req.HotVideoViews != nil {
			c.Collector.HotVideoViews = *req.HotVideoViews
		}
		if req.HotVideoReplies != nil {
			c.Collector.HotVideoReplies = *req.HotVideoReplies
		}
		if req.AutoDownload != nil {
			c.Download.AutoDownload = *req.AutoDownload
		}
		if req.DownloadDanmaku != nil {
			c.Download.DownloadDanmaku = *req.DownloadDanmaku
		}
		if req.Concurrency != nil {
			c.Download.Concurrency = *req.Concurrency
		}
		if req.Segments != nil {
			c.Download.Segments = *req.Segments
		}
		if req.AllowAV1 != nil {
			c.Download.AllowAV1 = *req.AllowAV1
		}
		if req.Allow4K != nil {
			c.Download.Allow4K = *req.Allow4K
		}
		if req.Qualities != nil {
			c.Download.Qualities = *req.Qualities
		}
		if req.AudioQualities != nil {
			c.Download.AudioQualities = *req.AudioQualities
		}
		if req.MinReqMs != nil {
			c.Bilibili.MinReqMs = *req.MinReqMs
		}
		if req.MediaEnabled != nil {
			c.Media.Enabled = *req.MediaEnabled
		}
		if req.MediaLayout != nil {
			c.Media.Layout = *req.MediaLayout
		}
		if req.MediaLinkMode != nil {
			c.Media.LinkMode = *req.MediaLinkMode
		}
		if req.MediaPublicURL != nil {
			c.Media.PublicURL = strings.TrimSpace(*req.MediaPublicURL)
		}
		if req.TeriteriEnabled != nil {
			c.Teriteri.Enabled = *req.TeriteriEnabled
		}
		if req.FrontendURL != nil {
			c.Teriteri.FrontendURL = *req.FrontendURL
		}
	}
	apply()
	c.Normalize()
	if err := c.Validate(); err != nil {
		errJSON(w, 400, err)
		return
	}
	if s.Deps.ConfigPath != "" {
		if err := config.SaveConfigFile(s.Deps.ConfigPath, c); err != nil {
			errJSON(w, 500, errStr("已生效但写盘失败: "+err.Error()))
			return
		}
	}
	writeJSON(w, 200, configPayload(c))
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// ---- 扫码登录 ----

func (s *Server) handleLoginQR(w http.ResponseWriter, _ *http.Request) {
	qr, err := s.Deps.API.QRGenerate(reqCtx())
	if err != nil {
		errJSON(w, 502, err)
		return
	}
	png, err := qrPNG(qr.URL)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"url": qr.URL, "key": qr.QRCodeKey, "png_base64": png})
}

func (s *Server) handleLoginPoll(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	res, err := s.Deps.API.QRPoll(reqCtx(), key)
	if err != nil {
		errJSON(w, 502, err)
		return
	}
	if res.Code == 0 {
		if err := s.Deps.API.SaveCookies(s.Deps.Cfg.CookiePath()); err != nil {
			errJSON(w, 500, err)
			return
		}
		biliapi.ResetLoginCache()
	}
	isLogin := false
	if res.Code == 0 {
		isLogin, _ = s.Deps.API.NavIsLogin(reqCtx())
	}
	writeJSON(w, 200, map[string]any{"code": res.Code, "message": res.Message, "url": res.URL, "is_login": isLogin})
}

// ---- 插件 ----

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.Deps.St.ListPlugins()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if rows == nil {
		rows = []*store.PluginRow{}
	}
	writeJSON(w, 200, rows)
}

func (s *Server) handlePluginsReload(w http.ResponseWriter, _ *http.Request) {
	if err := s.Deps.Bus.Reload(); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]int{"loaded": len(s.Deps.Bus.Manifests())})
}

func (s *Server) handlePluginEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errJSON(w, 400, err)
		return
	}
	if err := s.Deps.St.SetPluginEnabled(id, body.Enabled); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- 媒体库桥 / 关系网 ----

func (s *Server) handleMediaRebuild(w http.ResponseWriter, _ *http.Request) {
	if !s.Deps.Cfg.Media.Enabled {
		errJSON(w, 400, errStr("媒体库桥未启用（media.enabled）"))
		return
	}
	res, err := s.Deps.Med.RebuildAll()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, res)
}

// handleImagesBackfill 入队全库图片补档（封面/头像本地化，幂等）。
func (s *Server) handleImagesBackfill(w http.ResponseWriter, _ *http.Request) {
	jobID, err := s.Deps.Mgr.Submit("fetch_images", map[string]any{})
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": jobID})
}

// handleTracks 跟踪列表：{total 过滤后总数, counts 分状态计数, items 页内记录（新→旧）}。
// state=queued|done|failed 过滤；q 模糊匹配 标题/UP主/bvid；limit（≤100，默认 20）/offset 分页。
func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	qp := r.URL.Query()
	limit, _ := strconv.Atoi(qp.Get("limit"))
	offset, _ := strconv.Atoi(qp.Get("offset"))
	items, total, err := s.Deps.St.ListTracksPage(qp.Get("state"), qp.Get("q"), limit, offset)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if items == nil {
		items = []*store.Track{}
	}
	counts, err := s.Deps.St.TrackStateCounts()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"total": total, "counts": counts, "items": items})
}

// handleMediaServersStatus 第三方媒体库在线状态（并发探测，3s 超时）。
func (s *Server) handleMediaServersStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.mediaServerStatuses(r.Context()))
}

// handleLevelsBackfill 入队等级补档（评论快照回填+UP主等级，幂等）。
func (s *Server) handleLevelsBackfill(w http.ResponseWriter, _ *http.Request) {
	jobID, err := s.Deps.Mgr.Submit("fetch_levels", map[string]any{})
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": jobID})
}

func (s *Server) handleListLibraries(w http.ResponseWriter, _ *http.Request) {
	libs, err := s.Deps.St.ListMediaLibraries()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if libs == nil {
		libs = []*store.MediaLibrary{}
	}
	writeJSON(w, 200, libs)
}

func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var l store.MediaLibrary
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		errJSON(w, 400, err)
		return
	}
	if l.Name == "" || l.RootPath == "" {
		errJSON(w, 400, errStr("name 与 root_path 必填"))
		return
	}
	l.CreatedAt = store.Now()
	id, err := s.Deps.St.UpsertMediaLibrary(&l)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) handlePatchLibrary(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	l, err := s.Deps.St.GetMediaLibrary(id)
	if err != nil {
		errJSON(w, 404, err)
		return
	}
	var req struct {
		Name           *string `json:"name"`
		RootPath       *string `json:"root_path"`
		Layout         *string `json:"layout"`
		LinkMode       *string `json:"link_mode"`
		MetadataFormat *string `json:"metadata_format"`
		MatchRules     *string `json:"match_rules"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err)
		return
	}
	if req.Name != nil {
		l.Name = *req.Name
	}
	if req.RootPath != nil {
		l.RootPath = *req.RootPath
	}
	if req.Layout != nil {
		l.Layout = *req.Layout
	}
	if req.LinkMode != nil {
		l.LinkMode = *req.LinkMode
	}
	if req.MetadataFormat != nil {
		l.MetadataFormat = *req.MetadataFormat
	}
	if req.MatchRules != nil {
		l.MatchRules = *req.MatchRules
	}
	if req.Enabled != nil {
		l.Enabled = *req.Enabled
	}
	if _, err := s.Deps.St.UpsertMediaLibrary(l); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, l)
}

func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Deps.St.DeleteMediaLibrary(id); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLibraryRebuild(w http.ResponseWriter, _ *http.Request) {
	// 单库重建：临时禁用其他库不可行（共享状态），直接全量重建并返回
	s.handleMediaRebuild(w, nil)
}

func (s *Server) handleRelations(w http.ResponseWriter, r *http.Request) {
	t := r.PathValue("type")
	id := r.PathValue("id")
	rels, err := s.Deps.St.ListRelations(t, id)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if rels == nil {
		rels = []*store.Relation{}
	}
	writeJSON(w, 200, rels)
}

// ---- 辅助 ----

func reqCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = cancel // 短生命周期请求，进程退出时自然回收
	return ctx
}

func orDefaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func errStr(msg string) error { return &simpleErr{msg} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// handleSimilarVideos 基于插件生成的向量做余弦相似检索（联想搜索/关系网数据源）。
func (s *Server) handleSimilarVideos(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	emb, err := s.Deps.St.ListVideoEmbeddings("")
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	base, ok := emb[bvid]
	if !ok {
		writeJSON(w, 200, map[string]any{"note": "该视频尚无向量（需要 ai-search 插件生成）", "items": []any{}})
		return
	}
	type item struct {
		Bvid  string  `json:"bvid"`
		Title string  `json:"title"`
		Score float64 `json:"score"`
	}
	items := []item{}
	for id, vec := range emb {
		if id == bvid || len(vec) != len(base) {
			continue
		}
		var dot, na, nb float64
		for i := range base {
			dot += float64(base[i]) * float64(vec[i])
			na += float64(base[i]) * float64(base[i])
			nb += float64(vec[i]) * float64(vec[i])
		}
		if na == 0 || nb == 0 {
			continue
		}
		score := dot / (sqrtF(na) * sqrtF(nb))
		items = append(items, item{Bvid: id, Score: score})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	limit := 10
	out := items[:0]
	for i, it := range items {
		if i >= limit {
			break
		}
		if v, err := s.Deps.St.GetVideo(it.Bvid); err == nil {
			it.Title = v.Title
		}
		out = append(out, it)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func sqrtF(f float64) float64 { return math.Sqrt(f) }

// handlePatchSource 更新源的同步策略（nil 字段保持不变）。
func (s *Server) handlePatchSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	src, err := s.Deps.St.GetSource(id)
	if err != nil {
		errJSON(w, 404, err)
		return
	}
	var req struct {
		Watch               *bool   `json:"watch"`
		SyncMode            *string `json:"sync_mode"`
		IntervalMin         *int    `json:"interval_minutes"`
		IntervalCommentsMin *int    `json:"interval_comments"`
		IntervalDanmakuMin  *int    `json:"interval_danmaku"`
		IntervalSubsMin     *int    `json:"interval_subtitles"`
		AutoDownload        *int    `json:"auto_download"` // 1开/-1关/0跟随全局
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err)
		return
	}
	if req.Watch != nil {
		src.Watch = *req.Watch
	}
	if req.SyncMode != nil {
		if *req.SyncMode != "once" && *req.SyncMode != "interval" {
			errJSON(w, 400, errStr("sync_mode 只能是 once 或 interval"))
			return
		}
		src.SyncMode = *req.SyncMode
		if *req.SyncMode == "interval" {
			src.Watch = true // 切回周期模式时恢复监控
		}
	}
	if req.IntervalMin != nil {
		src.SyncIntervalMinutes = *req.IntervalMin
	}
	if req.IntervalCommentsMin != nil {
		src.IntervalCommentsMin = *req.IntervalCommentsMin
	}
	if req.IntervalDanmakuMin != nil {
		src.IntervalDanmakuMin = *req.IntervalDanmakuMin
	}
	if req.IntervalSubsMin != nil {
		src.IntervalSubsMin = *req.IntervalSubsMin
	}
	if req.AutoDownload != nil {
		if *req.AutoDownload != 0 && *req.AutoDownload != 1 && *req.AutoDownload != -1 {
			errJSON(w, 400, errStr("auto_download 只能是 1(开)/-1(关)/0(跟随全局)"))
			return
		}
		src.AutoDownload = *req.AutoDownload
	}
	if _, err := s.Deps.St.UpsertSource(src); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, src)
}

// handleResolve 纯本地解析输入类型（不打 B 站接口，供输入框实时识别）。
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	in, err := resolver.Parse(r.URL.Query().Get("input"))
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "type": string(in.Type), "ext_id": in.ExtID, "label": in.String(),
	})
}

// handleVideoComments 管理端评论明细（消失审计）：status 可选 visible/deleted/空=全部。
func (s *Server) handleVideoComments(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	q := r.URL.Query()
	status := q.Get("status")
	limit, _ := strconv.Atoi(orDefaultStr(q.Get("limit"), "200"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	list, err := s.Deps.St.ListComments(bvid, status, limit, offset)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if list == nil {
		list = []*store.Comment{}
	}
	writeJSON(w, 200, list)
}

// handleVideoDanmaku 管理端弹幕明细（消失审计）。
func (s *Server) handleVideoDanmaku(w http.ResponseWriter, r *http.Request) {
	bvid := r.PathValue("bvid")
	status := r.URL.Query().Get("status")
	pages, _ := s.Deps.St.ListPages(bvid)
	var out []*store.Danmaku
	for _, pg := range pages {
		rows, err := s.Deps.St.ListDanmaku(pg.Cid, status)
		if err != nil {
			errJSON(w, 500, err)
			return
		}
		out = append(out, rows...)
	}
	if out == nil {
		out = []*store.Danmaku{}
	}
	writeJSON(w, 200, out)
}

// handleJobRetry 重试失败/已完成任务（复制载荷重新入队）。
func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	j, err := s.Deps.St.GetJob(id)
	if err != nil {
		errJSON(w, 404, err)
		return
	}
	newID, err := s.Deps.Mgr.Submit(j.Type, json.RawMessage(j.PayloadJSON))
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": newID})
}

// handleLive 轻量实时指标（总览页 2s 轮询用，避免全表 count）。
func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	type liveJob struct {
		ID       int64   `json:"id"`
		Type     string  `json:"type"`
		Progress float64 `json:"progress"`
		Detail   string  `json:"detail"`
	}
	var running, queued int
	_ = s.Deps.St.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN state='running' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='queued' THEN 1 ELSE 0 END),0)
		FROM jobs`).Scan(&running, &queued)
	runningJobs := []liveJob{}
	if rows, err := s.Deps.St.ListJobs(8, []string{"running"}); err == nil {
		for _, j := range rows {
			runningJobs = append(runningJobs, liveJob{ID: j.ID, Type: j.Type, Progress: j.Progress, Detail: j.Detail})
		}
	}
	writeJSON(w, 200, map[string]any{
		"running": running, "queued": queued, "jobs": runningJobs,
		"download_bps": metrics.Download.BPS(), "upload_bps": metrics.Upload.BPS(),
		"download_total": metrics.Download.Total(), "upload_total": metrics.Upload.Total(),
	})
}
