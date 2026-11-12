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

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/biliapi"
	"bili-saver/internal/card"
	"bili-saver/internal/collector"
	"bili-saver/internal/config"
	"bili-saver/internal/downloader"
	"bili-saver/internal/images"
	"bili-saver/internal/jobs"
	"bili-saver/internal/levels"
	"bili-saver/internal/media"
	"bili-saver/internal/metrics"
	"bili-saver/internal/nodeauth"
	"bili-saver/internal/plugin"
	"bili-saver/internal/resolver"
	"bili-saver/internal/server"
	"bili-saver/internal/store"
	"bili-saver/internal/teriteriweb"
	"sync"
	"time"
)

// App 持有全部组件引用。
type App struct {
	guard       *archiveguard.Lock
	vault       *nodeauth.Vault
	pluginToken string
	closeOnce   sync.Once
	closeError  error
	Cfg         *config.Config
	Log         *slog.Logger
	St          *store.Store
	API         *biliapi.Client
	Col         *collector.Collector
	Dl          *downloader.Downloader
	Mgr         *jobs.Manager
	Bus         *plugin.Bus
	Med         *media.Bridge
	Card        *card.Generator
	Img         *images.Localizer
	Lv          *levels.Backfiller
	Srv         *server.Server
	Teri        *teriteriweb.Web
	// CfgPath 配置文件路径（serve 时由 CLI 注入）；非空时设置页 PATCH config 会写盘。
	CfgPath string
}

// asyncEmitter 让业务事件不阻塞在插件执行上。
type asyncEmitter struct{ b *plugin.Bus }

func (a asyncEmitter) Emit(ev string, p map[string]any) { a.b.EmitAsync(ev, p) }

