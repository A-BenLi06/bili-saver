// Package media 媒体库桥：把归档库结构化软链接/硬链接到
// Emby / Plex / Jellyfin / Kodi 可刮削的目录布局。NFO 按 Kodi 规范最大化
// 写入 B 站原生数据（origin 忠诚）：标题/简介/官方AI小结/统计快照/标签/
// UP主/来源收藏夹等；派生数据（插件标签等）一律不写入。
package media

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

// Bridge 媒体库桥。
type Bridge struct {
	St  *store.Store
	Cfg *config.Config
	Log *slog.Logger

	// 重建并发控制（两把锁，职责不同，都不嵌套）：
	// rebuildMu 串行化实际重建（直接 RebuildAll 与去抖轮互斥，
	// 消除 linkFile 检查-删除-重建窗口的并发竞争）；
	// stateMu 保护 running/dirty 标记，让下载完成回调能在重建进行中
	// 即刻置脏并返回（合并批量下载的 N 次触发）。
	rebuildMu    sync.Mutex
	stateMu      sync.Mutex
	rebuildBusy  bool
	rebuildDirty bool
}

func New(st *store.Store, cfg *config.Config, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{St: st, Cfg: cfg, Log: log}
}

// result 汇总。
type Result struct {
	Created int
	Skipped int
	Errors  []string
	Libs    []LibResult `json:"libraries,omitempty"`
}

// LibResult 单个媒体库的重建统计。
type LibResult struct {
	Name    string `json:"name"`
	Created int    `json:"created"`
	Skipped int    `json:"skipped"`
}

// RebuildAll 遍历所有启用的媒体库，按各自配置与路由规则重建输出（幂等）。
// 串行执行（单飞）：原实现两次并发重建会在 linkFile 的检查-删除-重建窗口上竞争。
func (b *Bridge) RebuildAll() (*Result, error) {
	b.rebuildMu.Lock()
	defer b.rebuildMu.Unlock()
	return b.rebuildAllLocked()
}

// RebuildAllDebounced 下载完成后的异步重建入口：批量下载时每个完成都触发一次
// 全量重建，N 个新视频就是 N 遍全库遍历。这里做合并——重建进行中时触发只置脏
// 标记并立即返回，由在跑轮次结束后补跑一次（一轮触发风暴 ≈ 两次重建）。
// 全程与 RebuildAll 经 rebuildMu 互斥。返回最后一轮重建的错误（无重建需要跑
// 或补跑成功时为 nil）；日志由调用方带上下文记录。
func (b *Bridge) RebuildAllDebounced() error {
	b.stateMu.Lock()
	if b.rebuildBusy {
		b.rebuildDirty = true
		b.stateMu.Unlock()
		return nil
	}
	b.rebuildBusy = true
	b.stateMu.Unlock()

	var lastErr error
	for {
		b.rebuildMu.Lock()
		_, err := b.rebuildAllLocked()
		b.rebuildMu.Unlock()
		lastErr = err

		b.stateMu.Lock()
		if !b.rebuildDirty {
			b.rebuildBusy = false
			b.stateMu.Unlock()
			break
		}
		b.rebuildDirty = false
		b.stateMu.Unlock()
	}
	if lastErr != nil {
		b.logger().Warn("媒体库去抖重建失败", "err", lastErr)
	}
	return lastErr
}

func (b *Bridge) logger() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

