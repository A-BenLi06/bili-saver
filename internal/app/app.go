// Package app 装配所有组件：store/api/collector/downloader/jobs/plugin/media/server。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/card"
	"bili-saver/internal/collector"
	"bili-saver/internal/config"
	"bili-saver/internal/downloader"
	"bili-saver/internal/images"
	"bili-saver/internal/jobs"
	"bili-saver/internal/levels"
	"bili-saver/internal/media"
	"bili-saver/internal/plugin"
	"bili-saver/internal/resolver"
	"bili-saver/internal/server"
	"bili-saver/internal/store"
	"bili-saver/internal/teriteriweb"
)

// App 持有全部组件引用。
type App struct {
	Cfg  *config.Config
	Log  *slog.Logger
	St   *store.Store
	API  *biliapi.Client
	Col  *collector.Collector
	Dl   *downloader.Downloader
	Mgr  *jobs.Manager
	Bus  *plugin.Bus
	Med  *media.Bridge
	Card *card.Generator
	Img  *images.Localizer
	Lv   *levels.Backfiller
	Srv  *server.Server
	Teri *teriteriweb.Web
	// CfgPath 配置文件路径（serve 时由 CLI 注入）；非空时设置页 PATCH config 会写盘。
	CfgPath string
}

// asyncEmitter 让业务事件不阻塞在插件执行上。
type asyncEmitter struct{ b *plugin.Bus }

func (a asyncEmitter) Emit(ev string, p map[string]any) { a.b.EmitAsync(ev, p) }

// New 初始化全部组件（不开服务、不启动 worker）。
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	if err := mkdirs(cfg); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "bili-saver.db"))
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	api, err := biliapi.New(cfg.CookiePath(), cfg.Bilibili.MinReqMs, cfg.Bilibili.UserAgent)
	if err != nil {
		st.Close()
		return nil, err
	}
	bus := plugin.NewBus(st, st, cfg.Plugins.Dir, log, plugin.Context{
		DataDir: cfg.DataDir,
		APIBase: "http://127.0.0.1" + cfg.Listen,
		Token:   cfg.Admin.Token,
	})
	col := collector.New(st, api, cfg, log, asyncEmitter{bus})
	api.SetSnapshotHook(col.SnapshotHook())
	dl := downloader.New(st, api, cfg, log, asyncEmitter{bus})
	mgr := jobs.NewManager(st, log, cfg.Download.Concurrency)
	med := media.New(st, cfg, log)
	cardGen := card.New(st, cfg, log)
	img := images.New(st, cfg.DataDir, cfg.Bilibili.UserAgent, log)
	lv := levels.New(st, cfg.DataDir, api, log)
	if err := st.EnsureDefaultLibrary(cfg.Media.Root, cfg.Media.Layout, cfg.Media.LinkMode); err != nil {
		log.Warn("默认媒体库迁移失败", "err", err)
	}
	a := &App{Cfg: cfg, Log: log, St: st, API: api, Col: col, Dl: dl, Mgr: mgr, Bus: bus, Med: med, Card: cardGen, Img: img, Lv: lv}
	if cfg.Teriteri.Enabled {
		a.Teri = teriteriweb.New(cfg, log)
		// 首页轮播取归档库最新可播视频的本地封面（构建期写入 client）
		a.Teri.CarouselProvider = func() []teriteriweb.CarouselItem {
			vs, err := st.ListVideos(0, "ok", 12, 0)
			if err != nil {
				return nil
			}
			items := make([]teriteriweb.CarouselItem, 0, len(vs))
			for _, v := range vs {
				if !v.Downloaded {
					continue
				}
				items = append(items, teriteriweb.CarouselItem{
					URL:    "/media/media/" + v.Bvid + "/cover.jpg",
					Title:  v.Title,
					Color:  "#fb7299",
					Target: "/video/" + v.Bvid,
				})
				if len(items) >= 8 {
					break
				}
			}
			return items
		}
	}
	a.registerJobs()
	if cfg.Plugins.Enabled {
		if err := a.Bus.Reload(); err != nil {
			log.Warn("插件加载失败", "err", err)
		}
	}
	return a, nil
}

