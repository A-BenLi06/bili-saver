// Package collector 执行归档编排：视频元数据/标签/评论/弹幕/字幕/官方AI小结，
// 原始响应全部先落快照；重同步时做评论与弹幕的“消失检测”。
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/bilicats"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"bili-saver/internal/taskflow"
)

// Emitter 由插件总线实现；collector 只依赖接口避免循环引用。
type Emitter interface {
	Emit(event string, payload map[string]any)
}

type noopEmitter struct{}

func (noopEmitter) Emit(string, map[string]any) {}

// Collector 归档执行器。
type Collector struct {
	St           *store.Store
	API          *biliapi.Client
	Cfg          *config.Config
	Log          *slog.Logger
	Emitter      Emitter
	gates        videoGates
	commentSlots chan struct{}
}

// New 组装 collector，默认 emitter 为空实现。
func New(st *store.Store, api *biliapi.Client, cfg *config.Config, log *slog.Logger, em Emitter) *Collector {
	if em == nil {
		em = noopEmitter{}
	}
	if log == nil {
		log = slog.Default()
	}
	n := max(1, min(4, cfg.Collector.CommentConcurrency))
	return &Collector{St: st, API: api, Cfg: cfg, Log: log, Emitter: em, commentSlots: make(chan struct{}, n)}
}

// ---- 主流程 ----

// Result 汇总单视频归档结果。
type Result struct {
	Bvid            string
	New             bool
	Gone            bool
	CommentsSeen    int
	CommentsDeleted int64
	DanmakuSeen     int
	DanmakuDeleted  int64
	Warnings        []string `json:"warnings,omitempty"`
	Complete        bool     // 本轮分页是否完整（决定是否做消失检测）
}

var reAVNum = regexp.MustCompile(`^av([0-9]+)$`)

// CollectOptions 控制重同步时刷新哪些部分。
// 首次归档不受限制（全部采集）；统计与元数据始终刷新。
type CollectOptions struct {
	RefreshComments  bool
	RefreshDanmaku   bool
	RefreshSubtitles bool
}

// All 全部刷新。
func AllOptions() CollectOptions { return CollectOptions{true, true, true} }

// ArchiveVideo 全量归档（首次与重同步同一路径，删除检测自动区分首次）。
func (c *Collector) ArchiveVideo(ctx context.Context, id string) (*Result, error) {
	return c.ArchiveVideoOpts(ctx, id, AllOptions())
}

