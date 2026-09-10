package biliapi

// NavAccount 解析测试：未登录 code=0 也要正常返回资料结构。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNavAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{
			"isLogin":true,"mid":12345,"uname":"测试UP",
			"face":"https://i0.hdslb.com/bfs/face/abc.jpg",
			"level_info":{"current_level":6,"current_exp":28800},
			"vipStatus":1,"vipType":2,"money":50.0}}`))
	}))
	defer srv.Close()
	c, err := New("", 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	c.SetBase(srv.URL, srv.URL)

	acc, err := c.NavAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !acc.IsLogin || acc.Mid != 12345 || acc.Uname != "测试UP" {
		t.Fatalf("基本资料解析错误: %+v", acc)
	}
	if acc.Level != 6 || acc.CurrentExp != 28800 {
		t.Errorf("等级/经验解析错误: %+v", acc)
	}
	if acc.VipStatus != 1 || acc.VipType != 2 || acc.Coin != 50 {
		t.Errorf("大会员/硬币解析错误: %+v", acc)
	}
}
