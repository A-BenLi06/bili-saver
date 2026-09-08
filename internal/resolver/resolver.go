// Package resolver 把用户输入（链接 / BV号 / UP主 / 收藏夹 / 合集 / 系列）
// 解析为统一源描述，并可展开为视频清单。
package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"bili-saver/internal/biliapi"
)

type Type string

const (
	TypeVideo   Type = "video"
	TypeSpace   Type = "space"
	TypeFavList Type = "favlist"
	TypeSeason  Type = "season"
	TypeSeries  Type = "series"
)

// Input 是一个归档源的规范描述。
type Input struct {
	Type  Type
	ExtID string
	Extra map[string]string
}

func (i *Input) ExtraJSON() string {
	b, _ := json.Marshal(i.Extra)
	return string(b)
}

func (i *Input) String() string {
	switch i.Type {
	case TypeVideo:
		return "视频 " + i.ExtID
	case TypeSpace:
		return "UP主 mid=" + i.ExtID
	case TypeFavList:
		return "收藏夹 " + i.ExtID
	case TypeSeason:
		return "合集 " + i.ExtID
	case TypeSeries:
		return "系列 " + i.ExtID
	}
	return string(i.Type) + ":" + i.ExtID
}

var (
	reBV     = regexp.MustCompile(`(?i)^BV[0-9A-Za-z]{10}$`)
	reAV     = regexp.MustCompile(`(?i)^av([0-9]+)$`)
	reNum    = regexp.MustCompile(`^[0-9]{4,}$`)
	reBVIn   = regexp.MustCompile(`(?i)BV[0-9A-Za-z]{10}`)
	reAVIn   = regexp.MustCompile(`(?i)\bav([0-9]+)\b`)
	reMedial = regexp.MustCompile(`(?i)^medialist/(?:play|detail)/([^/?]+)`)
)

// Parse 解析纯文本输入。支持:
//
//	BV1xxxx / av123
//	www.bilibili.com/video/BV...
//	space.bilibili.com/{mid}[/favlist?fid=|/channel/collectiondetail?sid=|/channel/seriesdetail?sid=|/lists/{sid}?type=season|series]
//	www.bilibili.com/lists/{sid}?type=season|series
//	www.bilibili.com/medialist/...?media_id=
//	纯数字(≥4位) 视为 UP主 mid
func Parse(raw string) (*Input, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("空输入")
	}
	if reBV.MatchString(s) {
		return &Input{Type: TypeVideo, ExtID: s}, nil
	}
	if m := reAV.FindStringSubmatch(s); m != nil {
		return &Input{Type: TypeVideo, ExtID: "av" + m[1]}, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		if reNum.MatchString(s) {
			return &Input{Type: TypeSpace, ExtID: s}, nil
		}
		return nil, fmt.Errorf("无法识别的输入: %q", raw)
	}
	host := strings.ToLower(u.Host)
	path := strings.Trim(u.Path, "/")
	q := u.Query()

	switch {
	case strings.HasPrefix(host, "space.bilibili.com"):
		mid := strings.TrimPrefix(host, "space.bilibili.com")
		if mid == "" && path != "" {
			parts := strings.SplitN(path, "/", 2)
			mid = parts[0]
			if len(parts) == 2 {
				path = strings.Trim(parts[1], "/")
			} else {
				path = ""
			}
		}
		if mid == "" || !reNum.MatchString(mid) {
			return nil, fmt.Errorf("space 链接缺少 mid: %q", raw)
		}
		switch {
		case strings.HasPrefix(path, "favlist"):
			fid := q.Get("fid")
			if fid == "" {
				return nil, fmt.Errorf("收藏夹链接缺少 fid: %q", raw)
			}
			return &Input{Type: TypeFavList, ExtID: fid, Extra: map[string]string{"mid": mid}}, nil
		case strings.HasPrefix(path, "channel/collectiondetail"):
			sid := q.Get("sid")
			if sid == "" {
				return nil, fmt.Errorf("合集链接缺少 sid: %q", raw)
			}
			return &Input{Type: TypeSeason, ExtID: sid, Extra: map[string]string{"mid": mid}}, nil
		case strings.HasPrefix(path, "channel/seriesdetail"):
			sid := q.Get("sid")
			if sid == "" {
				return nil, fmt.Errorf("系列链接缺少 sid: %q", raw)
			}
			return &Input{Type: TypeSeries, ExtID: sid, Extra: map[string]string{"mid": mid}}, nil
		case strings.HasPrefix(path, "lists/"):
			return parseLists(strings.TrimPrefix(path, "lists/"), q)
		default:
			return &Input{Type: TypeSpace, ExtID: mid}, nil
		}
	case strings.Contains(host, "bilibili.com"), host == "b23.tv":
		if strings.HasPrefix(path, "lists/") {
			return parseLists(strings.TrimPrefix(path, "lists/"), q)
		}
		if m := reMedial.FindStringSubmatch(path); m != nil {
			return &Input{Type: TypeFavList, ExtID: m[1]}, nil
		}
		if mid := q.Get("media_id"); mid != "" {
			return &Input{Type: TypeFavList, ExtID: mid}, nil
		}
		if m := reBVIn.FindString(path); m != "" && strings.HasPrefix(path, "video") {
			return &Input{Type: TypeVideo, ExtID: m}, nil
		}
		if m := reBVIn.FindString(path); m != "" {
			return &Input{Type: TypeVideo, ExtID: m}, nil
		}
		if m := reAVIn.FindStringSubmatch(path); m != nil {
			return &Input{Type: TypeVideo, ExtID: "av" + m[1]}, nil
		}
		if fid := q.Get("fid"); fid != "" {
			return &Input{Type: TypeFavList, ExtID: fid}, nil
		}
		return nil, fmt.Errorf("无法识别的 B 站链接: %q", raw)
	}
	return nil, fmt.Errorf("不支持的域名: %q", raw)
}

