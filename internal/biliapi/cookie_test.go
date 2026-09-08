package biliapi

// 回归测试：cookie jar 必须挂载到 http.Client（否则登录态/风控 cookie 全部失效），
// 且 Save/Load 往返后 cookie 仍会被发送（Domain 属性保留）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCookieJarAttachedAndRoundTrip(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "abc123", Path: "/"})
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			gotCookie = r.Header.Get("Cookie")
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	c, err := New("", 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	c.SetBase(srv.URL, srv.URL)
	c.cookieHosts = []string{"127.0.0.1"} // 测试域与生产域等价的导出范围

	// 模拟扫码成功：响应 Set-Cookie 应进入 jar，同客户端后续请求带上它
	if err := c.getJSON(ctx, "/set", nil, nil); err != nil {
		t.Fatal(err)
	}
	gotCookie = ""
	if err := c.getJSON(ctx, "/check", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotCookie, "SESSDATA=abc123") {
		t.Fatal("jar 未挂载到 http.Client：Set-Cookie 没有进入 jar（登录态丢失的根因）")
	}

	// SaveCookies 落盘应包含 SESSDATA 及其 Domain
	file := filepath.Join(t.TempDir(), "cookies.json")
	if err := c.SaveCookies(file); err != nil {
		t.Fatal(err)
	}
	b := readFile(t, file)
	if !strings.Contains(b, "SESSDATA") {
		t.Fatalf("cookie 文件缺少 SESSDATA: %s", b)
	}

	// 新客户端加载文件后请求应带上 cookie
	c2, err := New(file, 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	c2.SetBase(srv.URL, srv.URL)
	gotCookie = ""
	if err := c2.getJSON(ctx, "/check", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotCookie, "SESSDATA=abc123") {
		t.Fatalf("加载后 cookie 未被发送: %q", gotCookie)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := osReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
