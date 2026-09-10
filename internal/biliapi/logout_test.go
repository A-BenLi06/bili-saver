package biliapi

// 登出测试：通知服务端（csrf 随请求）+ 本机 cookie 清空 + 登录态缓存失效。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestLogoutClearsCookies(t *testing.T) {
	var gotPath, gotCSRF string
	passport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotCSRF = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer passport.Close()

	c, err := New("", 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	c.SetBase("http://api.invalid", passport.URL)
	// 注入一对 cookie 模拟登录态（域指向 api.bilibili.com，与 HasCookie 同一读取路径）
	u := &url.URL{Scheme: "https", Host: "api.bilibili.com"}
	c.jar.SetCookies(u, []*http.Cookie{
		{Name: "SESSDATA", Value: "sess"},
		{Name: "bili_jct", Value: "jct-token"},
	})

	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/x/log/web/logout" {
		t.Errorf("应调用新登出端点: %q", gotPath)
	}
	if gotCSRF != "csrf=jct-token" {
		t.Errorf("应携带 csrf: %q", gotCSRF)
	}
	if c.HasCookie("SESSDATA") || c.HasCookie("bili_jct") {
		t.Error("登出后 cookie 应已清空")
	}
	if c.CachedLogin(context.Background()) {
		t.Error("登出后登录态应为 false")
	}
}
