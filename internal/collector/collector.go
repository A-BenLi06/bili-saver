// Package collector 执行归档编排：视频元数据/标签/评论/弹幕/字幕/官方AI小结，
// 原始响应全部先落快照；重同步时做评论与弹幕的“消失检测”。
package collector

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

// Emitter 由插件总线实现；collector 只依赖接口避免循环引用。
type Emitter interface {
	Emit(event string, payload map[string]any)
}

type noopEmitter struct{}

func (noopEmitter) Emit(string, map[string]any) {}

// Collector 归档执行器。
type Collector struct {
	St      *store.Store
	API     *biliapi.Client
	Cfg     *config.Config
	Log     *slog.Logger
	Emitter Emitter
}

// New 组装 collector，默认 emitter 为空实现。
func New(st *store.Store, api *biliapi.Client, cfg *config.Config, log *slog.Logger, em Emitter) *Collector {
	if em == nil {
		em = noopEmitter{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Collector{St: st, API: api, Cfg: cfg, Log: log, Emitter: em}
}

// ---- 快照（origin 层原始响应） ----

// SnapshotHook 返回挂到 biliapi.Client 的快照回调：gzip 落盘 + 记录行。
func (c *Collector) SnapshotHook() biliapi.SnapshotHook {
	return func(endpoint string, params url.Values, status int, body []byte) {
		ep := strings.TrimPrefix(endpoint, "/")
		ep = strings.ReplaceAll(ep, "/", "_")
		paramsJSON := params.Encode()
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		dir := filepath.Join(c.Cfg.DataDir, "raw", ep, hash[:16])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			c.Log.Warn("快照目录创建失败", "err", err)
			return
		}
		name := fmt.Sprintf("%d-%s.json.gz", time.Now().Unix(), hash[:12])
		full := filepath.Join(dir, name)
		rel, err := filepath.Rel(c.Cfg.DataDir, full)
		if err != nil {
			rel = full
		}
		f, err := os.Create(full)
		if err != nil {
			c.Log.Warn("快照写入失败", "err", err)
			return
		}
		gz := gzip.NewWriter(f)
		_, _ = gz.Write(body)
		_ = gz.Close()
		_ = f.Close()
		_ = c.St.AddSnapshot(&store.Snapshot{
			Endpoint:   endpoint,
			ParamsJSON: paramsJSON,
			RespHash:   hash,
			BodyPath:   rel,
			StatusCode: status,
			FetchedAt:  store.Now(),
		})
	}
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
	Complete        bool // 本轮分页是否完整（决定是否做消失检测）
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
	for _, p := range view.Pages {
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

	// 弹幕：范围外跳过，但变化的 cid 强制补采
	complete := true
	danmakuDone := false
	cidSeen := map[int64]int{} // 每个 cid 本轮见到的弹幕数（消失防御）
	for _, p := range view.Pages {
		force := changedCids[p.Cid]
		if !opts.RefreshDanmaku && !force {
			complete = false
			continue
		}
		seen, comp, err := c.collectDanmaku(ctx, view.Bvid, p.Cid, p.Duration, syncStart, isNew)
		danmakuDone = true
		if err != nil {
			c.Log.Warn("弹幕抓取不完整", "bvid", bvid, "cid", p.Cid, "err", err)
			complete = false
		}
		res.DanmakuSeen += seen
		cidSeen[p.Cid] = seen
		if !comp {
			complete = false
		}
		if c.Cfg.Download.DownloadDanmaku {
			if err := c.writeDanmakuXML(view.Bvid, p.Cid); err != nil {
				c.Log.Warn("弹幕 XML 生成失败", "bvid", bvid, "cid", p.Cid, "err", err)
			}
		}
	}
	res.Complete = complete

	// 字幕（B 站 CC）；无 CC 字幕的分P发 subtitle.missing 事件（whisper 等插件订阅）
	subsDone := false
	for _, p := range view.Pages {
		force := changedCids[p.Cid]
		if !opts.RefreshSubtitles && !force {
			continue
		}
		subsDone = true
		if err := c.collectSubtitles(ctx, view.Bvid, view.Aid, p.Cid); err != nil {
			c.Log.Warn("字幕抓取失败", "bvid", bvid, "cid", p.Cid, "err", err)
		}
		has, _ := c.St.HasCCSubtitle(p.Cid)
		if !has {
			c.Emitter.Emit("subtitle.missing", map[string]any{
				"bvid": view.Bvid, "cid": p.Cid, "page": p.Page,
			})
		}
	}

	// 评论
	commentsDone := false
	if opts.RefreshComments {
		seen, comp, err := c.collectComments(ctx, view.Aid, view.Bvid, syncStart, isNew)
		commentsDone = true
		if err != nil {
			c.Log.Warn("评论抓取不完整", "bvid", bvid, "err", err)
			complete = false
		}
		res.CommentsSeen = seen
		res.Complete = res.Complete && comp
	} else {
		complete = false // 未全量对账，不做消失检测
	}

	// 分类型同步时间戳（完成的部分才更新）
	if err := c.St.UpdateVideoSyncTimes(view.Bvid, true, commentsDone, danmakuDone, subsDone, syncStart); err != nil {
		c.Log.Warn("同步时间戳更新失败", "bvid", bvid, "err", err)
	}

	// 消失检测（仅本轮分页完整时执行；带假消失防御）
	if res.Complete {
		delC, anomalous, err := c.St.MarkMissingCommentsGuarded(view.Bvid, syncStart, isNew, res.CommentsSeen)
		if err != nil {
			return res, err
		}
		if anomalous {
			c.Log.Warn("评论单轮消失占比异常（疑似接口截断），本轮不标删，待下轮确认",
				"bvid", bvid, "本轮见到", res.CommentsSeen)
		}
		res.CommentsDeleted = delC
		for _, p := range view.Pages {
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
		if res.CommentsDeleted > 0 || res.DanmakuDeleted > 0 {
			c.Emitter.Emit("comments.resynced", map[string]any{
				"bvid":             view.Bvid,
				"deleted_comments": res.CommentsDeleted,
				"deleted_danmaku":  res.DanmakuDeleted,
			})
		}
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
	prev, err := c.St.GetVideo(view.Bvid)
	if err != nil {
		return err
	}
	prev.LastSyncAt = now
	if prev.Status != "ok" {
		prev.Status = "ok"
	}
	if err := c.St.UpsertVideo(prev); err != nil {
		return err
	}
	// 统计时间戳必须反映本次轻量刷新，否则统计类冷却判断（adaptive）失真。
	return c.St.UpdateVideoSyncTimes(view.Bvid, true, false, false, false, now)
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
		if _, dbErr := c.St.GetVideo(bvid); dbErr == nil {
			_ = c.St.MarkVideoGone(bvid, "gone")
		}
		res.Gone = true
		c.Log.Info("视频已不可见", "bvid", bvid, "code", apiErr.Code)
		c.Emitter.Emit("video.gone", map[string]any{"bvid": bvid, "code": apiErr.Code})
		return true
	}
	return false
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