// ReloadPlugins 扫描插件目录（启用时）。
func (a *App) ReloadPlugins() error {
	if !a.Cfg.Plugins.Enabled {
		return nil
	}
	return a.Bus.Reload()
}

func mkdirs(cfg *config.Config) error {
	for _, d := range []string{cfg.DataDir, cfg.Media.Root, cfg.Plugins.Dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// registerJobs 注册任务类型。
func (a *App) registerJobs() {
	a.Mgr.Register("archive", a.jobArchive)
	a.Mgr.Register("download", a.jobDownload)
	a.Mgr.Register("sync_source", a.jobSyncSource)
	a.Mgr.Register("sync_stats_all", a.jobSyncStatsAll)
	a.Mgr.Register("touch_stats", a.jobTouchStats)
	a.Mgr.Register("fetch_images", a.jobFetchImages)
	a.Mgr.Register("fetch_levels", a.jobFetchLevels)
}

func (a *App) jobArchive(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	var p struct {
		Bvid             string `json:"bvid"`
		Download         *bool  `json:"download"`
		RefreshComments  *bool  `json:"refresh_comments"`
		RefreshDanmaku   *bool  `json:"refresh_danmaku"`
		RefreshSubtitles *bool  `json:"refresh_subtitles"`
		SourceID         int64  `json:"source_id"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	opts := collector.AllOptions()
	if p.RefreshComments != nil || p.RefreshDanmaku != nil || p.RefreshSubtitles != nil {
		// 显式给出任一范围开关时按给定范围执行
		opts = collector.CollectOptions{
			RefreshComments:  boolOr(p.RefreshComments),
			RefreshDanmaku:   boolOr(p.RefreshDanmaku),
			RefreshSubtitles: boolOr(p.RefreshSubtitles),
		}
	}
	res, err := a.Col.ArchiveVideoOpts(ctx, p.Bvid, opts)
	if err != nil {
		return err
	}
	if p.SourceID != 0 {
		_, _ = a.St.DB.Exec(`UPDATE videos SET source_id=? WHERE bvid=? AND source_id=0`, p.SourceID, res.Bvid)
	}
	// 图片本地化（封面/头像落盘 media/，绕开图床防盗链；失败不阻塞归档）
	if !res.Gone {
		if n, err := a.Img.SyncVideo(ctx, res.Bvid); err != nil {
			a.Log.Warn("图片本地化失败", "bvid", res.Bvid, "err", err)
		} else if n > 0 {
			a.Log.Info("图片本地化完成", "bvid", res.Bvid, "新增", n)
		}
		if err := a.Lv.EnsureOwnerLevel(ctx, res.Bvid); err != nil {
			a.Log.Warn("UP主等级拉取失败", "bvid", res.Bvid, "err", err)
		}
	}
	progress(0.4, "归档完成")
	a.refreshCard(ctx, res.Bvid)
	dl := a.Cfg.Download.AutoDownload
	if p.Download != nil {
		dl = *p.Download
	}
	if dl && !res.Gone {
		if err := a.Dl.DownloadVideo(ctx, res.Bvid, func(f float64, detail string) {
			progress(0.4+f*0.6, detail)
		}); err != nil {
			// 触发器跟踪记录回写失败态（无记录时空操作）
			if serr := a.St.TrackSetState(res.Bvid, "failed", err.Error(), store.Now()); serr != nil {
				a.Log.Warn("跟踪状态回写失败", "bvid", res.Bvid, "err", serr)
			}
			return fmt.Errorf("下载失败（元数据已归档）: %w", err)
		}
		if serr := a.St.TrackSetState(res.Bvid, "done", "", store.Now()); serr != nil {
			a.Log.Warn("跟踪状态回写失败", "bvid", res.Bvid, "err", serr)
		}
		// 新内容落库后异步刷新媒体库软链接（幂等，失败不影响任务）；
		// 去抖合并：批量下载时 N 次完成只触发实际重建一轮+补跑，不再 N 遍全库遍历
		if a.Cfg.Media.Enabled {
			go a.Med.RebuildAllDebounced()
		}
	}
	progress(1, "完成")
	return nil
}

// refreshCard 归档/下载后刷新快速预览卡片（derived 层，失败只告警不阻塞归档）。
func (a *App) refreshCard(ctx context.Context, bvid string) {
	if bvid == "" {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			a.Log.Warn("预览卡片生成 panic（已忽略）", "bvid", bvid, "panic", r)
		}
	}()
	if _, err := a.Card.RenderToFile(ctx, bvid); err != nil {
		a.Log.Warn("预览卡片生成失败", "bvid", bvid, "err", err)
	}
}

func (a *App) jobDownload(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	var p struct {
		Bvid string `json:"bvid"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	if err := a.Dl.DownloadVideo(ctx, p.Bvid, func(f float64, detail string) { progress(f, detail) }); err != nil {
		return err
	}
	a.refreshCard(ctx, p.Bvid)
	if n, err := a.Img.SyncVideo(ctx, p.Bvid); err != nil {
		a.Log.Warn("图片本地化失败", "bvid", p.Bvid, "err", err)
	} else if n > 0 {
		a.Log.Info("图片本地化完成", "bvid", p.Bvid, "新增", n)
	}
	return nil
}

// jobFetchImages 全库图片补档：封面+UP主头像+评论者头像落盘 media/（幂等，已存在跳过）。
func (a *App) jobFetchImages(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	n, err := a.Img.Backfill(ctx, func(done, total int) {
		progress(float64(done)/float64(total), fmt.Sprintf("%d/%d", done, total))
	})
	if err != nil {
		return err
	}
	a.Log.Info("图片补档完成", "新增", n)
	progress(1, "完成")
	return nil
}

func (a *App) jobSyncSource(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	var p struct {
		SourceID int64 `json:"source_id"`
		Download *bool `json:"download"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	src, err := a.St.GetSource(p.SourceID)
	if err != nil {
		return err
	}
	progress(0.02, "展开源 "+src.Title)
	res, err := a.ExpandSource(ctx, src)
	if err != nil {
		return err
	}
	total := len(res.Bvids)
	if total == 0 {
		_ = a.St.SetSourceSynced(src.ID, store.Now())
		progress(1, "源为空")
		return nil
	}
	dl := a.Cfg.Download.AutoDownload
	manual := p.Download != nil
	if manual {
		dl = *p.Download
	}
	// 触发器：手动指定 download 参数的同步不产生跟踪记录（跟踪列表只记自动触发）
	trigger := !manual && triggerOn(src.AutoDownload, dl)
	now := store.Now()
	archived := 0
	for i, bvid := range res.Bvids {
		v, verr := a.St.GetVideo(bvid)
		if verr != nil {
			// 新视频：触发器补档，全量归档；命中触发则计入跟踪列表
			if _, err := a.Mgr.Submit("archive", map[string]any{"bvid": bvid, "download": trigger || manual && dl, "source_id": src.ID}); err == nil {
				archived++
				if trigger {
					if terr := a.St.TrackAdd(bvid, src.ID, now); terr != nil {
						a.Log.Warn("跟踪列表记录失败", "bvid", bvid, "err", terr)
					}
				}
			}
		} else {
			// 已有视频：按源的分类型间隔计算到期范围
			stats, comments, danmaku, subs := a.dueParts(src, v, now)
			if comments || danmaku || subs {
				if _, err := a.Mgr.Submit("archive", map[string]any{
					"bvid": bvid, "download": dl, "source_id": src.ID,
					"refresh_comments": comments, "refresh_danmaku": danmaku, "refresh_subtitles": subs,
				}); err == nil {
					archived++
				}
			} else if stats {
				if _, err := a.Mgr.Submit("touch_stats", map[string]any{"bvid": bvid}); err == nil {
					archived++
				}
			}
		}
		progress(float64(i+1)/float64(total), fmt.Sprintf("%d/%d", i+1, total))
	}
	// UP主源：按源级勾选范围同步动态/图文专栏，并按需归档动态视频（v10）。
	if src.Type == "space" {
		if sc := collector.ParseSpaceScope(src.SpaceScope); sc.Dynamic || sc.Article || sc.DynamicVideo {
			progress(0.9, "同步动态/专栏")
			if mid, perr := strconv.ParseInt(src.ExtID, 10, 64); perr == nil {
				if dynBvids, derr := a.Col.SyncSpaceDynamics(ctx, mid); derr != nil {
					a.Log.Warn("动态/专栏同步失败（不影响视频归档）", "source", src.Title, "err", derr)
				} else if sc.DynamicVideo {
					dynNew := 0
					for _, bvid := range dynBvids {
						if _, verr := a.St.GetVideo(bvid); verr == nil {
							continue // 已在库（投稿视频链路覆盖）
						}
						if _, err := a.Mgr.Submit("archive", map[string]any{"bvid": bvid, "download": dl, "source_id": src.ID}); err == nil {
							dynNew++
							archived++
						}
					}
					if dynNew > 0 {
						a.Log.Info("动态视频归档", "source", src.Title, "新增", dynNew)
					}
				}
			}
		}
	}
	_ = a.St.SetSourceSynced(src.ID, store.Now())
	// 仅一次模式：完成首次同步后自动停止监控
	if src.SyncMode == "once" {
		_ = a.St.SetSourceWatch(src.ID, false)
		a.Log.Info("仅一次模式：已完成首次同步，停止监控", "source", src.Title)
	}
	a.Log.Info("源同步完成", "source", src.Title, "视频数", total, "本轮归档", archived)
	progress(1, "完成")
	return nil
}

// triggerOn 源级新视频触发器三态解析：1=开，-1=关，0=跟随全局 download.auto_download。
func triggerOn(srcAuto int, global bool) bool {
	switch srcAuto {
	case 1:
		return true
	case -1:
		return false
	default:
		return global
	}
}

// dueParts 计算某视频当前到期需要刷新的部分。
// 间隔语义：-1=从不；0=跟随源主间隔；主间隔 0=全局默认。统计始终跟随主间隔。
func (a *App) dueParts(src *store.Source, v *store.Video, now int64) (stats, comments, danmaku, subs bool) {
	master := src.SyncIntervalMinutes
	if master <= 0 {
		master = a.Cfg.Collector.SyncDefaultMinutes
	}
	iv := func(srcIv int) int {
		if srcIv < 0 {
			return -1
		}
		if srcIv == 0 {
			return master
		}
		return srcIv
	}
	due := func(last int64, minutes int) bool {
		if minutes < 0 {
			return false
		}
		return last == 0 || now-last >= int64(minutes)*60
	}
	// 自适应冷却：按视频新发程度与统计增速伸缩各部分有效间隔
	// （adaptive_sync=false 时倍率恒 1，等价固定间隔）。倍率每视频只算一次。
	mult := a.syncMultiplier(v, now)
	stats = due(v.LastStatsAt, stretchInterval(master, mult))
	comments = due(v.LastCommentsAt, stretchInterval(iv(src.IntervalCommentsMin), mult))
	danmaku = due(v.LastDanmakuAt, stretchInterval(iv(src.IntervalDanmakuMin), mult))
	subs = due(v.LastSubsAt, stretchInterval(iv(src.IntervalSubsMin), mult))
	return
}

// touchStats 占位已移除：老视频统计走 sync_stats_all。

// jobTouchStats 单视频轻量统计刷新。
func (a *App) jobTouchStats(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	var p struct {
		Bvid string `json:"bvid"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	if err := a.Col.SyncVideoStats(ctx, p.Bvid); err != nil {
		return err
	}
	progress(1, "完成")
	return nil
}

func boolOr(b *bool) bool { return b != nil && *b }

func (a *App) jobSyncStatsAll(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	videos, err := a.St.ListVideos(0, "", 0, 0)
	if err != nil {
		return err
	}
	now := store.Now()
	skipped := 0
	for i, v := range videos {
		// 自适应冷却：沉寂/老旧视频的统计快照拉长周期，跳过本轮（见 adaptive.go）。
		if a.statsSkipAdaptive(v, now) {
			skipped++
			progress(float64(i+1)/float64(len(videos)), fmt.Sprintf("%d/%d 冷却跳过", i+1, len(videos)))
			continue
		}
		if err := a.Col.SyncVideoStats(ctx, v.Bvid); err != nil {
			a.Log.Warn("统计重同步失败", "bvid", v.Bvid, "err", err)
		}
		progress(float64(i+1)/float64(len(videos)), fmt.Sprintf("%d/%d", i+1, len(videos)))
	}
	a.Log.Info("全库统计完成", "总数", len(videos), "刷新", len(videos)-skipped, "冷却跳过", skipped)
	return nil
}

// jobFetchLevels 等级补档：评论等级从 raw 快照离线回填；UP主等级走 space 接口（需登录）。
func (a *App) jobFetchLevels(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	progress(0.02, "扫描评论快照")
	n, err := a.Lv.CommentLevelsFromSnapshots(ctx, func(done, total int) {
		progress(0.02+float64(done)/float64(total)*0.88, fmt.Sprintf("快照 %d/%d", done, total))
	})
	if err != nil {
		return err
	}
	a.Log.Info("评论等级回填完成", "回填", n)
	progress(0.92, "拉取UP主等级")
	updated, skipped, err := a.Lv.OwnerLevels(ctx)
	if err != nil {
		return fmt.Errorf("评论等级回填 %d 行，但 UP主等级拉取失败: %w", n, err)
	}
	if skipped {
		progress(1, "完成（未登录，UP主等级跳过）")
		return nil
	}
	a.Log.Info("UP主等级拉取完成", "更新", updated)
	progress(1, "完成")
	return nil
}

// ExpandSource 用 resolver 展开源。
func (a *App) ExpandSource(ctx context.Context, src *store.Source) (*resolver.Expanded, error) {
	in := &resolver.Input{Type: resolver.Type(src.Type), ExtID: src.ExtID}
	if src.Extra != "" && src.Extra != "{}" {
		extra := map[string]string{}
		if err := json.Unmarshal([]byte(src.Extra), &extra); err == nil {
			in.Extra = extra
		}
	}
	return resolver.Expand(ctx, a.API, in)
}

// HTTPHandler 组装完整 HTTP 出口（测试与 Serve 共用）。
func (a *App) HTTPHandler() http.Handler {
	if a.Srv == nil {
		a.Srv = server.New(server.Deps{
			Cfg: a.Cfg, St: a.St, API: a.API, Col: a.Col, Dl: a.Dl,
			Mgr: a.Mgr, Bus: a.Bus, Med: a.Med, Card: a.Card, Log: a.Log,
			Teri: a.Teri, ConfigPath: a.CfgPath,
		})
	}
	return a.Srv.Handler()
}

// Serve 启动 HTTP 服务（阻塞）。TeriTeri 多端站点（已构建时）在二级端口一并托管。
func (a *App) Serve(ctx context.Context) error {
	handler := a.HTTPHandler()
	if a.Teri != nil {
		a.Teri.Attach(handler)
		a.Teri.StartSites() // dist 已存在则立即上线；否则待一键安装完成后自动拉起
	}
	httpSrv := &http.Server{Addr: a.Cfg.Listen, Handler: handler}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	a.Log.Info("bili-saver 开始服务", "listen", a.Cfg.Listen,
		"teriteri", a.Cfg.Teriteri.Enabled, "media", a.Cfg.Media.Enabled)
	err := httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