func (b *Bridge) rebuildAllLocked() (*Result, error) {
	res := &Result{}
	libs, err := b.St.ListMediaLibraries()
	if err != nil {
		return nil, err
	}
	videos, err := b.St.ListVideos(0, "ok", 0, 0)
	if err != nil {
		return nil, err
	}
	// 一轮按块预取 origin 标签，不为每个视频重复扫描派生标签。
	ids := make([]string, 0, len(videos))
	for _, v := range videos {
		ids = append(ids, v.Bvid)
	}
	tagCache, err := b.St.BilibiliTagsByBvids(ids)
	if err != nil {
		return nil, fmt.Errorf("读取媒体库原始标签: %w", err)
	}
	for _, lib := range libs {
		if !lib.Enabled {
			continue
		}
		if err := os.MkdirAll(lib.RootPath, 0o755); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("库[%s]: %v", lib.Name, err))
			continue
		}
		matched := []*store.Video{}
		for _, v := range videos {
			if lib.Matches(v, tagCache[v.Bvid]) {
				matched = append(matched, v)
			}
		}
		byOwner := map[int64][]*store.Video{}
		for _, v := range matched {
			byOwner[v.OwnerMid] = append(byOwner[v.OwnerMid], v)
		}
		showPlans := map[int64]*showPlan{}
		if lib.Layout == "show" {
			for owner, siblings := range byOwner {
				if owner != 0 && len(siblings) >= 3 {
					showPlans[owner] = b.planShow(siblings, tagCache, lib.MetadataFormat)
				}
			}
		}
		libCreated0, libSkipped0 := res.Created, res.Skipped
		for _, v := range matched {
			layout := lib.Layout
			if v.OwnerMid == 0 || len(byOwner[v.OwnerMid]) < 3 {
				layout = "movie" // 单视频不足成剧，统一电影布局
			}
			var err error
			switch layout {
			case "show":
				err = b.buildShowEntry(v, showPlans[v.OwnerMid], lib, res, tagCache[v.Bvid])
			default:
				err = b.buildMovieEntry(v, lib, res, tagCache[v.Bvid])
			}
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("库[%s] %s: %v", lib.Name, v.Bvid, err))
			}
		}
		res.Libs = append(res.Libs, LibResult{
			Name:    lib.Name,
			Created: res.Created - libCreated0,
			Skipped: res.Skipped - libSkipped0,
		})
	}
	return res, nil
}

// Rebuild 兼容旧入口：等价 RebuildAll。
func (b *Bridge) Rebuild() (*Result, error) { return b.RebuildAll() }

// ---- 单视频 origin 元数据聚合 ----

// vmeta 单视频全部 B 站原生数据（NFO 只写这些，绝不含派生/站内数据）。
type vmeta struct {
	v         *store.Video
	biliTags  []string
	stats     *store.StatsSnapshot // 最新一次统计快照
	aiModel   string               // B站官方AI小结（origin=bilibili）
	aiSummary string
	setName   string // 来源为收藏夹/合集时作为合集（<set>）名
}

func (b *Bridge) gatherMeta(v *store.Video) *vmeta {
	var biliTags []string
	if tags, err := b.St.VideoTags(v.Bvid); err == nil {
		biliTags = tags["bilibili"]
	}
	return b.gatherMetaWithTags(v, biliTags)
}

// 重建轮的标签已批读；空列表也是有效结果，不回退逐视频查库。
func (b *Bridge) gatherMetaWithTags(v *store.Video, biliTags []string) *vmeta {
	m := &vmeta{v: v, biliTags: biliTags}
	if last, err := b.St.LatestStats(v.Bvid); err == nil {
		m.stats = last
	}
	if model, summary, _, err := b.St.GetAIConclusion(v.Bvid); err == nil && summary != "" {
		m.aiModel, m.aiSummary = model, summary
	}
	if v.SourceID > 0 {
		if src, err := b.St.GetSource(v.SourceID); err == nil &&
			(src.Type == "favlist" || src.Type == "season" || src.Type == "series") {
			m.setName = src.Title
		}
	}
	return m
}

// plot 简介 + B站官方AI小结 + 统计快照行（全部显式标注来源）。
func (m *vmeta) plot() string {
	var lines []string
	if d := strings.TrimSpace(m.v.Description); d != "" && d != "-" {
		lines = append(lines, d)
	}
	if m.aiSummary != "" {
		lines = append(lines, fmt.Sprintf("—— B站官方AI小结（%s）——\n%s", m.aiModel, m.aiSummary))
	}
	if m.stats != nil {
		s := m.stats
		lines = append(lines, fmt.Sprintf(
			"—— B站统计（截至 %s）——\n播放 %s · 弹幕 %s · 评论 %s · 点赞 %s · 投币 %s · 收藏 %s · 分享 %s",
			time.Unix(s.Ts, 0).Format("2006-01-02"),
			numCN(s.View), numCN(s.Danmaku), numCN(s.Reply), numCN(s.Like),
			numCN(s.Coin), numCN(s.Favorite), numCN(s.Share)))
	}
	if m.v.PagesCount > 1 {
		lines = append(lines, fmt.Sprintf("（多P投稿，共归档 %d 个分P）", m.v.PagesCount))
	}
	if len(lines) == 0 {
		return m.v.Title
	}
	return strings.Join(lines, "\n\n")
}

