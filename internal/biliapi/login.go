package biliapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// QRGenerate 申请扫码登录二维码。
func (c *Client) QRGenerate(ctx context.Context) (*QRGenerate, error) {
	var d QRGenerate
	if err := c.passportGet(ctx, "/x/passport-login/web/qrcode/generate", url.Values{}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// qrRequest 直接对 passport 域发 GET。
func (c *Client) passportGet(ctx context.Context, endpoint string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if err := c.lim.wait(ctx); err != nil {
		return err
	}
	u := c.passportBase + endpoint + "?" + params.Encode()
	body, status, err := c.do(ctx, "GET", u, nil, nil)
	if err != nil {
		return err
	}
	c.emit(endpoint, params, status, body)
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: 非 JSON 响应 (http %d)", endpoint, status)
	}
	if env.Code != 0 && env.Code != 86038 { // 86038 = 二维码过期，也交给调用方判断
		return &APIError{Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s: 解析 data: %w", endpoint, err)
		}
	}
	return nil
}

// QRPoll 轮询扫码结果；扫码成功后 cookie 已写入 jar（响应 Set-Cookie）。
func (c *Client) QRPoll(ctx context.Context, key string) (*QRPoll, error) {
	p := url.Values{"qrcode_key": {key}}
	var d QRPoll
	if err := c.passportGet(ctx, "/x/passport-login/web/qrcode/poll", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// FetchBuvid 获取并注入 buvid3/buvid4（无 cookie 时提升接口可用性）。
func (c *Client) FetchBuvid(ctx context.Context) error {
	p := url.Values{}
	var d struct {
		B3 string `json:"b_3"`
		B4 string `json:"b_4"`
	}
	if err := c.getJSON(ctx, "/x/frontend/finger/spi", p, &d); err != nil {
		return err
	}
	if d.B3 != "" || d.B4 != "" {
		c.setCookieDirect("api.bilibili.com", "buvid3", d.B3)
		c.setCookieDirect("api.bilibili.com", "buvid4", d.B4)
		c.setCookieDirect(".bilibili.com", "buvid3", d.B3)
		c.setCookieDirect(".bilibili.com", "buvid4", d.B4)
	}
	return nil
}

// loginCache 登录态缓存（避免每个 /stats 都打 nav）。
var loginCache struct {
	mu    sync.Mutex
	ok    bool
	at    time.Time
	valid bool
}

// CachedLogin 带缓存的登录态校验（5 分钟）。
// cookie 不存在直接 false；nav 失败时降级为 cookie 存在性。
func (c *Client) CachedLogin(ctx context.Context) bool {
	if !c.HasCookie("SESSDATA") {
		return false
	}
	loginCache.mu.Lock()
	defer loginCache.mu.Unlock()
	if loginCache.valid && time.Since(loginCache.at) < 5*time.Minute {
		return loginCache.ok
	}
	ok, err := c.NavIsLogin(ctx)
	if err != nil {
		return true // 接口失败降级：cookie 存在即认为可能仍有效
	}
	loginCache.ok, loginCache.at, loginCache.valid = ok, time.Now(), true
	return ok
}

// ResetLoginCache 使登录态缓存失效（登录成功后调用）。
func ResetLoginCache() {
	loginCache.mu.Lock()
	loginCache.valid = false
	loginCache.mu.Unlock()
}

// Account 当前登录账号资料（nav 接口；未登录时 IsLogin=false 其余零值）。
type Account struct {
	IsLogin    bool   `json:"is_login"`
	Mid        int64  `json:"mid"`
	Uname      string `json:"uname"`
	Face       string `json:"face"`
	Level      int    `json:"level"`
	VipStatus  int    `json:"vip_status"` // 1=大会员有效
	VipType    int    `json:"vip_type"`   // 0无 1月度 2年度
	Coin       int    `json:"coin"`
	CurrentExp int64  `json:"current_exp"`
}

// NavAccount 拉取当前登录账号资料（nav 无需 wbi 签名，未登录返回 isLogin=false）。
func (c *Client) NavAccount(ctx context.Context) (*Account, error) {
	var nav struct {
		navData
		Uname     string `json:"uname"`
		Face      string `json:"face"`
		LevelInfo struct {
			CurrentLevel int   `json:"current_level"`
			CurrentExp   int64 `json:"current_exp"`
		} `json:"level_info"`
		VipStatus int `json:"vipStatus"`
		VipType   int `json:"vipType"`
		Coin      int `json:"money"`
	}
	if err := c.getJSONLenient(ctx, "/x/web-interface/nav", url.Values{}, &nav); err != nil {
		return nil, err
	}
	return &Account{
		IsLogin: nav.IsLogin, Mid: nav.Mid, Uname: nav.Uname, Face: nav.Face,
		Level: nav.LevelInfo.CurrentLevel, VipStatus: nav.VipStatus, VipType: nav.VipType,
		Coin: nav.Coin, CurrentExp: nav.LevelInfo.CurrentExp,
	}, nil
}

// NavIsLogin 返回当前 cookie 的登录态。
func (c *Client) NavIsLogin(ctx context.Context) (bool, error) {
	var nav navData
	if err := c.getJSON(ctx, "/x/web-interface/nav", url.Values{}, &nav); err != nil {
		return false, err
	}
	return nav.IsLogin, nil
}
