package collector

// space_dynamics.go — UP主动态/图文（专栏）归档（v10）。
// 数据源：polymer 动态 feed（AV/WORD/DRAW/FORWARD/ARTICLE 同源；2026-09-11 实测可用，
// 免 wbi）。传统专栏列表接口已死（legacy x/space/article 全员空、polymer opus 404），
// 专栏以 feed 中 ARTICLE/图文类动态形式归档（kind=article）。
// 消失语义对齐 comments：只增不删，feed 拉到底才允许标记消失，大面积消失走防御。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
)

const (
	dynMaxPages = 200 // 翻页硬上限（防接口异常导致死循环），每页约 12-20 条
)

// SpaceScope UP主源归档范围（sources.space_scope；空串=全部）。
type SpaceScope struct {
	Video        bool // 投稿视频（arc_search 展开链路）
	Dynamic      bool // 所有动态（纯文字/图文/转发/专栏文章等）
	DynamicVideo bool // 动态视频（视频动态里的稿件，发现即归档）
	Article      bool // 专栏（文章/图文类，kind=article）
	Bangumi      bool // 追番/追剧列表（follow list，kind=bangumi）
}

// scopeTokens 归档范围 token 集合；”=未设置（默认全部）、'all'=全部、'none'=全部关闭。
var scopeTokens = map[string]int{
	"video": 0, "dynamic": 1, "dynamic_video": 2, "article": 3, "bangumi": 4, "none": 5, "all": 6,
}

// ParseSpaceScope 解析逗号分隔 scope；空串或 'all' = 全部开启；'none' = 全部关闭。
func ParseSpaceScope(s string) SpaceScope {
	sc := SpaceScope{Video: true, Dynamic: true, DynamicVideo: true, Article: true, Bangumi: true}
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return sc
	}
	if s == "none" {
		return SpaceScope{}
	}
	sc = SpaceScope{}
	for _, tok := range strings.Split(s, ",") {
		if idx, ok := scopeTokens[strings.TrimSpace(tok)]; ok && idx < 5 {
			switch idx {
			case 0:
				sc.Video = true
			case 1:
				sc.Dynamic = true
			case 2:
				sc.DynamicVideo = true
			case 3:
				sc.Article = true
			case 4:
				sc.Bangumi = true
			}
		}
	}
	return sc
}

// ValidSpaceScope 校验并归一化用户输入；非法 token 返回 false。
// ”=未设置（保持默认全部）；'all'=显式全部；'none'=显式全关。
func ValidSpaceScope(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" || s == "none" {
		return s, true
	}
	var toks []string
	seen := map[string]bool{}
	for _, tok := range strings.Split(s, ",") {
		t := strings.TrimSpace(tok)
		if t == "" {
			continue
		}
		if _, ok := scopeTokens[t]; !ok || t == "none" || t == "all" {
			return "", false
		}
		if !seen[t] {
			seen[t] = true
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return "", true
	}
	if len(toks) == 5 {
		return "all", true
	}
	return strings.Join(toks, ","), true
}

// SyncSpaceDynamics 全量拉取 UP 主动态并归档；返回发现的视频稿件 bvid
// （供"动态视频"勾选归档）。只在 feed 拉到底时才标记消失（防接口截断）。
func (c *Collector) SyncSpaceDynamics(ctx context.Context, mid int64) (videoBvids []string, err error) {
	offset := ""
	now := store.Now()
	seen := map[string]bool{}
	full := false
	for page := 0; page < dynMaxPages; page++ {
		feed, err := c.API.DynamicFeed(ctx, mid, offset)
		if err != nil {
			return videoBvids, err
		}
		if len(feed.Items) == 0 {
			full = true
			break
		}
		for _, it := range feed.Items {
			p := spacePostOf(mid, it, now)
			if p == nil {
				continue
			}
			if err := c.St.UpsertSpacePost(p); err != nil {
				return videoBvids, err
			}
			seen[p.PostID] = true
			if p.Bvid != "" {
				videoBvids = append(videoBvids, p.Bvid)
			}
		}
		if !feed.HasMore || feed.NextOffset == "" || feed.NextOffset == offset {
			full = true
			break
		}
		offset = feed.NextOffset
	}
	if gone, skipped := c.St.MarkSpacePostsGone(mid, []string{"dynamic", "article"}, seen, full); gone > 0 {
		c.Log.Info("动态消失标记", "mid", mid, "消失", gone)
	} else if skipped {
		c.Log.Warn("动态消失标记跳过（非全量或触发防御）", "mid", mid)
	}
	return videoBvids, nil
}

