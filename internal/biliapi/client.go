// Package biliapi 是 B 站 Web API 客户端：限流、WBI 签名、cookie 管理、
// 快照钩子（origin 层数据从这里流出）。所有请求先经全局最小间隔限流。
package biliapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIError 表示 B 站返回 code != 0 的业务错误。
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bilibili api code=%d: %s", e.Code, e.Message)
}

// SnapshotHook 在每个响应返回后被调用（含二进制接口），用于原始快照。
type SnapshotHook func(endpoint string, params url.Values, status int, body []byte)

// Client 是并发安全的 B 站客户端。
type Client struct {
	hc           *http.Client
	dl           *http.Client // CDN 下载专用：无总超时，大文件传输不被 60s 掐断
	jar          *cookiejar.Jar
	lim          *limiter
	userAgent    string
	snapMu       sync.Mutex
	snap         SnapshotHook
	base         string
	passportBase string

	wbiMu    sync.Mutex
	wbiImg   string
	wbiSub   string
	wbiUntil time.Time

	buvidOnce sync.Once

	// cookieHosts 是 SaveCookies 的导出范围（测试可覆盖）
	cookieHosts []string
}

type limiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	t := l.next
	if t.Before(now) {
		t = now
	}
	l.next = t.Add(l.interval)
	l.mu.Unlock()
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
	}
	return nil
}

// New 创建客户端。cookieFile 为空表示不持久化；minReqMs 控制全局请求节奏。
func New(cookieFile string, minReqMs int, userAgent string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if minReqMs <= 0 {
		minReqMs = 350
	}
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36"
	}
	c := &Client{
		hc: &http.Client{Timeout: 60 * time.Second, Jar: jar},
		dl: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		},
		jar:          jar,
		lim:          &limiter{interval: time.Duration(minReqMs) * time.Millisecond},
		userAgent:    userAgent,
		base:         "https://api.bilibili.com",
		passportBase: "https://passport.bilibili.com",
		cookieHosts:  []string{"api.bilibili.com", "www.bilibili.com", "passport.bilibili.com", "bilibili.com"},
	}
	if cookieFile != "" {
		if err := c.LoadCookies(cookieFile); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// SetBase 覆盖 API 地址（测试用）。
func (c *Client) SetBase(apiBase, passportBase string) {
	c.base = apiBase
	c.passportBase = passportBase
}

// UserAgent 返回客户端 UA（下载 CDN 流时需要相同 UA）。
func (c *Client) UserAgent() string { return c.userAgent }

// HTTPClient 暴露底层 http.Client（CDN 下载复用 cookie jar 与代理设置）。
func (c *Client) HTTPClient() *http.Client { return c.hc }

// DownloadClient 返回 CDN 大文件下载专用 http.Client：无总超时（API 的 60s
// 总超时会反复掐断慢速大流），仅限制响应头等待；传输中途挂死由 downloader
// 侧的空闲 watchdog 兜底。cookie jar 与 API client 共享（登录态/风控 cookie
// 必须随下载请求带上）。
func (c *Client) DownloadClient() *http.Client { return c.dl }

// SetSnapshotHook 注册原始快照回调。
func (c *Client) SetSnapshotHook(h SnapshotHook) { c.snap = h }

func (c *Client) snapshot(endpoint string, params url.Values, status int, body []byte) {
	c.snapMu.Lock()
	h := c.snap
	c.snapMu.Unlock()
	if h != nil {
		h(endpoint, params, status, body)
	}
}

// emit 手动上报（如带 params 变换的接口）。
func (c *Client) emit(endpoint string, params url.Values, status int, body []byte) {
	c.snapshot(endpoint, params, status, body)
}

// ---- 请求原语 ----

func (c *Client) do(ctx context.Context, method, fullURL string, body io.Reader, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Origin", "https://www.bilibili.com")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// getJSON 请求 JSON 接口并解包 {code,message,data}。code!=0 返回 *APIError。
func (c *Client) getJSON(ctx context.Context, endpoint string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if err := c.lim.wait(ctx); err != nil {
		return err
	}
	u := c.base + endpoint + "?" + params.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return err
	}
	c.snapshot(endpoint, params, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: 响应非 JSON (http %d): %w", endpoint, status, err)
	}
	if env.Code != 0 {
		return &APIError{Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s: 解析 data: %w", endpoint, err)
		}
	}
	return nil
}

// EnsureBuvid 确保已注入 buvid3/buvid4（首次调用时获取，之后幂等）。
// 空间/播放等接口缺少 buvid 时常被 412 风控。
func (c *Client) EnsureBuvid(ctx context.Context) error {
	var err error
	c.buvidOnce.Do(func() {
		if c.HasCookie("buvid3") {
			return
		}
		err = c.FetchBuvid(ctx)
	})
	return err
}

// getJSONLenient 请求 JSON 接口但容忍业务 code 错误（如未登录的 nav），
// 只要 data 能解析就成功。
func (c *Client) getJSONLenient(ctx context.Context, endpoint string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if err := c.lim.wait(ctx); err != nil {
		return err
	}
	u := c.base + endpoint + "?" + params.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return err
	}
	c.snapshot(endpoint, params, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: 响应非 JSON (http %d): %w", endpoint, status, err)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s: 解析 data: %w", endpoint, err)
		}
	}
	return nil
}