// ArchiveVideoOpts 按范围归档。换源兼容：即使范围跳过弹幕/字幕，
// 只要检测到新增/变化的 cid（UP 重新投稿、改分P），这些分P仍会强制补采。
func (c *Collector) ArchiveVideoOpts(ctx context.Context, id string, opts CollectOptions) (*Result, error) {
	taskflow.Report(ctx, 0.01, "读取视频信息")
	res := &Result{Bvid: id}
	// 统一转成 bvid
	bvid := id
	if m := reAVNum.FindStringSubmatch(strings.ToLower(id)); m != nil {
		aid, _ := strconv.ParseInt(m[1], 10, 64)
		v, err := c.apiViewByAid(ctx, aid)
		if err != nil {
			return nil, err
		}
		bvid = v.Bvid
		res.Bvid = bvid
	}

	release, err := c.gates.acquire(ctx, bvid)
	if err != nil {
		return nil, err
	}
	defer release()
	_ = c.API.EnsureBuvid(ctx)

	syncStart := store.Now()
	prev, prevErr := c.St.GetVideo(bvid)
	isNew := errors.Is(prevErr, store.ErrNotFound)
	if prevErr != nil && !isNew {
		return nil, prevErr
	}
	res.New = isNew
	// 保证 syncStart 严格单调：同秒内的重复同步也能正确做消失检测
	// （本轮见到的行 last_seen=syncStart，上一轮的行必然 < 本轮 syncStart）。
	if prev != nil && syncStart <= prev.LastSyncAt {
		syncStart = prev.LastSyncAt + 1
	}

	view, err := c.API.VideoView(ctx, bvid)
	if err != nil {
		if c.handleGone(bvid, err, res) {
			return res, nil
		}
		return nil, err
	}

	c.Log.Info("归档视频", "bvid", bvid, "title", view.Title)

	// 视频主记录（时间戳统一用单调 syncStart，保证同秒内多次同步也严格递增）
	now := store.Now()
	v := &store.Video{
		Bvid:            view.Bvid,
		Aid:             view.Aid,
		Title:           view.Title,
		Description:     view.Desc,
		Pic:             view.Pic,
		OwnerMid:        view.OwnerOwner.Mid,
		OwnerName:       view.OwnerOwner.Name,
		OwnerFace:       view.OwnerOwner.Face,
		Pubdate:         view.Pubdate,
		Duration:        view.Duration,
		PagesCount:      len(view.Pages),
		Status:          "ok",
		FirstArchivedAt: now,
		LastSyncAt:      syncStart,
		Tid:             int64(view.TID),
		Tname:           pickTname(view),
		Orientation:     orientationOf(view.Dimension),
	}
	if prev != nil {
		v.FirstArchivedAt = prev.FirstArchivedAt
		v.Downloaded = prev.Downloaded
		v.LocalPath = prev.LocalPath
		v.LastCommentsAt = prev.LastCommentsAt
		v.LastDanmakuAt = prev.LastDanmakuAt
		v.LastSubsAt = prev.LastSubsAt
		v.SourceID = prev.SourceID
	}
	v.LastStatsAt = syncStart
	if err := c.St.UpsertVideo(v); err != nil {
		return nil, err
	}

	// 分P兜底：部分老视频/接口只返回顶层 cid 而无 pages 数组，
	// 此时按单 P 处理，避免弹幕/字幕/下载整体丢失。
	if len(view.Pages) == 0 && view.CID != 0 {
		view.Pages = []biliapi.PageItem{{
			Cid: view.CID, Page: 1, Part: view.Title, Duration: view.Duration,
		}}
	}

	// 分P：与库内已有 cid 对比，找出新增/变化的 cid（换源/重投稿检测）
	prevCids := map[int64]bool{}
	if prev != nil {
		if oldPages, err := c.St.ListPages(view.Bvid); err == nil {
			for _, op := range oldPages {
				prevCids[op.Cid] = true
			}
		}
	}
	changedCids := map[int64]bool{}
	currentPages := make([]*store.Page, 0, len(view.Pages))
	for _, p := range view.Pages {
		currentPages = append(currentPages, &store.Page{Bvid: view.Bvid, Cid: p.Cid, Page: p.Page})
		if !prevCids[p.Cid] && prev != nil {
			changedCids[p.Cid] = true // 全新视频不算变化
		}
		if err := c.St.UpsertPage(&store.Page{
			Bvid: view.Bvid, Cid: p.Cid, Page: p.Page, Part: p.Part,
			Duration: p.Duration, FirstSeenAt: now, LastSeenAt: now,
		}); err != nil {
			return nil, err
		}
	}
	if err := c.St.ObservePages(view.Bvid, currentPages, syncStart); err != nil {
		return res, err
	}
	if len(changedCids) > 0 {
		c.Log.Info("检测到分P/cid 变化（换源或重投稿）", "bvid", bvid, "cids", len(changedCids))
	}

	// 统计快照（时间序列）
	st := view.StatStat
	if err := c.St.AppendStats(&store.StatsSnapshot{
		Bvid: view.Bvid, Ts: now,
		View: st.View, Danmaku: st.Danmaku, Reply: st.Reply,
		Favorite: st.Favorite, Coin: st.Coin, Share: st.Share, Like: st.Like,
	}); err != nil {
		return nil, err
	}

	taskflow.Report(ctx, 0.05, "保存视频信息与统计")
	// 标签
	if tags, err := c.API.VideoTags(ctx, view.Aid, view.Bvid); err == nil {
		names := make([]string, 0, len(tags))
		for _, t := range tags {
			names = append(names, t.NameOr())
		}
		if err := c.St.AddTags(view.Bvid, names, "bilibili"); err != nil {
			c.Log.Warn("标签入库失败", "bvid", bvid, "err", err)
		}
	} else {
		c.Log.Warn("标签拉取失败", "bvid", bvid, "err", err)
	}

	// 官方 AI 小结（B 站原生数据，归 origin 层）
	if concl, err := c.API.Conclusion(ctx, view.Bvid, view.CID, view.OwnerOwner.Mid); err == nil {
		outline, _ := json.Marshal(concl.ModelResult.Outline)
		model := "unknown"
		for k := range concl.ModelList {
			model = k
			break
		}
		if err := c.St.UpsertAIConclusion(view.Bvid, model, concl.ModelResult.Summary, string(outline)); err != nil {
			c.Log.Warn("AI小结入库失败", "bvid", bvid, "err", err)
		}
	}

	taskflow.Report(ctx, 0.08, "采集弹幕")
	// 弹幕：范围外跳过，但变化的 cid 强制补采
	complete := true
	danmakuDone := len(view.Pages) > 0
	cidComplete := map[int64]bool{}
	cidSeen := map[int64]int{} // 每个 cid 本轮见到的弹幕数（消失防御）
	for _, p := range view.Pages {
		force := changedCids[p.Cid]
		if !opts.RefreshDanmaku && !force {
			complete = false
			danmakuDone = false
			continue
		}
		seen, comp, err := c.collectDanmaku(ctx, view.Bvid, p.Cid, p.Duration, syncStart, isNew)
		cidComplete[p.Cid] = comp && err == nil
		if rerr := c.St.RecordCollectionStatus(view.Bvid, p.Cid, "danmaku", syncStart, comp, err); rerr != nil {
			return res, rerr
		}
		if err != nil {
			c.Log.Warn("弹幕抓取不完整", "bvid", bvid, "cid", p.Cid, "err", err)
			res.Warnings = append(res.Warnings, err.Error())
			danmakuDone = false
			complete = false
		}
		res.DanmakuSeen += seen
		cidSeen[p.Cid] = seen
		if !comp {
			complete = false
		}
		if c.Cfg.View().Download.DownloadDanmaku {
			if err := c.writeDanmakuXMLContext(ctx, view.Bvid, p.Cid); err != nil {
				c.Log.Warn("弹幕 XML 生成失败", "bvid", bvid, "cid", p.Cid, "err", err)
			}
		}
	}
	res.Complete = complete

	taskflow.Report(ctx, 0.12, "采集字幕")
	// 字幕（B 站 CC）；无 CC 字幕的分P发 subtitle.missing 事件（whisper 等插件订阅）
	subsDone := len(view.Pages) > 0
	for _, p := range view.Pages {
		force := changedCids[p.Cid]
		if !opts.RefreshSubtitles && !force {
			subsDone = false
			continue
		}
		subErr := c.collectSubtitles(ctx, view.Bvid, view.Aid, p.Cid)
		if rerr := c.St.RecordCollectionStatus(view.Bvid, p.Cid, "subtitles", syncStart, subErr == nil, subErr); rerr != nil {
			return res, rerr
		}
		if err := subErr; err != nil {
			subsDone = false
			res.Warnings = append(res.Warnings, err.Error())
			c.Log.Warn("字幕抓取失败", "bvid", bvid, "cid", p.Cid, "err", err)
		}
		has, _ := c.St.HasCCSubtitle(p.Cid)
		if !has && subErr == nil {
			c.Emitter.Emit("subtitle.missing", map[string]any{
				"bvid": view.Bvid, "cid": p.Cid, "page": p.Page,
			})
		}
	}

	// 评论
	commentsDone := false
	var commentScan *commentScanResult
	commentsComplete := false
	if opts.RefreshComments {
		taskflow.Report(ctx, 0.15, "开始评论扫描")
		var err error
		commentScan, err = c.scanComments(ctx, view.Aid, view.Bvid, syncStart, isNew)
		commentsDone = err == nil
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			c.Log.Warn("评论抓取不完整", "bvid", bvid, "err", err)
			if ctx.Err() != nil {
				return res, err
			}
		}
		if commentScan != nil {
			res.CommentsSeen = commentScan.Scan.Seen
			commentsComplete = commentScan.Complete && err == nil
		}
		// 评论全文索引批量补齐（每轮一次，幂等；失败不阻断，下轮自愈）
		if n, err := c.St.HealVideoCommentsFTS(view.Bvid); err != nil {
			c.Log.Warn("评论FTS索引补齐失败", "bvid", bvid, "err", err)
		} else if n > 0 {
			c.Log.Info("评论FTS索引补齐", "bvid", bvid, "新增", n)
		}
	}
	if opts.RefreshComments {
		if err := c.St.RecordCollectionStatus(view.Bvid, 0, "comments", syncStart, commentsComplete, func() error {
			if commentsDone {
				return nil
			}
			return errors.New("comment scan incomplete")
		}()); err != nil {
			return res, err
		}
	}
	res.Complete = complete && (!opts.RefreshComments || commentsComplete) && subsDone

	taskflow.Report(ctx, 0.35, "保存同步结果与核对状态")
	// 分类型同步时间戳（完成的部分才更新）
	if err := c.St.UpdateVideoSyncTimes(view.Bvid, true, commentsDone, danmakuDone, subsDone, syncStart); err != nil {
		c.Log.Warn("同步时间戳更新失败", "bvid", bvid, "err", err)
	}

	// Each data type requires its own complete coverage before reconciliation.
	if commentsComplete {
		delC, anomalous, err := c.St.CompleteCommentScan(commentScan.Scan, store.Now())
		if err != nil {
			return res, err
		}
		if anomalous {
			c.Log.Warn("评论单轮消失占比异常（疑似接口截断），本轮不标删，待下轮确认",
				"bvid", bvid, "本轮见到", res.CommentsSeen)
		}
		res.CommentsDeleted = delC
	} else if commentScan != nil && commentScan.Finished {
		if err := c.finishCommentScan(commentScan, 0); err != nil {
			return res, err
		}
	}
	{
		for _, p := range view.Pages {
			if !cidComplete[p.Cid] {
				continue
			}
			delD, anomD, err := c.St.MarkMissingDanmakuGuarded(view.Bvid, p.Cid, syncStart, isNew, cidSeen[p.Cid])
			if err != nil {
				return res, err
			}
			if anomD {
				c.Log.Warn("弹幕单轮消失数异常（疑似 seg→list 降级），本轮不标删",
					"bvid", bvid, "cid", p.Cid, "本轮见到", cidSeen[p.Cid])
			}
			res.DanmakuDeleted += delD
		}
	}
	if res.CommentsDeleted > 0 || res.DanmakuDeleted > 0 {
		c.Emitter.Emit("comments.resynced", map[string]any{
			"bvid":             view.Bvid,
			"deleted_comments": res.CommentsDeleted,
			"deleted_danmaku":  res.DanmakuDeleted,
		})
	}

	// 更新 FTS
	if err := c.St.ReindexVideoFTS(v); err != nil {
		c.Log.Warn("FTS 索引失败", "bvid", bvid, "err", err)
	}

	evPayload := map[string]any{
		"bvid": view.Bvid, "new": res.New, "gone": false,
		"title": view.Title, "description": view.Desc,
		"owner_mid": view.OwnerOwner.Mid, "owner_name": view.OwnerOwner.Name,
	}
	c.Emitter.Emit("video.archived", evPayload)
	if res.New {
		c.Emitter.Emit("video.discovered", evPayload)
	}
	return res, nil
}