// spacePostOf 动态条目 → 归档记录。专栏/图文类（ARTICLE/新版 opus）kind=article。
func spacePostOf(mid int64, it biliapi.DynFeedItem, now int64) *store.SpacePost {
	if it.ID == "" {
		return nil
	}
	p := &store.SpacePost{
		Mid: mid, PostID: it.ID, Dtype: it.Type, Kind: "dynamic",
		Origin: "bilibili", FirstSeenAt: now, LastSeenAt: now, Raw: it.Raw,
		Pubdate: int64(it.Modules.ModuleAuthor.PubTs),
	}
	md := it.Modules.ModuleDynamic
	p.Content = strings.TrimSpace(strings.TrimSpace(md.Content.Text) + "\n" + strings.TrimSpace(md.Content.Description))
	major := md.Major
	switch {
	case major.Archive != nil: // 视频动态
		p.Bvid = major.Archive.Bvid
		p.Title = major.Archive.Title
		if p.Content == "" {
			p.Content = strings.TrimSpace(major.Archive.Desc)
		}
	case major.Article != nil: // 专栏文章
		p.Kind = "article"
		p.Title = major.Article.Title
		p.Content = strings.TrimSpace(major.Article.Desc)
	case major.Opus != nil: // 新版图文（专栏新形态）
		p.Kind = "article"
		p.Title = major.Opus.Title
		p.Pics = len(major.Opus.Images)
		p.Content = strings.TrimSpace(p.Content + "\n" + opusText(major.Opus.Content))
	case major.Draw != nil: // 图片动态
		p.Pics = len(major.Draw.Items)
		p.Title = major.Draw.Title
	}
	if strings.Contains(it.Type, "ARTICLE") {
		p.Kind = "article"
	}
	p.Content = strings.TrimSpace(p.Content)
	return p
}

// opusText 从新版图文 content 富文本 JSON 里递归提取全部文本节点。
func opusText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var walk func(v any) string
	walk = func(v any) string {
		switch t := v.(type) {
		case map[string]any:
			var sb strings.Builder
			for k, child := range t {
				switch k {
				case "text", "message":
					if str, ok := child.(string); ok && str != "" {
						sb.WriteString(str)
					} else {
						sb.WriteString(walk(child))
					}
				default:
					sb.WriteString(walk(child))
				}
			}
			return sb.String()
		case []any:
			var sb strings.Builder
			for _, child := range t {
				sb.WriteString(walk(child))
			}
			return sb.String()
		}
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return strings.TrimSpace(walk(v))
}

// SyncSpaceBangumi 全量拉取 UP 主的追番/追剧列表（follow list type=1/2），
// kind=bangumi 归档（post_id=season_id，dtype 区分追番/追剧，正文=番剧简介，
// 原始 JSON 保留）。列表接口分页拉到底，支持消失标记（取关/下架）。
func (c *Collector) SyncSpaceBangumi(ctx context.Context, mid int64) error {
	now := store.Now()
	seen := map[string]bool{}
	const ps = 20
	for _, typ := range []struct {
		num   int
		label string
	}{{1, "follow_bangumi"}, {2, "follow_cinema"}} {
		for pn := 1; pn <= 100; pn++ { // 每页 20，100 页硬上限
			list, err := c.API.SpaceFollowList(ctx, mid, typ.num, pn, ps)
			if err != nil {
				return err
			}
			if len(list.List) == 0 {
				break
			}
			for _, e := range list.List {
				p := &store.SpacePost{
					Mid: mid, PostID: fmt.Sprintf("ss%d", e.SeasonID), Dtype: typ.label,
					Kind: "bangumi", Title: e.Title, Content: strings.TrimSpace(e.Evaluate),
					Origin: "bilibili", Raw: e.Raw, Pubdate: 0,
					FirstSeenAt: now, LastSeenAt: now,
				}
				if e.TotalCount > 0 {
					p.Pics = 0
					p.Content = strings.TrimSpace(p.Content + fmt.Sprintf("\n[共 %d 集]", e.TotalCount))
				}
				if err := c.St.UpsertSpacePost(p); err != nil {
					return err
				}
				seen[p.PostID] = true
			}
			if len(list.List) < ps {
				break
			}
		}
	}
	if gone, skipped := c.St.MarkSpacePostsGone(mid, []string{"bangumi"}, seen, true); gone > 0 {
		c.Log.Info("追番列表消失标记", "mid", mid, "消失", gone)
	} else if skipped {
		c.Log.Warn("追番列表消失标记跳过（防御触发）", "mid", mid)
	}
	return nil
}