// getRaw 请求二进制/文本接口（弹幕 seg.so 等），返回原始字节。
// 若响应实际是 JSON 错误体（{code:-xxx}），转成 *APIError 返回。
func (c *Client) getRaw(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	if err := c.lim.wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + endpoint + "?" + params.Encode()
	body, status, err := c.do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, err
	}
	c.snapshot(endpoint, params, status, body)
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s: http %d", endpoint, status)
	}
	if len(body) > 0 && body[0] == '{' {
		var env struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &env) == nil && env.Code != 0 {
			return nil, &APIError{Code: env.Code, Message: env.Message}
		}
	}
	return body, nil
}

// RawProbe 导出的原始请求（探针/调试用）。
func (c *Client) RawProbe(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	return c.getRaw(ctx, endpoint, params)
}

// ---- cookies ----

var cookieHosts = []string{"api.bilibili.com", "www.bilibili.com", "passport.bilibili.com", "bilibili.com"}

type storedCookie struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Expires int64  `json:"expires"`
}

// SaveCookies 导出 cookie（SESSDATA 等）到文件。
// 必须保留 Domain 属性：SESSDATA 是 .bilibili.com 域 cookie，
// 丢了 Domain 加载后就不会被发送到 api.bilibili.com。
func (c *Client) SaveCookies(file string) error {
	seen := map[string]bool{}
	out := []storedCookie{}
	for _, h := range c.cookieHosts {
		u := &url.URL{Scheme: "https", Host: h}
		for _, co := range c.jar.Cookies(u) {
			domain := co.Domain
			if domain == "" {
				domain = h
			}
			key := co.Name + "|" + domain + "|" + co.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			var exp int64
			if !co.Expires.IsZero() {
				exp = co.Expires.Unix()
			}
			out = append(out, storedCookie{
				Name: co.Name, Value: co.Value, Domain: domain, Path: co.Path, Expires: exp,
			})
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return osWriteFile(file, b)
}

// LoadCookies 从文件恢复 cookie。
func (c *Client) LoadCookies(file string) error {
	b, err := osReadFile(file)
	if err != nil {
		if errors.Is(err, errFileNotFound) {
			return nil
		}
		return err
	}
	var stored []storedCookie
	if err := json.Unmarshal(b, &stored); err != nil {
		return fmt.Errorf("cookie 文件损坏: %w", err)
	}
	byDomain := map[string][]*http.Cookie{}
	for _, sc := range stored {
		if sc.Name == "" || sc.Value == "" {
			continue
		}
		co := &http.Cookie{Name: sc.Name, Value: sc.Value, Domain: sc.Domain, Path: sc.Path}
		if sc.Expires > 0 {
			co.Expires = time.Unix(sc.Expires, 0)
		}
		host := strings.TrimPrefix(sc.Domain, ".")
		byDomain[host] = append(byDomain[host], co)
	}
	for host, cookies := range byDomain {
		c.jar.SetCookies(&url.URL{Scheme: "https", Host: host}, cookies)
	}
	return nil
}

// HasCookie 判断是否携带某 cookie（判断登录态用 SESSDATA）。
func (c *Client) HasCookie(name string) bool {
	u := &url.URL{Scheme: "https", Host: "api.bilibili.com"}
	for _, co := range c.jar.Cookies(u) {
		if co.Name == name {
			return true
		}
	}
	return false
}

// setCookieDirect 直接写入一条 cookie（buvid 注入等）。
func (c *Client) setCookieDirect(host, name, value string) {
	u := &url.URL{Scheme: "https", Host: host}
	c.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value}})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