// New 初始化全部组件（不开服务、不启动 worker）。
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	guard, err := archiveguard.Acquire(cfg.View().DataDir)
	if err != nil {
		return nil, err
	}
	constructed := false
	defer func() {
		if !constructed {
			guard.Close()
		}
	}()

	if err := mkdirs(cfg); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(cfg.View().DataDir, "bili-saver.db"))
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	overrides, _, err := st.RuntimeSettings()
	if err != nil {
		st.Close()
		return nil, err
	}
	if err = cfg.Prepare(overrides); err != nil {
		st.Close()
		return nil, err
	}
	vault, err := nodeauth.Open(cfg.View().DataDir)
	if err == nil {
		err = vault.Bootstrap(st, cfg)
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	cfg.MarkApplied()
	pluginToken := nodeauth.Token()
	api, err := biliapi.New(cfg.View().CookiePath(), cfg.View().Bilibili.MinReqMs, cfg.View().Bilibili.UserAgent)
	if err != nil {
		st.Close()
		return nil, err
	}
	// CDN has its own transport; keep its cookie jar and timeout policy intact.
	api.DownloadClient().Transport = metrics.InstrumentTransport(api.DownloadClient().Transport.(*http.Transport))
	bus := plugin.NewBus(st, st, cfg.View().Plugins.Dir, log, plugin.Context{
		DataDir: cfg.View().DataDir,
		APIBase: cfg.LocalURL(),
		Token:   pluginToken,
	})
	col := collector.New(st, api, cfg, log, asyncEmitter{bus})
	api.SetSnapshotHook(col.SnapshotHook())
	dl := downloader.New(st, api, cfg, log, asyncEmitter{bus})
	mgr := jobs.NewManager(st, log, cfg.View().Download.Concurrency)
	med := media.New(st, cfg, log)
	cardGen := card.New(st, cfg, log)
	img := images.New(st, cfg.View().DataDir, cfg.View().Bilibili.UserAgent, log)
	lv := levels.New(st, cfg.View().DataDir, api, log)
	if err := st.EnsureDefaultLibrary(cfg.View().Media.Root, cfg.View().Media.Layout, cfg.View().Media.LinkMode); err != nil {
		log.Warn("默认媒体库迁移失败", "err", err)
	}
	a := &App{guard: guard, vault: vault, pluginToken: pluginToken, Cfg: cfg, Log: log, St: st, API: api, Col: col, Dl: dl, Mgr: mgr, Bus: bus, Med: med, Card: cardGen, Img: img, Lv: lv}
	if cfg.View().Teriteri.Enabled {
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
	constructed = true
	a.registerJobs()
	if cfg.View().Plugins.Enabled {
		if err := a.Bus.Reload(); err != nil {
			log.Warn("插件加载失败", "err", err)
		}
	}
	return a, nil
}

// ReloadPlugins 扫描插件目录（启用时）。
func (a *App) ReloadPlugins() error {
	if !a.Cfg.View().Plugins.Enabled {
		return nil
	}
	return a.Bus.Reload()
}

func mkdirs(cfg *config.Config) error {
	for _, d := range []string{cfg.View().DataDir, cfg.View().Media.Root, cfg.View().Plugins.Dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// registerJobs 注册任务类型。
func (a *App) registerJobs() {
	a.Mgr.RegisterVideo("archive", a.jobArchive)
	a.Mgr.RegisterVideo("download", a.jobDownload)
	a.Mgr.RegisterVideo("user_download", a.jobUserDownload)
	a.Mgr.Register("sync_source", a.jobSyncSource)
	a.Mgr.Register("sync_stats_all", a.jobSyncStatsAll)
	a.Mgr.RegisterVideo("touch_stats", a.jobTouchStats)
	a.Mgr.PreferAtYield("sync_source", "touch_stats")
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
	progress(0.01, "开始归档")
	res, err := a.Col.ArchiveVideoOpts(ctx, p.Bvid, opts)
	if err != nil {
		return err
	}
	userSource := false
	if p.SourceID != 0 {
		if src, err := a.St.GetSource(p.SourceID); err == nil {
			if err := a.attachSourceVideo(src, res.Bvid, false, j.Priority); err != nil {
				return err
			}
			if u, err := a.St.GetUser(src.UserID); err == nil {
				userSource = u.Role == "user"
			}
		}
		_, _ = a.St.DB.Exec(`UPDATE videos SET source_id=? WHERE bvid=? AND source_id=0`, p.SourceID, res.Bvid)
	}
	// 图片本地化（封面/头像落盘 media/，绕开图床防盗链；失败不阻塞归档）
	if !res.Gone {
		progress(0.36, "本地化封面与头像")
		if n, err := a.Img.SyncVideo(ctx, res.Bvid); err != nil {
			a.Log.Warn("图片本地化失败", "bvid", res.Bvid, "err", err)
		} else if n > 0 {
			a.Log.Info("图片本地化完成", "bvid", res.Bvid, "新增", n)
		}
		progress(0.38, "补齐UP主等级")
		if err := a.Lv.EnsureOwnerLevel(ctx, res.Bvid); err != nil {
			a.Log.Warn("UP主等级拉取失败", "bvid", res.Bvid, "err", err)
		}
	}
	progress(0.4, "归档完成")
	a.refreshCard(ctx, res.Bvid)
	dl := a.Cfg.View().Download.AutoDownload
	if p.Download != nil {
		dl = *p.Download
	}
	if dl && !res.Gone && !userSource {
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
		// 新内容落库后异步刷新媒体库软链接（幂等）；去抖合并：批量下载的
		// N 次完成合并为实际一轮重建+补跑，不再 N 遍全库遍历
		if a.Cfg.View().Media.Enabled {
			go func() {
				if err := a.Med.RebuildAllDebounced(); err != nil {
					a.Log.Warn("下载后媒体库刷新失败", "bvid", res.Bvid, "err", err)
				}
			}()
		}
	}
	if len(res.Warnings) > 0 {
		return fmt.Errorf("archive saved with incomplete components: %v", res.Warnings)
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
	userSource := false
	if u, err := a.St.GetUser(src.UserID); err == nil {
		userSource = u.Role == "user"
	}
	var personalScanned bool
	if err := a.St.DB.QueryRow(`SELECT personal_scanned FROM sources WHERE id=?`, src.ID).Scan(&personalScanned); err != nil {
		return err
	}
	total := len(res.Bvids)
	if total == 0 && src.Type != "space" {
		_ = a.St.SetSourceSynced(src.ID, store.Now())
		progress(1, "源为空")
		return nil
	}
	dl := a.Cfg.View().Download.AutoDownload
	manual := p.Download != nil
	if manual {
		dl = *p.Download
	}
	// 触发器：手动指定 download 参数的同步不产生跟踪记录（跟踪列表只记自动触发）
	if userSource {
		dl = false
	}
	trigger := !userSource && !manual && triggerOn(src.AutoDownload, dl)
	now := store.Now()
	videos, err := a.St.VideosByBvids(res.Bvids)
	if err != nil {
		return fmt.Errorf("读取源视频同步状态: %w", err)
	}
	var recent map[string][]*store.StatsSnapshot
	if a.Cfg.View().Collector.AdaptiveSync {
		recent, err = a.St.RecentStatsBatch(res.Bvids)
		if err != nil {
			return fmt.Errorf("读取源视频活跃度: %w", err)
		}
	}
	visited := make(map[string]bool, total)
	archived := 0
	for i, bvid := range res.Bvids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if bvid == "" || visited[bvid] {
			continue
		}
		visited[bvid] = true
		v := videos[bvid]
		allowAuto := userSource && src.AutoDownload != -1 && (src.LastSyncAt == 0 || personalScanned || v == nil)
		if err := a.attachSourceVideo(src, bvid, allowAuto, j.Priority); err != nil {
			return err
		}
		if v == nil {
			// 新视频：触发器补档，全量归档；命中触发则计入跟踪列表
			if _, err := a.Mgr.SubmitPrio("archive", map[string]any{"bvid": bvid, "download": trigger || manual && dl, "source_id": src.ID}, j.Priority); err == nil {
				archived++
				if trigger {
					if terr := a.St.TrackAdd(bvid, src.ID, now); terr != nil {
						a.Log.Warn("跟踪列表记录失败", "bvid", bvid, "err", terr)
					}
				}
			}
		} else {
			// 已有视频：按源的分类型间隔计算到期范围
			mult := 1.0
			if a.Cfg.View().Collector.AdaptiveSync {
				mult = ageFactor(now, v.Pubdate) * activityFactor(recent[bvid])
			}
			stats, comments, danmaku, subs := a.duePartsWithMultiplier(src, v, now, mult)
			if comments || danmaku || subs {
				if _, err := a.Mgr.SubmitPrio("archive", map[string]any{
					"bvid": bvid, "download": dl, "source_id": src.ID,
					"refresh_comments": comments, "refresh_danmaku": danmaku, "refresh_subtitles": subs,
				}, j.Priority); err == nil {
					archived++
				}
			} else if stats {
				payload := map[string]any{"bvid": bvid}
				if v.Status == "gone" {
					payload["recover_parts"], payload["source_id"], payload["download"] = true, src.ID, dl
				}
				if _, err := a.Mgr.SubmitPrio("touch_stats", payload, j.Priority); err == nil {
					archived++
				}
			}
		}
		progress(float64(i+1)/float64(total), fmt.Sprintf("%d/%d", i+1, total))
	}
	// UP主源：按源级勾选范围同步动态/图文专栏，并按需归档动态视频（v10）。
	if src.Type == "space" {
		if sc := collector.ParseSpaceScope(src.SpaceScope); sc.Dynamic || sc.Article || sc.DynamicVideo || sc.Bangumi {
			progress(0.9, "同步动态/专栏/追番")
			if mid, perr := strconv.ParseInt(src.ExtID, 10, 64); perr == nil {
				if sc.Bangumi {
					if berr := a.Col.SyncSpaceBangumi(ctx, mid); berr != nil {
						a.Log.Warn("追番列表同步失败（不影响其他归档）", "source", src.Title, "err", berr)
					}
				}
				if sc.Dynamic || sc.Article || sc.DynamicVideo {
					if dynBvids, derr := a.Col.SyncSpaceDynamics(ctx, mid); derr != nil {
						a.Log.Warn("动态/专栏同步失败（不影响视频归档）", "source", src.Title, "err", derr)
					} else if sc.DynamicVideo {
						dynNew := 0
						for _, bvid := range dynBvids {
							allowAuto := userSource && src.AutoDownload != -1 && (src.LastSyncAt == 0 || personalScanned)
							if err := a.attachSourceVideo(src, bvid, allowAuto, j.Priority); err != nil {
								return err
							}
							if _, verr := a.St.GetVideo(bvid); verr == nil {
								continue // 已在库（投稿视频链路覆盖）
							}
							if _, err := a.Mgr.SubmitPrio("archive", map[string]any{"bvid": bvid, "download": dl, "source_id": src.ID}, j.Priority); err == nil {
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
	}
	_, _ = a.St.DB.Exec(`UPDATE sources SET personal_scanned=1 WHERE id=?`, src.ID)
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
	return a.duePartsWithMultiplier(src, v, now, a.syncMultiplier(v, now))
}

func (a *App) duePartsWithMultiplier(src *store.Source, v *store.Video, now int64, mult float64) (stats, comments, danmaku, subs bool) {
	master := src.SyncIntervalMinutes
	if master <= 0 {
		master = a.Cfg.View().Collector.SyncDefaultMinutes
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
	stats = due(v.LastStatsAt, stretchInterval(master, mult))
	comments = due(v.LastCommentsAt, stretchInterval(iv(src.IntervalCommentsMin), mult))
	danmaku = due(v.LastDanmakuAt, stretchInterval(iv(src.IntervalDanmakuMin), mult))
	subs = due(v.LastSubsAt, stretchInterval(iv(src.IntervalSubsMin), mult))
	if v.Status == "gone" {
		// 已消失视频只做轻量存在性检查；恢复后下轮再补采其他到期部分。
		comments, danmaku, subs = false, false, false
	}
	return
}

// touchStats 占位已移除：老视频统计走 sync_stats_all。

// jobTouchStats 单视频轻量统计刷新。
func (a *App) jobTouchStats(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	var p struct {
		Bvid         string `json:"bvid"`
		RecoverParts bool   `json:"recover_parts"`
		SourceID     int64  `json:"source_id"`
		Download     *bool  `json:"download"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	if err := a.Col.SyncVideoStats(ctx, p.Bvid); err != nil {
		return err
	}
	if p.RecoverParts {
		if err := a.queueRestoredParts(ctx, p.Bvid, p.SourceID, p.Download, j.Priority); err != nil {
			return err
		}
	}
	progress(1, "完成")
	return nil
}

func boolOr(b *bool) bool { return b != nil && *b }

// jobSyncStatsAll 按固定大小分页；统计网络请求仍串行，遵守原接口限速。
func (a *App) jobSyncStatsAll(ctx context.Context, j *store.Job, progress jobs.ProgressFn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	total, before, err := a.St.StatsSweepWindow(ctx)
	if err != nil {
		return err
	}
	const pageSize = 400
	now := store.Now()
	done, skipped := 0, 0
	for before > 0 {
		videos, err := a.St.StatsSweepPage(ctx, before, pageSize)
		if err != nil {
			return err
		}
		if len(videos) == 0 {
			break
		}
		var recent map[string][]*store.StatsSnapshot
		if a.Cfg.View().Collector.AdaptiveSync && a.Cfg.View().Collector.StatsResyncHours > 0 {
			ids := make([]string, 0, len(videos))
			for _, v := range videos {
				if v.LastStatsAt != 0 {
					ids = append(ids, v.Bvid)
				}
			}
			recent, err = a.St.RecentStatsBatch(ids)
			if err != nil {
				return fmt.Errorf("读取统计刷新活跃度: %w", err)
			}
		}
		for _, v := range videos {
			if err := ctx.Err(); err != nil {
				return err
			}
			done++
			mult := ageFactor(now, v.Pubdate) * activityFactor(recent[v.Bvid])
			candidate := store.Video{Bvid: v.Bvid, Pubdate: v.Pubdate, LastStatsAt: v.LastStatsAt}
			if a.statsSkipWithMultiplier(&candidate, now, mult) {
				skipped++
				progress(float64(done)/float64(total), fmt.Sprintf("%d/%d 冷却跳过", done, total))
				continue
			}
			if err := a.Col.SyncVideoStats(ctx, v.Bvid); err != nil {
				a.Log.Warn("统计重同步失败", "bvid", v.Bvid, "err", err)
			}
			progress(float64(done)/float64(total), fmt.Sprintf("%d/%d", done, total))
		}
		before = videos[len(videos)-1].RowID - 1
	}
	a.Log.Info("全库统计完成", "总数", total, "已检查", done, "刷新", done-skipped, "冷却跳过", skipped)
	return ctx.Err()
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
			Teri: a.Teri, ConfigPath: a.CfgPath, Vault: a.vault, PluginToken: a.pluginToken,
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
	httpSrv := &http.Server{Addr: a.Cfg.View().Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	shutdownDone := make(chan struct{})
	serveDone := make(chan struct{})
	defer close(serveDone)
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
		case <-serveDone:
			return
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdown); err != nil {
			_ = httpSrv.Close()
		}
	}()
	a.Log.Info("bili-saver 开始服务", "listen", a.Cfg.View().Listen,
		"teriteri", a.Cfg.View().Teriteri.Enabled, "media", a.Cfg.View().Media.Enabled)
	err := metrics.ListenAndServe(httpSrv)
	if err == http.ErrServerClosed {
		<-shutdownDone
		return nil
	}
	return err
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		if a.Bus != nil {
			a.Bus.Cancel()
		}
		if a.Mgr != nil {
			a.Mgr.Stop()
		}
		if a.Teri != nil {
			a.Teri.Close()
		}
		if a.Bus != nil {
			a.Bus.Wait()
		}
		if a.St != nil {
			a.closeError = a.St.Close()
		}
		if a.guard != nil {
			if err := a.guard.Close(); a.closeError == nil {
				a.closeError = err
			}
		}
	})
	return a.closeError
}