// outline 简介摘要（前 200 字符）。
func (m *vmeta) outline() string {
	d := strings.TrimSpace(m.v.Description)
	if d == "" || d == "-" {
		return truncateRunes(m.v.Title, 80)
	}
	return truncateRunes(d, 200)
}

// rating 点赞率映射 0-10 分（Kodi/Jellyfin 标准），votes=点赞数。
func (m *vmeta) rating() (float64, int) {
	if m.stats == nil || m.stats.View <= 0 {
		return 0, 0
	}
	r := float64(m.stats.Like) / float64(m.stats.View) * 10
	if r > 10 {
		r = 10
	}
	return float64(int(r*100)) / 100, m.stats.Like
}

func (m *vmeta) runtimeMin() int {
	if m.v.Duration <= 0 {
		return 1
	}
	if m.v.Duration < 60 {
		return 1
	}
	return m.v.Duration / 60
}

func numCN(n int) string {
	switch {
	case n >= 100000000:
		return fmt.Sprintf("%.1f亿", float64(n)/100000000)
	case n >= 10000:
		return fmt.Sprintf("%.1f万", float64(n)/10000)
	default:
		return strconv.Itoa(n)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// publicURL STRM 模式写入的核心访问地址。
func (b *Bridge) publicURL() string {
	if u := strings.TrimSpace(b.Cfg.Media.PublicURL); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://127.0.0.1" + b.Cfg.Listen
}

// emitVideo 按库链接方式落视频：strm 模式写 <base>.strm（内容为核心 HTTP 流地址，
// Emby/Jellyfin/Kodi 跨机直接播放），其余走 symlink→hardlink→copy 降级链。
func (b *Bridge) emitVideo(dst, src string, v *store.Video, lib *store.MediaLibrary) error {
	if lib.LinkMode == "strm" {
		url := b.publicURL() + "/media/" + mediaURLEscape(v.LocalPath)
		return writeFileIfChanged(strings.TrimSuffix(dst, filepath.Ext(dst))+".strm", []byte(url))
	}
	return linkFile(src, dst, lib.LinkMode)
}

// mediaURLEscape /media/ 相对路径的 URL 化（保留 /，转义空格与中文等）。
func mediaURLEscape(rel string) string {
	rel = filepath.ToSlash(rel)
	segs := strings.Split(rel, "/")
	for i, sg := range segs {
		segs[i] = url.PathEscape(sg)
	}
	return strings.Join(segs, "/")
}

// buildMovieEntry Movies/{标题} ({年})/{标题}.mp4 + 元数据 + 图片组 + 外挂字幕
func (b *Bridge) buildMovieEntry(v *store.Video, lib *store.MediaLibrary, res *Result, biliTags []string) error {
	if !v.Downloaded || v.LocalPath == "" {
		res.Skipped++
		return nil
	}
	src := filepath.Join(b.Cfg.DataDir, v.LocalPath)
	if _, err := os.Stat(src); err != nil {
		res.Skipped++
		return nil
	}
	year := time.Unix(v.Pubdate, 0).Year()
	title := sanitizeFS(v.Title)
	if title == "" {
		title = v.Bvid
	}
	dir := filepath.Join(lib.RootPath, "Movies", fmt.Sprintf("%s (%d) [%s]", title, year, v.Bvid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := fmt.Sprintf("%s (%d)", title, year)
	dst := filepath.Join(dir, base+".mp4")
	if err := b.emitVideo(dst, src, v, lib); err != nil {
		return err
	}
	if lib.MetadataFormat != "none" {
		b.writeMeta(dir, lib.MetadataFormat, b.gatherMetaWithTags(v, biliTags), 0, "")
	}
	b.artwork(dir, v, "poster.jpg", "fanart.jpg", "banner.jpg")
	b.linkSubtitles(dir, base, v, lib.LinkMode)
	b.linkMultiPExtras(dir, v, lib)
	res.Created++
	return nil
}

// linkMultiPExtras 多P投稿：已下载的 P2+ 作为 extras/ 特花收录
// （Emby/Jellyfin 条目页“附加内容”区展示），文件名带分P标题。
func (b *Bridge) linkMultiPExtras(dir string, v *store.Video, lib *store.MediaLibrary) {
	if v.PagesCount <= 1 {
		return
	}
	pages, err := b.St.ListPages(v.Bvid)
	if err != nil {
		return
	}
	var extrasDir string
	for _, p := range pages {
		if p.Page <= 1 {
			continue
		}
		rel := fmt.Sprintf("media/%s/p%d/%s-p%d.mp4", v.Bvid, p.Page, v.Bvid, p.Page)
		src := filepath.Join(b.Cfg.DataDir, filepath.FromSlash(rel))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if extrasDir == "" {
			extrasDir = filepath.Join(dir, "extras")
			if err := os.MkdirAll(extrasDir, 0o755); err != nil {
				return
			}
		}
		name := sanitizeFS(p.Part)
		if name == "" {
			name = fmt.Sprintf("P%d", p.Page)
		}
		dst := filepath.Join(extrasDir, fmt.Sprintf("P%d %s.mp4", p.Page, name))
		_ = b.emitVideo(dst, src, v, lib)
	}
}

// writeMeta 按库配置输出元数据：kodi NFO / JSON sidecar / 无。
// format=nfo 写 NFO 与兼容 JSON sidecar；json 仅写 JSON；none 不写。
func (b *Bridge) writeMeta(dir, format string, m *vmeta, ep int, showBase string) {
	if format == "none" {
		return
	}
	v := m.v
	switch {
	case format != "nfo":
	case ep == 0:
		_ = writeFileIfChanged(filepath.Join(dir, "movie.nfo"), []byte(movieNFO(m)))
	default:
		_ = writeFileIfChanged(filepath.Join(dir, showBase+".nfo"), []byte(episodeNFO(m, ep)))
	}
	// JSON sidecar（origin 数据全量）
	if m != nil && dir != "" {
		meta := map[string]any{
			"bvid": v.Bvid, "aid": v.Aid, "title": v.Title, "description": v.Description,
			"pubdate": v.Pubdate, "duration": v.Duration,
			"owner":         map[string]any{"mid": v.OwnerMid, "name": v.OwnerName, "face": v.OwnerFace},
			"tags_bilibili": m.biliTags, "archived_at": v.FirstArchivedAt,
			"origin": "bilibili",
		}
		if m.stats != nil {
			meta["stats"] = map[string]int{
				"view": m.stats.View, "danmaku": m.stats.Danmaku, "reply": m.stats.Reply,
				"favorite": m.stats.Favorite, "coin": m.stats.Coin, "share": m.stats.Share,
				"like": m.stats.Like,
			}
			meta["stats_ts"] = m.stats.Ts
		}
		if m.aiSummary != "" {
			meta["ai_conclusion"] = map[string]string{"model": m.aiModel, "summary": m.aiSummary, "origin": "bilibili"}
		}
		if m.setName != "" {
			meta["collection"] = m.setName
		}
		if ep > 0 {
			meta["season"], meta["episode"] = 1, ep
		}
		if j, err := json.MarshalIndent(meta, "", "  "); err == nil {
			name := v.Bvid + ".json"
			if ep > 0 {
				name = fmt.Sprintf("%s-S01E%02d.json", v.Bvid, ep)
			}
			_ = writeFileIfChanged(filepath.Join(dir, name), j)
		}
	}
}

// artwork 从本地化封面复制一组标准图片（缺源则跳过）。
func (b *Bridge) artwork(dir string, v *store.Video, names ...string) {
	cover := filepath.Join(b.Cfg.DataDir, "media", sanitizeFS(v.Bvid), "cover.jpg")
	for _, n := range names {
		_ = copyIfChanged(cover, filepath.Join(dir, n))
	}
}

// linkSubtitles 把 B 站 CC 字幕（bcc/json，origin=bilibili）转 srt 外挂；
// 已是 srt 的直接复制。命名 <视频名>.<语言>.srt 供 Emby/Jellyfin/Kodi 自动挂载。
func (b *Bridge) linkSubtitles(dir, base string, v *store.Video, mode string) {
	subs, err := b.St.ListSubtitles(v.Bvid, 0)
	if err != nil {
		return
	}
	for _, sub := range subs {
		if sub.Origin != "bilibili" || sub.FilePath == "" {
			continue
		}
		src := b.resolvePath(sub.FilePath)
		if src == "" {
			continue
		}
		lang := sub.Lang
		if lang == "" || lang == "ai-zh" {
			lang = "zh"
		}
		dst := filepath.Join(dir, fmt.Sprintf("%s.%s.srt", base, lang))
		switch strings.ToLower(sub.Format) {
		case "srt":
			_ = copyIfChanged(src, dst)
		case "bcc", "json":
			if srt, err := bccToSRTFile(src); err == nil && srt != "" {
				_ = writeFileIfChanged(dst, []byte(srt))
			}
		}
	}
}

// resolvePath 解析字幕文件位置（历史数据里 FilePath 可能含 data/ 前缀或为绝对路径）。
func (b *Bridge) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	for _, cand := range []string{p, filepath.Join(b.Cfg.DataDir, p)} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// bccToSRTFile B站 CC 字幕（bcc/json：{"body":[{"from","to","content"}]}）转 SRT 文本。
func bccToSRTFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc struct {
		Body []struct {
			From     float64  `json:"from"`
			To       float64  `json:"to"`
			Content  string   `json:"content"`
			Contents []string `json:"contents"` // 部分版本为行数组
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", err
	}
	var sb strings.Builder
	idx := 1
	for _, l := range doc.Body {
		text := l.Content
		if text == "" {
			text = strings.Join(l.Contents, "\n")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		from, to := l.From, l.To
		if from > 100000 { // 兼容毫秒时间轴
			from, to = from/1000, to/1000
		}
		sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n",
			idx, srtTime(from), srtTime(to), strings.TrimRight(text, "\n")))
		idx++
	}
	return sb.String(), nil
}

func srtTime(sec float64) string {
	ms := int(sec*1000 + 0.5)
	h, m, s := ms/3600000, ms/60000%60, ms/1000%60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms%1000)
}

// buildShowEntry Shows/{UP主}/Season 01/{UP主} - SxxEyy - {标题} [BV].mp4
// 剧集 NFO/缩略图与视频同名（多集同目录时 Emby/Jellyfin 按文件名关联）。
func (b *Bridge) buildShowEntry(v *store.Video, plan *showPlan, lib *store.MediaLibrary, res *Result, biliTags []string) error {
	if !v.Downloaded || v.LocalPath == "" {
		res.Skipped++
		return nil
	}
	src := filepath.Join(b.Cfg.DataDir, v.LocalPath)
	if _, err := os.Stat(src); err != nil {
		res.Skipped++
		return nil
	}
	ep := plan.episodes[v.Bvid]
	show := plan.name
	showDir := filepath.Join(lib.RootPath, "Shows", show)
	seasonDir := filepath.Join(showDir, "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return err
	}
	base := fmt.Sprintf("%s - S01E%02d - %s [%s]", show, ep, sanitizeFS(v.Title), v.Bvid)
	dst := filepath.Join(seasonDir, base+".mp4")
	if err := b.emitVideo(dst, src, v, lib); err != nil {
		return err
	}
	// 每个 UP 主每轮仅写一次共享元数据与海报，避免 N 集 × N 标签查询。
	if !plan.sharedWritten {
		if lib.MetadataFormat == "nfo" {
			_ = writeFileIfChanged(filepath.Join(showDir, "tvshow.nfo"), plan.nfo)
		}
		b.artwork(showDir, plan.first, "poster.jpg", "fanart.jpg", "banner.jpg")
		b.artwork(seasonDir, plan.first, "poster.jpg")
		plan.sharedWritten = true
	}
	// 集级 NFO 与视频同名；集缩略图 -thumb.jpg
	if lib.MetadataFormat != "none" {
		b.writeMeta(seasonDir, lib.MetadataFormat, b.gatherMetaWithTags(v, biliTags), ep, base)
	}
	b.artwork(seasonDir, v, base+"-thumb.jpg")
	b.linkSubtitles(seasonDir, base, v, lib.LinkMode)
	res.Created++
	return nil
}

// sortByID 按发布时间升序（稳定），求剧集集号用。
func sortByID(vs []*store.Video) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Pubdate < vs[j].Pubdate })
}

var reUnsafeFS = regexp.MustCompile(`[\\/:*?"<>|]+`)

func sanitizeFS(s string) string {
	s = strings.TrimSpace(reUnsafeFS.ReplaceAllString(s, "_"))
	s = strings.Trim(s, " ._")
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ { // 去控制字符
		if s[i] >= 32 {
			b = append(b, s[i])
		}
	}
	if len(b) > 120 { // 控制路径长度
		b = b[:120]
	}
	return string(b)
}
