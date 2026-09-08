// Package downloader 实现 DASH 视频下载：选流、断点续传、ffmpeg 合并（原子改名）、封面。
package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/metrics"
	"bili-saver/internal/store"
)

// Emitter 与 collector.Emitter 同签名。
type Emitter interface {
	Emit(event string, payload map[string]any)
}

type noopEmitter struct{}

func (noopEmitter) Emit(string, map[string]any) {}

// ProgressFn 汇报整体进度（0..1）。
type ProgressFn func(fraction float64, detail string)

type Downloader struct {
	St      *store.Store
	API     *biliapi.Client
	Cfg     *config.Config
	Log     *slog.Logger
	Emitter Emitter
	FFmpeg  string
}

func New(st *store.Store, api *biliapi.Client, cfg *config.Config, log *slog.Logger, em Emitter) *Downloader {
	if em == nil {
		em = noopEmitter{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Downloader{St: st, API: api, Cfg: cfg, Log: log, Emitter: em, FFmpeg: cfg.FFmpegPath()}
}

// qualityRank 清晰度 id → 可比较档位（越大越高）。
var qualityNames = map[string]int{
	"4k": 120, "1080p60": 116, "1080p-high": 112, "1080p": 80,
	"720p60": 74, "720p": 64, "480p": 32, "360p": 16,
}

// audioQualityNames 音频质量名 → 流 ID。30250 杜比全景声 / 30251 Hi-Res 无损
// 需要请求 fnval|256 才会返回，且音轨在 dash.dolby / dash.flac 独立字段里。
var audioQualityNames = map[string]int{
	"hires": 30251, "dolby": 30250, "192k": 30280, "132k": 30232, "64k": 30216,
}

// audioPref 标准音轨偏好：192k > 132k > 64k（兼容性最好，默认不取空间音频）。
var audioPref = []int{30280, 30232, 30216}

func (d *Downloader) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// videoQNs 把质量配置解析为允许的清晰度集合；nil=全部画质（默认，取账号可用的最高档）。
// qualities 非空时忽略旧 quality 单档位；allow_4k=false 始终剔除 4K(120)。
func (d *Downloader) videoQNs() map[int]bool {
	c := &d.Cfg.Download
	var ids []int
	if len(c.Qualities) > 0 {
		for _, name := range c.Qualities {
			id, ok := qualityNames[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				d.logger().Warn("未知清晰度名，已忽略", "quality", name)
				continue
			}
			ids = append(ids, id)
		}
	} else if c.Quality != "" {
		if id, ok := qualityNames[strings.ToLower(strings.TrimSpace(c.Quality))]; ok {
			ids = []int{id}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id == 120 && !c.Allow4K {
			d.logger().Warn("4K 被 allow_4k=false 剔除", "qualities", strings.Join(c.Qualities, ","))
			continue
		}
		set[id] = true
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// audioPrefChain 音频偏好链（高→低）：audio_qualities 指定集合优先，标准三档兜底。
func (d *Downloader) audioPrefChain() []int {
	names := d.Cfg.Download.AudioQualities
	if len(names) == 0 {
		return audioPref
	}
	ids := make([]int, 0, len(names)+3)
	for _, n := range names {
		id, ok := audioQualityNames[strings.ToLower(strings.TrimSpace(n))]
		if !ok {
			d.logger().Warn("未知音频质量名，已忽略", "audio_quality", n)
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return append(ids, audioPref...)
}

// selectAudio 在全部音轨（标准+杜比+Hi-Res 合并传入）中按偏好链选一；
// 指定集合全不可用时由偏好链尾部的标准三档兜底，再不行取首条。
func (d *Downloader) selectAudio(tracks []biliapi.DashStream) *biliapi.DashStream {
	for _, want := range d.audioPrefChain() {
		for i := range tracks {
			if tracks[i].ID == want {
				return &tracks[i]
			}
		}
	}
	if len(tracks) > 0 {
		return &tracks[0]
	}
	return nil
}

// resolveQuality 计算 playurl 请求参数：qn 只是期望提示（DASH 模式下服务器按账号
// 权限返回全部可用档，实际选流在本地），fnval 决定是否下发 4K/杜比/Hi-Res 能力。
func (d *Downloader) resolveQuality() (qnHint, fnval int) {
	fnval = 16
	qns := d.videoQNs()
	if qns == nil {
		if d.Cfg.Download.Allow4K {
			fnval |= 128
		}
		return 120, fnval
	}
	qnHint = 0
	for id := range qns {
		if id > qnHint {
			qnHint = id
		}
	}
	if qnHint == 120 {
		fnval |= 128 // videoQNs 已在 allow_4k=false 时剔除 120
	}
	for _, id := range d.audioPrefChain() {
		if id == 30250 || id == 30251 {
			fnval |= 256
			break
		}
	}
	return qnHint, fnval
}

// selectVideo 选流策略：
//  1. 允许集合内可用的最高档（同档优先 AVC，兼容性最好）；
//  2. 集合内全部不可用 → 上限以下最高档（指定档位拿不到时优雅降级，不打断归档）；
//  3. 仍无 → 允许编码里最低档兜底。
func (d *Downloader) selectVideo(streams []biliapi.DashStream) *biliapi.DashStream {
	allowed := d.videoQNs()
	maxAllowed := 0
	for id := range allowed {
		if id > maxAllowed {
			maxAllowed = id
		}
	}
	var best, bestBelow, lowest *biliapi.DashStream
	for i := range streams {
		s := &streams[i]
		if !d.codecAllowed(s) {
			continue
		}
		if lowest == nil || s.ID < lowest.ID || (s.ID == lowest.ID && d.codecScore(s) > d.codecScore(lowest)) {
			lowest = s
		}
		if allowed != nil && !allowed[s.ID] {
			if s.ID < maxAllowed && (bestBelow == nil || s.ID > bestBelow.ID) {
				bestBelow = s
			}
			continue
		}
		if best == nil || s.ID > best.ID || (s.ID == best.ID && d.codecScore(s) > d.codecScore(best)) {
			best = s
		}
	}
	if best != nil {
		return best
	}
	if bestBelow != nil {
		return bestBelow
	}
	return lowest
}

func (d *Downloader) codecAllowed(s *biliapi.DashStream) bool {
	switch s.Codecid {
	case 7: // AVC/H.264 兼容性最好
		return true
	case 12, 13: // HEVC / AV1 需显式允许
		return d.Cfg.Download.AllowAV1
	}
	return false
}

func (d *Downloader) codecScore(s *biliapi.DashStream) int {
	if s.Codecid == 7 {
		return 1
	}
	return 0
}

// DownloadVideo 下载整支视频（全部分P）+ 封面。
func (d *Downloader) DownloadVideo(ctx context.Context, bvid string, progress ProgressFn) error {
	v, err := d.St.GetVideo(bvid)
	if err != nil {
		return fmt.Errorf("视频不存在于库中: %w", err)
	}
	pages, err := d.St.ListPages(bvid)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("库中无分P信息: %s", bvid)
	}
	if progress == nil {
		progress = func(float64, string) {}
	}

	coverPath := ""
	if v.Pic != "" {
		if p, err := d.downloadCover(ctx, bvid, v.Pic); err == nil {
			coverPath = p
		} else {
			d.Log.Warn("封面下载失败", "bvid", bvid, "err", err)
		}
	}

	qn, fnval := d.resolveQuality()

	var firstLocal string
	for i, pg := range pages {
		local, err := d.downloadPage(ctx, bvid, v, pg, qn, fnval, func(f float64) {
			progress((float64(i)+f)/float64(len(pages)), fmt.Sprintf("P%d/%d", i+1, len(pages)))
		})
		if err != nil {
			return err
		}
		if i == 0 {
			firstLocal = local
		}
	}
	if err := d.St.SetVideoDownloaded(bvid, firstLocal); err != nil {
		return err
	}
	_ = coverPath
	payload := map[string]any{
		"bvid": bvid, "title": v.Title, "description": v.Description,
		"owner_mid": v.OwnerMid, "owner_name": v.OwnerName,
	}
	if tags, err := d.St.VideoTags(bvid); err == nil {
		payload["tags"] = tags["bilibili"]
	}
	d.Emitter.Emit("video.downloaded", payload)
	progress(1, "完成")
	return nil
}

// downloadPage 下载单个分P并合并为 mp4，返回相对 dataDir 的路径。
func (d *Downloader) downloadPage(ctx context.Context, bvid string, v *store.Video, pg *store.Page, qn, fnval int, onPage func(float64)) (string, error) {
	// 幂等：已完成的本P直接跳过（重试/重复任务不重复拉流，也不请求 playurl）
	dir := filepath.Join(d.Cfg.DataDir, "media", sanitize(bvid), fmt.Sprintf("p%d", pg.Page))
	outPath := filepath.Join(dir, fmt.Sprintf("%s-p%d.mp4", sanitize(bvid), pg.Page))
	if _, err := os.Stat(outPath); err == nil {
		if rel, rerr := filepath.Rel(d.Cfg.DataDir, outPath); rerr == nil {
			onPage(1)
			return rel, nil
		}
	}
	pu, err := d.API.PlayURL(ctx, bvid, v.Aid, pg.Cid, qn, fnval)
	if err != nil {
		return "", fmt.Errorf("playurl: %w", err)
	}
	if pu.DashDash == nil || len(pu.DashDash.Video) == 0 {
		return "", fmt.Errorf("无 DASH 流（可能需要登录或清晰度不可用）")
	}
	vs := d.selectVideo(pu.DashDash.Video)
	// 音轨候选：标准 audio 数组 + 杜比/Hi-Res 独立字段（fnval|256 才返回）
	tracks := make([]biliapi.DashStream, 0, len(pu.DashDash.Audio)+2)
	tracks = append(tracks, pu.DashDash.Audio...)
	if pu.DashDash.Dolby != nil {
		tracks = append(tracks, pu.DashDash.Dolby.Audio...)
	}
	if pu.DashDash.FLAC != nil {
		tracks = append(tracks, pu.DashDash.FLAC.Audio...)
	}
	as := d.selectAudio(tracks)
	if vs == nil {
		return "", fmt.Errorf("没有可用视频流")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	vPath := filepath.Join(dir, "video.m4s.part")
	aPath := "" // 无独立音轨时必须传空，merge 才不会去开不存在的 audio.m4s.part
	if as != nil {
		aPath = filepath.Join(dir, "audio.m4s.part")
	}

	// 音视频流无依赖，并行下载；进度按带宽加权（时长相同，大小比≈带宽比），
	// 下载阶段占 0..0.9，合并收尾占 0.9..1
	wv, wa := 1.0, 0.0
	if as != nil {
		if bw := vs.Bandwidth + as.Bandwidth; bw > 0 {
			wv = float64(vs.Bandwidth) / float64(bw)
			wa = 1 - wv
		}
	}
	var progMu sync.Mutex
	fv, fa := 0.0, 0.0
	rep := func() {
		progMu.Lock()
		defer progMu.Unlock()
		onPage((fv*wv + fa*wa) * 0.9)
	}
	repV := func(f float64) { progMu.Lock(); fv = f; progMu.Unlock(); rep() }
	repA := func(f float64) { progMu.Lock(); fa = f; progMu.Unlock(); rep() }

	var wg sync.WaitGroup
	var errA error
	if as != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errA = d.downloadFile(ctx, as, aPath, repA)
		}()
	}
	errV := d.downloadFile(ctx, vs, vPath, repV)
	wg.Wait()
	if errV != nil {
		return "", fmt.Errorf("下载视频流: %w", errV)
	}
	if errA != nil {
		return "", fmt.Errorf("下载音频流: %w", errA)
	}

	outName := fmt.Sprintf("%s-p%d.mp4", sanitize(bvid), pg.Page)
	outPath2 := filepath.Join(dir, outName)
	merging := outPath2 + ".merging"
	_ = os.Remove(merging)
	if err := d.merge(ctx, vPath, aPath, merging); err != nil {
		_ = os.Remove(merging)
		return "", fmt.Errorf("ffmpeg 合并: %w", err)
	}
	if err := os.Rename(merging, outPath); err != nil {
		return "", err
	}
	_ = os.Remove(vPath)
	if aPath != "" {
		_ = os.Remove(aPath)
	}
	rel, err := filepath.Rel(d.Cfg.DataDir, outPath)
	if err != nil {
		rel = outPath
	}
	onPage(1)
	return rel, nil
}

func (d *Downloader) streamURLs(s *biliapi.DashStream) []string {
	urls := []string{s.BaseURL}
	urls = append(urls, s.BackupURL...)
	return urls
}

// minSegSize 流大小低于该值时不分段并行（建连开销超过收益）。
const minSegSize = 8 << 20

// segIdleTimeout 下载连接空闲（无字节到达）超过该时长视为挂死，取消当前请求。
var segIdleTimeout = 30 * time.Second

// segExt 分段断点状态文件后缀（与 .part 同目录）。
const segExt = ".segs"

// downloadFile 下载一个 DASH 流到 path（.part）。
// 流足够大且 CDN 支持 Range 时走分段并行；否则退回单流断点续传。
func (d *Downloader) downloadFile(ctx context.Context, s *biliapi.DashStream, path string, onFrac func(float64)) error {
	client := d.API.DownloadClient()
	ua := d.API.UserAgent()
	urls := d.streamURLs(s)
	segsPath := path + segExt

	// 残留分段断点：分段写出的 .part 是稀疏文件，只有分段路径能安全续传
	if _, err := os.Stat(segsPath); err == nil {
		if d.Cfg.Download.Segments <= 1 {
			return fmt.Errorf("%s 存在未完成的分段断点，需 download.segments>1 续传；或删除 %s 与 .part 重下", segsPath, segsPath)
		}
		b, rerr := os.ReadFile(segsPath)
		var sf segFile
		if rerr != nil || json.Unmarshal(b, &sf) != nil || sf.Size <= 0 {
			return fmt.Errorf("%s 断点损坏，删除 %s 与 .part 后重试", segsPath, segsPath)
		}
		d.Log.Info("续传分段下载", "file", filepath.Base(path), "size", sf.Size)
		return downloadSegmented(ctx, client, ua, urls, path, sf.Size, d.Cfg.Download.Segments, d.Log, onFrac)
	}

	if d.Cfg.Download.Segments > 1 {
		for _, u := range urls {
			if u == "" {
				continue
			}
			total, ranged, err := probeSize(ctx, client, ua, u)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				d.Log.Warn("分段探测失败，换下一个地址", "err", err)
				continue
			}
			if !ranged || total < minSegSize {
				break // 不支持 Range 或文件太小：单流即可
			}
			return downloadSegmented(ctx, client, ua, urls, path, total, d.Cfg.Download.Segments, d.Log, onFrac)
		}
	}

	var have int64
	if st, err := os.Stat(path); err == nil {
		have = st.Size()
	}
	for attempt, u := range urls {
		if u == "" {
			continue
		}
		size, err := d.tryDownload(ctx, u, path, have, s.Bandwidth, onFrac)
		if err == nil {
			_ = size
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == len(s.BackupURL) {
			return err
		}
		d.Log.Warn("下载失败，尝试备用 CDN", "err", err)
		// 部分失败后文件不完整，重新全量下载
		have = 0
	}
	return fmt.Errorf("无可用下载地址")
}

func (d *Downloader) tryDownload(ctx context.Context, u, path string, have int64, bandwidth int64, onFrac func(float64)) (int64, error) {
	// 下载专用 client 无总超时（60s 总超时会反复掐断慢速大流），挂死由空闲 watchdog 兜底
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watch := time.AfterFunc(segIdleTimeout, cancel)
	defer watch.Stop()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", d.API.UserAgent())
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := d.API.DownloadClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusOK:
	case http.StatusRequestedRangeNotSatisfiable:
		// 已下载完成或远端不认 Range：重新下载
		have = 0
		return d.tryDownload(ctx, u, path, 0, bandwidth, onFrac)
	default:
		return 0, fmt.Errorf("http %d", resp.StatusCode)
	}

	var flag int
	if resp.StatusCode == http.StatusOK && have > 0 {
		flag = os.O_TRUNC // 服务器不支持 Range，重来
	} else {
		flag = os.O_CREATE | os.O_WRONLY
		if resp.StatusCode == http.StatusPartialContent {
			flag |= os.O_APPEND
		} else {
			flag |= os.O_TRUNC
		}
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	total := have + resp.ContentLength
	written := have
	buf := make([]byte, 512*1024)
	last := time.Now()
	for {
		watch.Reset(segIdleTimeout)
		n, err := resp.Body.Read(buf)
		if n > 0 {
			metrics.Download.Add(int64(n))
			if _, werr := f.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if onFrac != nil && total > 0 && time.Since(last) > 300*time.Millisecond {
				last = time.Now()
				onFrac(float64(written) / float64(total))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
	}
	// 与流声明大小核对（audio/video m4s 总长）
	if resp.ContentLength > 0 && written != total {
		return written, fmt.Errorf("下载不完整 %d/%d", written, total)
	}
	if onFrac != nil {
		onFrac(1)
	}
	return written, nil
}

// segState 单个分段的下载进度。
type segState struct {
	Start int64 `json:"start"` // 段起始偏移（含）
	End   int64 `json:"end"`   // 段结束偏移（不含）
	Done  int64 `json:"done"`  // 已写入字节数
}

// segFile 分段断点状态（path+segExt）。
type segFile struct {
	Size int64      `json:"size"`
	Segs []segState `json:"segs"`
}

// parseContentRangeTotal 从 "bytes 0-0/12345" 提取总大小；无法解析返回 0。
func parseContentRangeTotal(cr string) int64 {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 || i == len(cr)-1 {
		return 0
	}
	n, err := strconv.ParseInt(cr[i+1:], 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// probeSize 用 1 字节 Range 请求探测总大小；ranged=false 表示 CDN 不支持 Range。
func probeSize(ctx context.Context, client *http.Client, ua, u string) (total int64, ranged bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return parseContentRangeTotal(resp.Header.Get("Content-Range")), true, nil
	case http.StatusOK:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("http %d", resp.StatusCode)
	}
}

// loadOrInitSegs 读取分段断点；缺失或与 total 不符时新建全零分段。
func loadOrInitSegs(segsPath string, total int64, n int) (*segFile, error) {
	if b, err := os.ReadFile(segsPath); err == nil {
		var sf segFile
		if json.Unmarshal(b, &sf) == nil && sf.Size == total && len(sf.Segs) > 0 {
			return &sf, nil
		}
	}
	sf := &segFile{Size: total, Segs: make([]segState, n)}
	chunk := (total + int64(n) - 1) / int64(n)
	for i := range sf.Segs {
		sf.Segs[i].Start = int64(i) * chunk
		sf.Segs[i].End = int64(i+1) * chunk
		if sf.Segs[i].End > total {
			sf.Segs[i].End = total
		}
	}
	return sf, nil
}

// saveSegs 原子落盘分段状态（tmp+rename）。
func saveSegs(segsPath string, sf *segFile) error {
	b, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	tmp := segsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, segsPath)
}

// downloadSegmented 用 n 个并发 Range 连接把 total 字节下载到 path。
// .part 稀疏预分配，断点状态存 path+segExt；任一段失败即返回错误并保留
// 状态（任务级重试时续传）。各段区间互不重叠，WriteAt 并发安全。
func downloadSegmented(ctx context.Context, client *http.Client, ua string, urls []string, path string, total int64, n int, log *slog.Logger, onFrac func(float64)) error {
	segsPath := path + segExt
	sf, err := loadOrInitSegs(segsPath, total, n)
	if err != nil {
		return err
	}
	if sf.Size != total {
		return fmt.Errorf("分段断点大小不符 %d != %d", sf.Size, total)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		return err
	}

	var mu sync.Mutex
	var written int64
	var lastRep time.Time
	for i := range sf.Segs {
		written += sf.Segs[i].Done
	}
	addWritten := func(delta int64) {
		metrics.Download.Add(delta)
		mu.Lock()
		defer mu.Unlock()
		written += delta
		if onFrac != nil && total > 0 && time.Since(lastRep) > 300*time.Millisecond {
			lastRep = time.Now()
			onFrac(float64(written) / float64(total))
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(sf.Segs))
	for i := range sf.Segs {
		seg := &sf.Segs[i]
		if seg.Done >= seg.End-seg.Start {
			continue
		}
		if log != nil {
			log.Info("分段下载", "seg", i, "range", fmt.Sprintf("%d-%d", seg.Start+seg.Done, seg.End-1))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fetchSegment(ctx, client, ua, urls, f, seg, addWritten); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		_ = saveSegs(segsPath, sf) // 保住已完成进度
		return firstErr
	}
	if written != total {
		return fmt.Errorf("分段下载不完整 %d/%d", written, total)
	}
	_ = os.Remove(segsPath)
	if onFrac != nil {
		onFrac(1)
	}
	return nil
}

// fetchSegment 单段下载：在镜像列表上逐个尝试。
func fetchSegment(ctx context.Context, client *http.Client, ua string, urls []string, f *os.File, seg *segState, addWritten func(int64)) error {
	var lastErr error
	for _, u := range urls {
		if u == "" {
			continue
		}
		err := trySegment(ctx, client, ua, u, f, seg, addWritten)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用下载地址")
	}
	return fmt.Errorf("分段 [%d,%d) 下载失败: %w", seg.Start, seg.End, lastErr)
}

// trySegment 一次 Range 请求把段剩余部分写入 f 的对应偏移。
func trySegment(ctx context.Context, client *http.Client, ua, u string, f *os.File, seg *segState, addWritten func(int64)) error {
	start := seg.Start + seg.Done
	if start >= seg.End {
		return nil
	}
	// 无总超时，靠空闲 watchdog 取消挂死连接
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watch := time.AfterFunc(segIdleTimeout, cancel)
	defer watch.Stop()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, seg.End-1))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		return fmt.Errorf("服务器不支持 Range（对分段请求返回 200）")
	default:
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength != seg.End-start {
		return fmt.Errorf("分段长度不符 %d != %d", resp.ContentLength, seg.End-start)
	}

	buf := make([]byte, 512*1024)
	off := start
	for {
		watch.Reset(segIdleTimeout)
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if seg.Done+int64(n) > seg.End-seg.Start {
				n = int(seg.End - seg.Start - seg.Done) // 服务器多发的部分丢弃
			}
			if n > 0 {
				if _, werr := f.WriteAt(buf[:n], off); werr != nil {
					return werr
				}
				off += int64(n)
				seg.Done += int64(n)
				addWritten(int64(n))
			}
		}
		if seg.Done >= seg.End-seg.Start {
			return nil
		}
		if rerr == io.EOF {
			return fmt.Errorf("连接提前结束 %d/%d", seg.Done, seg.End-seg.Start)
		}
		if rerr != nil {
			return rerr
		}
	}
}

// merge 用 ffmpeg 把视频/音频流封装成 mp4（-c copy 不重编码）。
func (d *Downloader) merge(ctx context.Context, videoPath, audioPath, outPath string) error {
	if _, err := exec.LookPath(d.FFmpeg); err != nil {
		if _, err2 := os.Stat(d.FFmpeg); err2 != nil {
			return fmt.Errorf("找不到 ffmpeg（配置 ffmpeg.path 或安装到 PATH）")
		}
	}
	args := []string{"-y", "-nostdin", "-loglevel", "error", "-i", videoPath}
	if audioPath != "" {
		args = append(args, "-i", audioPath)
	}
	args = append(args, "-c", "copy", "-movflags", "+faststart", "-f", "mp4", outPath)
	cmd := exec.CommandContext(ctx, d.FFmpeg, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// downloadCover 下载封面到 media/{bvid}/cover.jpg。
func (d *Downloader) downloadCover(ctx context.Context, bvid, picURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, picURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", d.API.UserAgent())
	req.Header.Set("Referer", "https://www.bilibili.com/")
	resp, err := d.API.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	dir := filepath.Join(d.Cfg.DataDir, "media", sanitize(bvid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "cover.jpg")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}

var reUnsafePath = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

func sanitize(s string) string {
	s = reUnsafePath.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._")
	if s == "" {
		s = "unknown"
	}
	return s
}
