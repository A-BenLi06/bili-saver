package biliapi

// 各业务端点。每个方法返回解析后的结构；原始响应通过 SnapshotHook 落盘。

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"bili-saver/internal/dmproto"
)

// VideoView 视频基础信息（x/web-interface/view）。
func (c *Client) VideoView(ctx context.Context, bvid string) (*VideoView, error) {
	p := url.Values{"bvid": {bvid}}
	var d VideoView
	if err := c.getJSON(ctx, "/x/web-interface/view", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// VideoViewAid 按 av 号取视频基础信息。
func (c *Client) VideoViewAid(ctx context.Context, aid int64) (*VideoView, error) {
	p := url.Values{"aid": {strconv.FormatInt(aid, 10)}}
	var d VideoView
	if err := c.getJSON(ctx, "/x/web-interface/view", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// VideoTags 视频标签。
func (c *Client) VideoTags(ctx context.Context, aid int64, bvid string) ([]TagItem, error) {
	p := url.Values{"bvid": {bvid}, "aid": {strconv.FormatInt(aid, 10)}}
	var d []TagItem
	if err := c.getJSON(ctx, "/x/tag/archive/tags", p, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// PlayURL 取播放地址（wbi）。qn 为期望清晰度 id；fnval 16=DASH，144=DASH+4K。
func (c *Client) PlayURL(ctx context.Context, bvid string, aid, cid int64, qn, fnval int) (*PlayURL, error) {
	p := url.Values{
		"bvid":  {bvid},
		"cid":   {strconv.FormatInt(cid, 10)},
		"qn":    {strconv.Itoa(qn)},
		"fnval": {strconv.Itoa(fnval)},
		"fnver": {"0"},
		"fourk": {"1"},
	}
	if aid != 0 {
		p.Set("aid", strconv.FormatInt(aid, 10))
	}
	var d PlayURL
	if err := c.getJSONWbi(ctx, "/x/player/wbi/playurl", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// PlayerInfo 播放器信息（CC 字幕列表等，wbi）。
func (c *Client) PlayerInfo(ctx context.Context, bvid string, aid, cid int64) (*PlayerInfo, error) {
	p := url.Values{
		"bvid": {bvid},
		"cid":  {strconv.FormatInt(cid, 10)},
	}
	if aid != 0 {
		p.Set("aid", strconv.FormatInt(aid, 10))
	}
	var d PlayerInfo
	if err := c.getJSONWbi(ctx, "/x/player/wbi/v2", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Conclusion B 站官方 AI 视频小结（origin 层数据；无小结时返回错误，调用方忽略）。
func (c *Client) Conclusion(ctx context.Context, bvid string, cid, upMid int64) (*Conclusion, error) {
	p := url.Values{
		"bvid":   {bvid},
		"cid":    {strconv.FormatInt(cid, 10)},
		"up_mid": {strconv.FormatInt(upMid, 10)},
	}
	var d Conclusion
	if err := c.getJSONWbi(ctx, "/x/web-interface/view/conclusion/get", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DanmakuSeg 拉取一段（6 分钟）弹幕 protobuf，同时返回原始字节供落盘。
// 优先使用 wbi 签名端点，失败回退旧端点。
func (c *Client) DanmakuSeg(ctx context.Context, cid int64, seg int) ([]dmproto.DanmakuElem, []byte, error) {
	base := url.Values{
		"type":    {"1"},
		"oid":     {strconv.FormatInt(cid, 10)},
		"segment": {strconv.Itoa(seg)},
	}
	if mixin, err := c.ensureWbi(ctx); err == nil {
		signed, _ := wbiSignParams(base, mixin)
		raw, err := c.getRaw(ctx, "/x/v2/dm/wbi/web/seg.so", signed)
		if err == nil {
			elems, perr := dmproto.ParseSegReply(raw)
			if perr != nil {
				return nil, raw, fmt.Errorf("解析弹幕段 %d: %w", seg, perr)
			}
			return elems, raw, nil
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		// wbi 端点失败 → 回退旧端点
	}
	raw, err := c.getRaw(ctx, "/x/v2/dm/web/seg.so", base)
	if err != nil {
		return nil, nil, err
	}
	elems, perr := dmproto.ParseSegReply(raw)
	if perr != nil {
		return nil, raw, fmt.Errorf("解析弹幕段 %d: %w", seg, perr)
	}
	return elems, raw, nil
}

// ReplyMain 主楼评论（wbi，mode=3 按热度，翻页走 pagination offset）。
func (c *Client) ReplyMain(ctx context.Context, aid int64, offset string) (*ReplyMainData, error) {
	p := url.Values{
		"oid":            {strconv.FormatInt(aid, 10)},
		"type":           {"1"},
		"mode":           {"3"},
		"pagination_str": {mustJSON(map[string]string{"offset": offset})},
		"plat":           {"1"},
		"web_location":   {"1315875"},
	}
	var d ReplyMainData
	if err := c.getJSONWbi(ctx, "/x/v2/reply/wbi/main", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ReplySub 楼中楼评论（按页）。
func (c *Client) ReplySub(ctx context.Context, aid, root int64, pn, ps int) (*ReplySubData, error) {
	p := url.Values{
		"oid":  {strconv.FormatInt(aid, 10)},
		"type": {"1"},
		"root": {strconv.FormatInt(root, 10)},
		"pn":   {strconv.Itoa(pn)},
		"ps":   {strconv.Itoa(ps)},
	}
	var d ReplySubData
	if err := c.getJSON(ctx, "/x/v2/reply/reply", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// FavResourceList 收藏夹内容。
func (c *Client) FavResourceList(ctx context.Context, mediaID int64, pn, ps int) (*FavListData, error) {
	p := url.Values{
		"media_id": {strconv.FormatInt(mediaID, 10)},
		"pn":       {strconv.Itoa(pn)},
		"ps":       {strconv.Itoa(ps)},
		"order":    {"mtime"},
		"type":     {"0"},
		"tid":      {"0"},
		"platform": {"web"},
	}
	var d FavListData
	if err := c.getJSON(ctx, "/x/v3/fav/resource/list", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// SpaceAccInfo 用户空间资料（wbi 签名，等级/认证等；未登录态可能风控 -352/-412）。
func (c *Client) SpaceAccInfo(ctx context.Context, mid int64) (*SpaceAccInfo, error) {
	if err := c.EnsureBuvid(ctx); err != nil {
		return nil, err
	}
	p := url.Values{
		"mid":          {strconv.FormatInt(mid, 10)},
		"platform":     {"web"},
		"web_location": {"1550101"},
	}
	mixin, err := c.ensureWbi(ctx)
	if err != nil {
		return nil, err
	}
	signed, _ := wbiSignParams(p, mixin)
	if err := c.lim.wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + "/x/space/wbi/acc/info?" + signed.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, map[string]string{
		"Referer": fmt.Sprintf("https://space.bilibili.com/%d/", mid),
	})
	if err != nil {
		return nil, err
	}
	c.snapshot("/x/space/wbi/acc/info", p, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("space acc info: http %d 非 JSON（可能风控 412）", status)
	}
	if env.Code != 0 {
		return nil, &APIError{Code: env.Code, Message: env.Message}
	}
	var d SpaceAccInfo
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

// SpaceArcSearch UP 主投稿列表（wbi + buvid + 空间页 Referer，缺一常被 412 风控）。
func (c *Client) SpaceArcSearch(ctx context.Context, mid int64, pn, ps int) (*SpaceArcData, error) {
	if err := c.EnsureBuvid(ctx); err != nil {
		return nil, err
	}
	p := url.Values{
		"mid":           {strconv.FormatInt(mid, 10)},
		"pn":            {strconv.Itoa(pn)},
		"ps":            {strconv.Itoa(ps)},
		"order":         {"pubdate"},
		"platform":      {"web"},
		"web_location":  {"1550101"},
		"order_avoided": {"true"},
		// 反风控指纹参数（社区实践）：缺失常触发 -352
		"dm_img_list":      {"[]"},
		"dm_img_str":       {"V2ViR0wgMS4wIChPcGVuR0wgRVMgMy4wKSBDcm9tZXQ"},
		"dm_cover_img_str": {"V2ViR0wxLjAgKE9wZW5HTCBFUyAzLjApIENyb21ldA"},
		"dm_img_inter":     {"{\"ds\":[],\"wh\":[3660,1871,79],\"of\":[320,694,320]}"},
	}
	mixin, err := c.ensureWbi(ctx)
	if err != nil {
		return nil, err
	}
	signed, _ := wbiSignParams(p, mixin)
	if err := c.lim.wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + "/x/space/wbi/arc/search?" + signed.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, map[string]string{
		"Referer": fmt.Sprintf("https://space.bilibili.com/%d/", mid),
	})
	if err != nil {
		return nil, err
	}
	c.snapshot("/x/space/wbi/arc/search", p, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("space arc search: http %d 非 JSON（可能风控 412）", status)
	}
	if env.Code != 0 {
		return nil, &APIError{Code: env.Code, Message: env.Message}
	}
	var d SpaceArcData
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

// SeasonArchives 合集（season）内容。
func (c *Client) SeasonArchives(ctx context.Context, mid, seasonID int64, page, size int) (*SeasonArcData, error) {
	p := url.Values{
		"mid":          {strconv.FormatInt(mid, 10)},
		"season_id":    {strconv.FormatInt(seasonID, 10)},
		"page_num":     {strconv.Itoa(page)},
		"page_size":    {strconv.Itoa(size)},
		"sort_reverse": {"false"},
	}
	var d SeasonArcData
	if err := c.getJSON(ctx, "/x/polymer/web-space/seasons_archives_list", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// SeriesArchives 系列（series）内容。
func (c *Client) SeriesArchives(ctx context.Context, mid, seriesID int64, pn, ps int) (*SeasonArcData, error) {
	p := url.Values{
		"mid":         {strconv.FormatInt(mid, 10)},
		"series_id":   {strconv.FormatInt(seriesID, 10)},
		"pn":          {strconv.Itoa(pn)},
		"ps":          {strconv.Itoa(ps)},
		"only_normal": {"true"},
		"sort":        {"desc"},
	}
	var d SeasonArcData
	if err := c.getJSON(ctx, "/x/series/archives", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DanmakuListXML 旧版整段弹幕（list.so，raw-deflate 压缩的 XML）。
// 返回解析后的弹幕列表与解压后的 XML 原文（供落盘）。seg.so 不可用时它是完整回退。
func (c *Client) DanmakuListXML(ctx context.Context, cid int64) ([]dmproto.DanmakuElem, []byte, error) {
	p := url.Values{"oid": {strconv.FormatInt(cid, 10)}}
	raw, err := c.getRaw(ctx, "/x/v1/dm/list.so", p)
	if err != nil {
		return nil, nil, err
	}
	xmlBytes, err := inflateAny(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("解压弹幕 XML: %w", err)
	}
	elems, perr := parseDanmakuXML(xmlBytes)
	if perr != nil {
		return nil, xmlBytes, fmt.Errorf("解析弹幕 XML: %w", perr)
	}
	return elems, xmlBytes, nil
}

// inflateAny 兼容 raw-deflate / zlib / gzip / 未压缩。
func inflateAny(b []byte) ([]byte, error) {
	if len(b) > 1 && b[0] == 0x1f && b[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(gr)
	}
	if len(b) > 1 && b[0] == 0x78 { // zlib
		zr, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(zr)
	}
	fr := flate.NewReader(bytes.NewReader(b))
	out, err := io.ReadAll(fr)
	if err == nil {
		return out, nil
	}
	if len(b) > 0 && b[0] == '<' {
		return b, nil // 未压缩 XML
	}
	return nil, err
}

// danmuXMLDoc 对应 list.so 的 XML 结构。
type danmuXMLDoc struct {
	D []struct {
		P       string `xml:"p,attr"`
		Content string `xml:",chardata"`
	} `xml:"d"`
}

func parseDanmakuXML(xmlBytes []byte) ([]dmproto.DanmakuElem, error) {
	var doc danmuXMLDoc
	if err := xml.Unmarshal(xmlBytes, &doc); err != nil {
		return nil, err
	}
	out := make([]dmproto.DanmakuElem, 0, len(doc.D))
	for _, d := range doc.D {
		f := strings.Split(d.P, ",")
		get := func(i int) float64 {
			if i >= len(f) {
				return 0
			}
			v, _ := strconv.ParseFloat(f[i], 64)
			return v
		}
		var e dmproto.DanmakuElem
		e.Progress = int32(get(0) * 1000)
		e.Mode = int32(get(1))
		e.Fontsize = int32(get(2))
		e.Color = uint32(get(3))
		e.Ctime = int64(get(4))
		if len(f) > 7 {
			if v, err := strconv.ParseInt(f[7], 10, 64); err == nil {
				e.ID = v
			}
		}
		e.Content = d.Content
		out = append(out, e)
	}
	return out, nil
}

// SubtitleCC 下载 CC 字幕 JSON（bcc）。
func (c *Client) SubtitleCC(ctx context.Context, subtitleURL string) ([]byte, error) {
	if len(subtitleURL) > 2 && subtitleURL[0] == '/' && subtitleURL[1] == '/' {
		subtitleURL = "https:" + subtitleURL
	}
	body, status, err := c.do(ctx, "GET", subtitleURL, nil, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("下载字幕 http %d", status)
	}
	return body, nil
}

func mustJSON(v any) string {
	b, _ := jsonMarshal(v)
	return string(b)
}

// DynamicFeed UP主动态 feed（polymer）。免 wbi 签名（2026-09-11 实测），
// 未登录或软风控时返回 -101/-352 由调用方按 APIError 处理。
// 纯文字(WORD)/图文(DRAW)/专栏文章(ARTICLE)/视频动态(AV)/转发(FORWARD) 同源。
func (c *Client) DynamicFeed(ctx context.Context, mid int64, offset string) (*DynFeed, error) {
	if err := c.EnsureBuvid(ctx); err != nil {
		return nil, err
	}
	p := url.Values{"host_mid": {strconv.FormatInt(mid, 10)}}
	if offset != "" {
		p.Set("offset", offset)
	}
	if err := c.lim.wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + "/x/polymer/web-dynamic/v1/feed/space?" + p.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, map[string]string{
		"Referer": fmt.Sprintf("https://space.bilibili.com/%d/dynamic", mid),
	})
	if err != nil {
		return nil, err
	}
	c.snapshot("/x/polymer/web-dynamic/v1/feed/space", p, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("dynamic feed: http %d 非 JSON（可能风控）", status)
	}
	if env.Code != 0 {
		return nil, &APIError{Code: env.Code, Message: env.Message}
	}
	var d DynFeed
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return nil, err
	}
	// 保留每条动态的原始 JSON（保真 + 免受解析字段不全影响）
	var raw struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(env.Data, &raw)
	for i := range d.Items {
		if i < len(raw.Items) {
			d.Items[i].Raw = string(raw.Items[i])
		}
	}
	return &d, nil
}

// SpaceFollowList UP主追番(type=1)/追剧(type=2)列表（2026-09-11 实测免 wbi；
// 用户关闭列表公开可见时返回空列表）。
func (c *Client) SpaceFollowList(ctx context.Context, mid int64, typ, pn, ps int) (*SpaceFollowList, error) {
	if err := c.EnsureBuvid(ctx); err != nil {
		return nil, err
	}
	p := url.Values{
		"vmid": {strconv.FormatInt(mid, 10)},
		"type": {strconv.Itoa(typ)},
		"pn":   {strconv.Itoa(pn)},
		"ps":   {strconv.Itoa(ps)},
	}
	if err := c.lim.wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + "/x/space/bangumi/follow/list?" + p.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, map[string]string{
		"Referer": fmt.Sprintf("https://space.bilibili.com/%d/bangumi", mid),
	})
	if err != nil {
		return nil, err
	}
	c.snapshot("/x/space/bangumi/follow/list", p, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("space follow list: http %d 非 JSON（可能风控）", status)
	}
	if env.Code != 0 {
		return nil, &APIError{Code: env.Code, Message: env.Message}
	}
	var d SpaceFollowList
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return nil, err
	}
	// 保留每条原始 JSON
	var raw struct {
		List []json.RawMessage `json:"list"`
	}
	_ = json.Unmarshal(env.Data, &raw)
	for i := range d.List {
		if i < len(raw.List) {
			d.List[i].Raw = string(raw.List[i])
		}
	}
	return &d, nil
}