func parseLists(rest string, q url.Values) (*Input, error) {
	sid := rest
	if i := strings.IndexByte(sid, '/'); i >= 0 {
		sid = sid[:i]
	}
	if sid == "" {
		return nil, fmt.Errorf("lists 链接缺少 id")
	}
	if q.Get("type") == "series" {
		return &Input{Type: TypeSeries, ExtID: sid}, nil
	}
	// 默认按合集处理
	return &Input{Type: TypeSeason, ExtID: sid}, nil
}

// ExpandShortLink 展开 b23.tv 短链（最多跟随 3 跳）。
func ExpandShortLink(ctx context.Context, raw string) (string, error) {
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("短链跳转过深")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Request.URL.String(), nil
}

// Normalize 先展开短链再解析。
func Normalize(ctx context.Context, raw string) (*Input, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "http") {
		if u, err := url.Parse(s); err == nil && strings.Contains(u.Host, "b23.tv") {
			expanded, err := ExpandShortLink(ctx, s)
			if err != nil {
				return nil, fmt.Errorf("展开短链失败: %w", err)
			}
			s = expanded
		}
	}
	return Parse(s)
}

// Expanded 是一个源展开的结果。
type Expanded struct {
	Bvids []string
	Title string
}

// Expand 把源展开为 bvid 列表。
func Expand(ctx context.Context, api *biliapi.Client, in *Input) (*Expanded, error) {
	switch in.Type {
	case TypeVideo:
		return &Expanded{Bvids: []string{in.ExtID}}, nil
	case TypeSpace:
		return expandSpace(ctx, api, in)
	case TypeFavList:
		return expandFavList(ctx, api, in)
	case TypeSeason:
		return expandSeason(ctx, api, in)
	case TypeSeries:
		return expandSeries(ctx, api, in)
	}
	return nil, fmt.Errorf("未知源类型 %q", in.Type)
}

func expandSpace(ctx context.Context, api *biliapi.Client, in *Input) (*Expanded, error) {
	mid, err := strconv.ParseInt(in.ExtID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("mid 非法: %w", err)
	}
	if !api.HasCookie("SESSDATA") {
		return nil, fmt.Errorf("UP主投稿列表接口被 B 站风控限制，未登录基本不可用；请先 `bili-saver login` 后重试（收藏夹/合集/系列无需登录）")
	}
	out := &Expanded{}
	const ps = 30
	for pn := 1; pn <= 100; pn++ {
		d, err := api.SpaceArcSearch(ctx, mid, pn, ps)
		if err != nil {
			return out, err
		}
		for _, v := range d.List.Vlist {
			if v.Bvid != "" {
				out.Bvids = append(out.Bvids, v.Bvid)
			}
		}
		total := d.Page.Count
		if len(out.Bvids) >= total || len(d.List.Vlist) == 0 {
			break
		}
	}
	return out, nil
}

func expandFavList(ctx context.Context, api *biliapi.Client, in *Input) (*Expanded, error) {
	mediaID, err := strconv.ParseInt(in.ExtID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("收藏夹 id 非法: %w", err)
	}
	out := &Expanded{}
	const ps = 20
	for pn := 1; pn <= 200; pn++ {
		d, err := api.FavResourceList(ctx, mediaID, pn, ps)
		if err != nil {
			return out, err
		}
		if out.Title == "" {
			out.Title = d.Info.Title
		}
		for _, m := range d.Medias {
			if m.Bvid != "" && (m.Type == 2 || m.Type == 0) {
				out.Bvids = append(out.Bvids, m.Bvid)
			}
		}
		if !d.HasMore || len(d.Medias) == 0 {
			break
		}
	}
	return out, nil
}

func expandSeason(ctx context.Context, api *biliapi.Client, in *Input) (*Expanded, error) {
	seasonID, err := strconv.ParseInt(in.ExtID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("合集 id 非法: %w", err)
	}
	var mid int64
	if in.Extra != nil {
		mid, _ = strconv.ParseInt(in.Extra["mid"], 10, 64)
	}
	out := &Expanded{}
	const size = 30
	for page := 1; page <= 100; page++ {
		d, err := api.SeasonArchives(ctx, mid, seasonID, page, size)
		if err != nil {
			return out, err
		}
		if out.Title == "" {
			out.Title = d.Meta.Name
		}
		for _, a := range d.Archives {
			if a.Bvid != "" {
				out.Bvids = append(out.Bvids, a.Bvid)
			}
		}
		if len(d.Archives) < size || len(out.Bvids) >= d.Meta.Total {
			break
		}
	}
	return out, nil
}

func expandSeries(ctx context.Context, api *biliapi.Client, in *Input) (*Expanded, error) {
	seriesID, err := strconv.ParseInt(in.ExtID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("系列 id 非法: %w", err)
	}
	var mid int64
	if in.Extra != nil {
		mid, _ = strconv.ParseInt(in.Extra["mid"], 10, 64)
	}
	out := &Expanded{}
	const ps = 30
	for pn := 1; pn <= 100; pn++ {
		d, err := api.SeriesArchives(ctx, mid, seriesID, pn, ps)
		if err != nil {
			return out, err
		}
		if out.Title == "" {
			out.Title = d.Meta.Name
		}
		for _, a := range d.Archives {
			if a.Bvid != "" {
				out.Bvids = append(out.Bvids, a.Bvid)
			}
		}
		if len(d.Archives) < ps {
			break
		}
	}
	return out, nil
}
