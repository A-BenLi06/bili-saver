// Package card 为每个已归档视频生成一张 B 站风格的快速预览卡片（PNG），
// 存放在 derived 层（data/derived/{bvid}/card.png），供日后分享功能与
// 管理端预览使用。卡片只渲染 origin 层数据与归档完整度统计。
package card

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
	_ "image/jpeg" // 封面 jpg 解码
	_ "image/png"  // 头像 png 解码
)

// Generator 卡片生成器。
type Generator struct {
	St    *store.Store
	Cfg   *config.Config
	Log   *slog.Logger
	faces *faceCache
	icons *iconCache
}

// New 组装生成器（字体与图标在首次渲染时解析并缓存）。
func New(st *store.Store, cfg *config.Config, log *slog.Logger) *Generator {
	if log == nil {
		log = slog.Default()
	}
	return &Generator{St: st, Cfg: cfg, Log: log, faces: newFaceCache(), icons: newIconCache()}
}

// PathFor 卡片输出位置（derived 层）。
func (g *Generator) PathFor(bvid string) string {
	return filepath.Join(g.Cfg.DataDir, "derived", filepath.Base(sanitizeName(bvid)), "card.png")
}

// Exists 卡片是否已生成。
func (g *Generator) Exists(bvid string) bool {
	_, err := os.Stat(g.PathFor(bvid))
	return err == nil
}

// Generate 渲染并写入卡片（原子替换），返回文件路径。
// 已有卡片时默认直接返回（用 ?refresh=1 / Exists 检查控制重渲染）。
func (g *Generator) Generate(ctx context.Context, bvid string) (string, error) {
	if bvid == "" {
		return "", fmt.Errorf("card: 空 bvid")
	}
	if g.Exists(bvid) {
		return g.PathFor(bvid), nil
	}
	return g.RenderToFile(ctx, bvid)
}

// RenderToFile 强制重渲染。
func (g *Generator) RenderToFile(ctx context.Context, bvid string) (string, error) {
	d, err := g.loadData(ctx, bvid)
	if err != nil {
		return "", err
	}
	img := renderCard(d, g.faces, g.icons)
	out := g.PathFor(bvid)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if err := encodePNG(f, img); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return out, nil
}

// commentView 热评视图（卡片只展示正文与元信息，不含楼中楼）。
type commentView struct {
	Name  string
	Face  image.Image
	Text  string
	Likes int
	When  string // 相对时间
	Where string // IP 属地
}

// cardData 渲染输入（全部来自 origin 层 + 归档完整度计数）。
type cardData struct {
	Video       *store.Video
	Stats       *store.StatsSnapshot // 最新统计快照（可能为 nil）
	Tags        []string             // B 站原生标签（最多 6 个）
	DanmakuVis  int
	CommentsVis int
	CommentsDel int
	SubtitleCnt int
	Comments    []commentView // 热门评论（最多 2 条，按点赞）
	Cover       image.Image
	Avatar      image.Image
	GeneratedAt int64
}

// loadData 从库与网络收集渲染输入。
func (g *Generator) loadData(ctx context.Context, bvid string) (*cardData, error) {
	v, err := g.St.GetVideo(bvid)
	if err != nil {
		return nil, fmt.Errorf("card: 查询视频失败: %w", err)
	}
	d := &cardData{Video: v, GeneratedAt: store.Now()}

	// 最新统计（单条查询取末条，不为一条记录加载全量历史）
	if stats, err := g.St.LatestStats(bvid); err == nil {
		d.Stats = stats
	}
	// B 站原生标签
	if tags, err := g.St.VideoTags(bvid); err == nil {
		names := tags["bilibili"]
		if len(names) > 6 {
			names = names[:6]
		}
		d.Tags = names
	}
	// 完整度计数
	if vc, err := g.St.GetVideoWithCounts(bvid); err == nil {
		d.DanmakuVis = vc.DanmakuCount
		d.CommentsVis = vc.CommentsVisible
		d.CommentsDel = vc.CommentsDeleted
		d.SubtitleCnt = vc.SubtitleCount
	}
	// 热门评论（按点赞，最多 2 条）
	d.Comments = g.loadHotComments(ctx, bvid)

	d.Cover = g.loadCover(ctx, v)
	d.Avatar = g.loadAvatar(ctx, v)
	return d, nil
}