// SyncVideoStats 轻量重同步：仅更新元数据与统计序列（不做评论全量翻页）。
func (c *Collector) SyncVideoStats(ctx context.Context, bvid string) error {
	release, err := c.gates.acquire(ctx, bvid)
	if err != nil {
		return err
	}
	defer release()
	view, err := c.API.VideoView(ctx, bvid)
	if err != nil {
		if c.handleGone(bvid, err, &Result{Bvid: bvid}) {
			return nil
		}
		return err
	}
	now := store.Now()
	st := view.StatStat
	if err := c.St.AppendStats(&store.StatsSnapshot{
		Bvid: view.Bvid, Ts: now,
		View: st.View, Danmaku: st.Danmaku, Reply: st.Reply,
		Favorite: st.Favorite, Coin: st.Coin, Share: st.Share, Like: st.Like,
	}); err != nil {
		return err
	}
	// 一次窄 UPDATE 替代 GetVideo + 全字段 Upsert + 同步时间 UPDATE。
	return c.St.UpdateStatsMetadata(view.Bvid, now, int64(view.TID), pickTname(view), orientationOf(view.Dimension))
}

// pickTname 分区名：官方 tname 优先，空串时回退 tname_v2，再回退本地分区表
// （bilicats，实测收割的官方映射；view 接口当前普遍不返回分区名，见 CLAIMS 2026-09-09）。
func pickTname(view *biliapi.VideoView) string {
	if view.TName != "" {
		return view.TName
	}
	if view.TNameV2 != "" {
		return view.TNameV2
	}
	return bilicats.Name(view.TID)
}

