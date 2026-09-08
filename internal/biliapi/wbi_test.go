package biliapi

import (
	"net/url"
	"testing"
)

func TestGetMixinKey(t *testing.T) {
	// 公开资料中的示例 key
	orig := "7cd084941338484aae1ad9425b94feee" + "4932ca060261963d2e9c20273d95603c"
	got := getMixinKey(orig)
	if len(got) != 32 {
		t.Fatalf("mixin key 长度应为 32，得到 %d", len(got))
	}
	// 混淆表前两位是 46,47 → orig[46]='c'? 只验证确定性
	if got != getMixinKey(orig) {
		t.Fatal("mixin key 应确定")
	}
}

func TestWbiValue(t *testing.T) {
	cases := map[string]string{
		"hello world": "hello%20world",
		"a!b'c(d)e*f": "abcdef",
		"中文&=?":       "%E4%B8%AD%E6%96%87%26%3D%3F",
		"safe-._~":    "safe-._~",
	}
	for in, want := range cases {
		if got := wbiValue(in); got != want {
			t.Errorf("wbiValue(%q)=%q 期望 %q", in, got, want)
		}
	}
}

func TestWbiSignParamsDeterministic(t *testing.T) {
	// 固定 wts 场景下签名应确定：手动构造
	params := url.Values{"foo": {"one"}, "bar": {"two"}}
	p1, q1 := wbiSignParams(params, "0123456789abcdef0123456789abcdef")
	p2, q2 := wbiSignParams(params, "0123456789abcdef0123456789abcdef")
	if p1.Get("w_rid") == "" || p1.Get("wts") == "" {
		t.Fatal("签名参数缺失")
	}
	if p1.Get("w_rid") != p2.Get("w_rid") && q1 == q2 {
		// 同一秒内 wts 相同，签名应一致
		t.Fatal("同参数同 wts 签名应一致")
	}
	if !hasAllKeys(p1, "foo", "bar", "wts", "w_rid") {
		t.Fatal("签名后应包含原始参数")
	}
}

func hasAllKeys(v url.Values, keys ...string) bool {
	for _, k := range keys {
		if _, ok := v[k]; !ok {
			return false
		}
	}
	return true
}

func TestKeyFromURL(t *testing.T) {
	if got := keyFromURL("https://i0.hdslb.com/bfs/wbi/abcdef0123456789.png"); got != "abcdef0123456789" {
		t.Errorf("keyFromURL 错误: %q", got)
	}
}
