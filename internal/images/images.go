// Package images 把 B 站图床资源（视频封面、UP主/评论者头像）本地化到
// data/media（origin 层）。直接给前端 CDN URL 会被防盗链拦截
// （i*.hdslb.com 对第三方 Referer 返回 403），且消失内容的图床文件会随之下线；
// 本地化是 teriteri 前端离线可看的前提。文件本身即状态：已存在即跳过，可重复执行。
package images

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/store"
)

// 落盘布局（dataDir 相对路径）：封面沿用下载器既有约定 media/{bvid}/cover.jpg
// （card 生成器也读这个位置），头像按 mid 去重存 media/faces/{mid}.jpg。
const FaceDir = "media/faces"

// maxImageBytes 单图上限（B站封面/头像远小于此，防异常响应撑爆磁盘）。
const maxImageBytes = 20 << 20

func CoverRel(bvid string) string { return "media/" + bvid + "/cover.jpg" }
func FaceRel(mid int64) string    { return FaceDir + "/" + strconv.FormatInt(mid, 10) + ".jpg" }

// bvidSafe 防御：库内 bvid 异常（路径分隔符等）时不生成本地路径。
func bvidSafe(bvid string) bool {
	return bvid != "" && !strings.ContainsAny(bvid, `/\:*?"<>|%`) && !strings.Contains(bvid, "..")
}

// CoverURL 本地封面存在时返回 /media URL，否则空串（调用方回退 CDN URL）。
func CoverURL(dataDir, bvid string) string {
	return fileURL(dataDir, CoverRel(bvid), bvidSafe(bvid))
}

// FaceURL 本地头像存在时返回 /media URL，否则空串。
func FaceURL(dataDir string, mid int64) string {
	return fileURL(dataDir, FaceRel(mid), mid > 0)
}

func fileURL(dataDir, rel string, ok bool) string {
	if !ok {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(rel))); err != nil {
		return ""
	}
	return "/media/" + rel
}

// Face 一个待本地化的头像（按 mid 去重）。
type Face struct {
	Mid int64
	URL string
}

// Localizer 拉取并落盘图床资源。
type Localizer struct {
	St      *store.Store
	DataDir string
	UA      string
	Log     *slog.Logger
	// Pace 相邻两次网络拉取的间隔（图床限速友好）。
	Pace time.Duration

	client *http.Client
}

func New(st *store.Store, dataDir, ua string, log *slog.Logger) *Localizer {
	if log == nil {
		log = slog.Default()
	}
	return &Localizer{
		St: st, DataDir: dataDir, UA: ua, Log: log,
		Pace:   20 * time.Millisecond,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// SyncVideo 本地化单个视频的封面、UP主头像与评论者头像（已存在的自动跳过）。
// 单个资源失败只告警不中断；返回新落盘数量。
func (l *Localizer) SyncVideo(ctx context.Context, bvid string) (int, error) {
	v, err := l.St.GetVideo(bvid)
	if err != nil {
		return 0, fmt.Errorf("查询视频: %w", err)
	}
	n := 0
	if v.Pic != "" {
		if ok, err := l.fetchMissing(ctx, CoverRel(v.Bvid), v.Pic); err != nil {
			l.Log.Warn("封面本地化失败", "bvid", v.Bvid, "err", err)
		} else if ok {
			n++
		}
	}
	if v.OwnerMid > 0 && v.OwnerFace != "" {
		if ok, err := l.fetchMissing(ctx, FaceRel(v.OwnerMid), v.OwnerFace); err != nil {
			l.Log.Warn("UP主头像本地化失败", "mid", v.OwnerMid, "err", err)
		} else if ok {
			n++
		}
	}
	faces, err := l.St.ListCommentFaces(bvid)
	if err != nil {
		return n, err
	}
	for _, f := range faces {
		if ok, err := l.fetchMissing(ctx, FaceRel(f.Mid), f.URL); err != nil {
			l.Log.Warn("评论头像本地化失败", "mid", f.Mid, "err", err)
		} else if ok {
			n++
		}
	}
	return n, nil
}

// Backfill 全库补齐：全部视频（封面+UP主头像）+ 全部评论者头像。
// 幂等，可反复执行；ctx 取消时中断并返回已下载数。
func (l *Localizer) Backfill(ctx context.Context, progress func(done, total int)) (int, error) {
	videos, err := l.St.ListVideos(0, "", 0, 0)
	if err != nil {
		return 0, err
	}
	faces, err := l.St.ListCommentFaces("")
	if err != nil {
		return 0, err
	}
	total := len(videos) + len(faces)
	done, n := 0, 0
	report := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	for _, v := range videos {
		m, err := l.syncVideoMeta(ctx, v)
		n += m
		report()
		if err != nil {
			l.Log.Warn("视频图片本地化中断", "bvid", v.Bvid, "err", err)
			return n, err
		}
	}
	for _, f := range faces {
		if ok, err := l.fetchMissing(ctx, FaceRel(f.Mid), f.URL); err != nil {
			l.Log.Debug("头像本地化失败", "mid", f.Mid, "err", err)
		} else if ok {
			n++
		}
		report()
		if err := ctx.Err(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// syncVideoMeta 与 SyncVideo 的封面/UP主部分一致（Backfill 里评论头像已批量列出，不重复拉）。
func (l *Localizer) syncVideoMeta(ctx context.Context, v *store.Video) (int, error) {
	n := 0
	if v.Pic != "" {
		if ok, err := l.fetchMissing(ctx, CoverRel(v.Bvid), v.Pic); err != nil {
			l.Log.Debug("封面本地化失败", "bvid", v.Bvid, "err", err)
		} else if ok {
			n++
		}
	}
	if v.OwnerMid > 0 && v.OwnerFace != "" {
		if ok, err := l.fetchMissing(ctx, FaceRel(v.OwnerMid), v.OwnerFace); err != nil {
			l.Log.Debug("UP主头像本地化失败", "mid", v.OwnerMid, "err", err)
		} else if ok {
			n++
		}
	}
	return n, ctx.Err()
}

// fetchMissing 下载 url 到 rel（dataDir 相对路径），原子落盘；已存在直接跳过。
// 返回是否新写入。
func (l *Localizer) fetchMissing(ctx context.Context, rel, url string) (bool, error) {
	full := filepath.Join(l.DataDir, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// B 站历史数据里存在协议相对链接
	if strings.HasPrefix(url, "//") {
		url = "https:" + url
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", l.UA)
	req.Header.Set("Referer", "https://www.bilibili.com/") // 图床对无 B 站 Referer 的浏览器请求放行，但带上最稳
	time.Sleep(l.Pace)
	resp, err := l.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return false, err
	}
	if len(b) < 64 {
		return false, fmt.Errorf("响应过小(%d字节)，疑似非图片", len(b))
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, err
	}
	tmp := full + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}