// orientationOf 由 dimension 派生横竖屏；rotate 90/270 时宽高语义互换；
// 尺寸未知（0）返回空串，不猜测。
func orientationOf(d biliapi.Dimension) string {
	w, h := d.Width, d.Height
	if w <= 0 || h <= 0 {
		return ""
	}
	if d.Rotate == 90 || d.Rotate == 270 {
		w, h = h, w
	}
	if h > w {
		return "vertical"
	}
	return "horizontal"
}

func (c *Collector) apiViewByAid(ctx context.Context, aid int64) (*biliapi.VideoView, error) {
	return c.API.VideoViewAid(ctx, aid)
}

// handleGone 当接口返回“不存在/不可见”时把已有记录标 gone。
// 返回 true 表示已按 gone 处理。
func (c *Collector) handleGone(bvid string, err error, res *Result) bool {
	apiErr, ok := err.(*biliapi.APIError)
	if !ok {
		return false
	}
	switch apiErr.Code {
	case -404, 62002, 62004, -403, -409:
		// UPDATE 对未归档视频为空操作，无需再读取整行（简介等大字段）。
		if dbErr := c.St.MarkVideoGone(bvid, "gone"); dbErr != nil {
			c.Log.Warn("视频消失状态保存失败", "bvid", bvid, "err", dbErr)
		}
		res.Gone = true
		c.Log.Info("视频已不可见", "bvid", bvid, "code", apiErr.Code)
		c.Emitter.Emit("video.gone", map[string]any{"bvid": bvid, "code": apiErr.Code})
		return true
	}
	return false
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