// loadHotComments 取点赞最高的现存评论并拉取头像（带磁盘缓存）。
func (g *Generator) loadHotComments(ctx context.Context, bvid string) []commentView {
	rows, err := g.St.ListComments(bvid, "visible", 2, 0)
	if err != nil {
		return nil
	}
	out := make([]commentView, 0, len(rows))
	for _, rc := range rows {
		cv := commentView{
			Name:  rc.MemberName,
			Text:  rc.Content,
			Likes: rc.LikeCount,
			When:  relativeTime(rc.Ctime, store.Now()),
			Where: rc.Location,
		}
		if cv.Name == "" {
			cv.Name = "匿名用户"
		}
		cv.Face = g.loadCommentFace(ctx, rc.Rpid, rc.MemberFace)
		out = append(out, cv)
	}
	return out
}

// loadCommentFace 评论者头像（derived/.cache 下按 rpid 缓存）。
func (g *Generator) loadCommentFace(ctx context.Context, rpid int64, faceURL string) image.Image {
	if faceURL == "" {
		return nil
	}
	cache := filepath.Join(g.Cfg.DataDir, "derived", ".cache", fmt.Sprintf("cmt-%d.jpg", rpid))
	if im := decodeFile(cache); im != nil {
		return im
	}
	im := g.fetchImage(ctx, faceURL)
	if im != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err == nil {
			_ = encodeJPEGFile(cache, im)
		}
	}
	return im
}

// loadCover 封面：本地 media/{bvid}/cover.jpg 优先，否则拉取 v.Pic；失败返回 nil（占位）。
func (g *Generator) loadCover(ctx context.Context, v *store.Video) image.Image {
	local := filepath.Join(g.Cfg.DataDir, "media", sanitizeName(v.Bvid), "cover.jpg")
	if im := decodeFile(local); im != nil {
		return im
	}
	return g.fetchImage(ctx, v.Pic)
}

// loadAvatar UP 主头像：images 包落盘的 media/faces/{mid}.jpg 优先，其次远端拉取
// （带磁盘缓存），失败返回 nil（首字母占位）。
func (g *Generator) loadAvatar(ctx context.Context, v *store.Video) image.Image {
	if v.OwnerFace == "" {
		return nil
	}
	cache := filepath.Join(g.Cfg.DataDir, "media", "faces", fmt.Sprintf("%d.jpg", v.OwnerMid))
	if im := decodeFile(cache); im != nil {
		return im
	}
	cache = filepath.Join(g.Cfg.DataDir, "derived", ".cache", fmt.Sprintf("avatar-%d.jpg", v.OwnerMid))
	if im := decodeFile(cache); im != nil {
		return im
	}
	im := g.fetchImage(ctx, v.OwnerFace)
	if im != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err == nil {
			_ = encodeJPEGFile(cache, im)
		}
	}
	return im
}

var cardHTTP = &http.Client{Timeout: 15 * time.Second}

// fetchImage 拉取 B 站图床图片（协议相对链接补 https，带 Referer 防盗链）。
func (g *Generator) fetchImage(ctx context.Context, url string) image.Image {
	if url == "" || ctx == nil {
		return nil
	}
	if len(url) > 2 && url[0] == '/' && url[1] == '/' {
		url = "https:" + url
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", g.Cfg.Bilibili.UserAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	resp, err := cardHTTP.Do(req)
	if err != nil {
		g.Log.Debug("卡片图片拉取失败", "url", url, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g.Log.Debug("卡片图片拉取失败", "url", url, "status", resp.StatusCode)
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil
	}
	return decodeBytes(b)
}

// sanitizeName 与 downloader/media 桥一致的安全文件名（bvid 本身安全，防御性处理）。
func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 32 && r != '/' && r != '\\' && r != ':' && r != '*' && r != '?' && r != '"' && r != '<' && r != '>' && r != '|' {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
