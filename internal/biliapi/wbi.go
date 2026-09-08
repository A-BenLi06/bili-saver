package biliapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// WBI 签名：依据 bilibili-API-collect 的公开算法。
// w_rid = md5(sorted_query + mixin_key)，query 先附加 wts 再排序。

var mixinKeyEncTab = [64]int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51,
	30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52}

func getMixinKey(orig string) string {
	var b strings.Builder
	for _, i := range mixinKeyEncTab {
		if i < len(orig) {
			b.WriteByte(orig[i])
		}
	}
	s := b.String()
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// wbiValue 等价 JS encodeURIComponent 后移除 !'()*。
func wbiValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '!', c == '\'', c == '(', c == ')', c == '*':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	out := b.String()
	for _, ch := range []string{"!", "'", "(", ")", "*"} {
		out = strings.ReplaceAll(out, ch, "")
	}
	return out
}

func wbiSignParams(params url.Values, mixinKey string) (url.Values, string) {
	p := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			p.Add(k, v)
		}
	}
	p.Set("wts", fmt.Sprintf("%d", time.Now().Unix()))
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var q strings.Builder
	for _, k := range keys {
		q.WriteString(wbiValue(k))
		q.WriteString("=")
		q.WriteString(wbiValue(p.Get(k)))
		q.WriteString("&")
	}
	s := strings.TrimSuffix(q.String(), "&")
	sum := md5.Sum([]byte(s + mixinKey))
	p.Set("w_rid", hex.EncodeToString(sum[:]))
	return p, s
}

// navData 只取 wbi_img 两个字段。
type navData struct {
	IsLogin bool  `json:"isLogin"`
	Mid     int64 `json:"mid"`
	WbiImg  struct {
		ImgURL string `json:"img_url"`
		SubURL string `json:"sub_url"`
	} `json:"wbi_img"`
}

func keyFromURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexByte(u, '.'); i >= 0 {
		u = u[:i]
	}
	return u
}

func (c *Client) ensureWbi(ctx context.Context) (string, error) {
	c.wbiMu.Lock()
	defer c.wbiMu.Unlock()
	if time.Now().Before(c.wbiUntil) && c.wbiImg != "" {
		return getMixinKey(c.wbiImg + c.wbiSub), nil
	}
	var nav navData
	// 未登录时 nav 返回 code=-101，但 wbi_img 仍在 data 里，这里不能按业务错误中断
	if err := c.getJSONLenient(ctx, "/x/web-interface/nav", url.Values{}, &nav); err != nil {
		return "", fmt.Errorf("获取 wbi key 失败: %w", err)
	}
	c.wbiImg = keyFromURL(nav.WbiImg.ImgURL)
	c.wbiSub = keyFromURL(nav.WbiImg.SubURL)
	if c.wbiImg == "" || c.wbiSub == "" {
		return "", fmt.Errorf("nav 未返回 wbi key")
	}
	c.wbiUntil = time.Now().Add(time.Hour)
	return getMixinKey(c.wbiImg + c.wbiSub), nil
}

// EnsureWbi 返回当前 mixin key（触发刷新）；导出供探针/调试工具使用。
func (c *Client) EnsureWbi(ctx context.Context) (string, error) { return c.ensureWbi(ctx) }

// SignParams 导出的 wbi 签名（探针/调试用）。
func SignParams(params url.Values, mixinKey string) (url.Values, string) {
	return wbiSignParams(params, mixinKey)
}

// getJSONWbi 带签名的 JSON 请求。
func (c *Client) getJSONWbi(ctx context.Context, endpoint string, params url.Values, out any) error {
	mixin, err := c.ensureWbi(ctx)
	if err != nil {
		return err
	}
	signed, _ := wbiSignParams(params, mixin)
	return c.getJSON(ctx, endpoint, signed, out)
}
