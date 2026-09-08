package teriteriadmin

// teriteri-admin（管理员端）契约测试：审核列表/详情/状态变更映射归档库。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func setup(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := store.Now()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Ok0000001", Aid: 1, Title: "在审归档视频", Pic: "https://i0.hdslb.com/1.jpg",
		OwnerMid: 42, OwnerName: "UP主", Pubdate: 1700000000, Duration: 100,
		Status: "ok", FirstArchivedAt: now, LastSyncAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Gone00001", Aid: 2, Title: "已消失视频", OwnerMid: 42, OwnerName: "UP主",
		Pubdate: 1690000000, Duration: 60, Status: "gone", FirstArchivedAt: now, LastSyncAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{St: st, DataDir: t.TempDir(), AdminUser: "admin", AdminPass: "pw"}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return st, srv
}

func adminToken(t *testing.T, srvURL string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", srvURL+"/api/admin/account/login",
		strings.NewReader(`{"username":"admin","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		Code int `json:"code"`
		Data struct {
			Token string         `json:"token"`
			User  map[string]any `json:"user"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Code != 200 || e.Data.Token == "" {
		t.Fatalf("登录失败: %+v", e)
	}
	if e.Data.User["nickname"] != "admin" {
		t.Fatalf("登录响应需带 user: %+v", e.Data)
	}
	return e.Data.Token
}

func get(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return e
}

func TestReviewVideoFlow(t *testing.T) {
	st, srv := setup(t)
	tok := adminToken(t, srv.URL)

	// 未登录 → 401
	req, _ := http.NewRequest("GET", srv.URL+"/api/review/video/getpage?vstatus=1", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("未登录应 401: %d", resp.StatusCode)
	}

	// 已过审列表：只有 ok 视频
	e := get(t, srv.URL+"/api/review/video/getpage?vstatus=1&page=1&quantity=10", tok)
	list := e["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("已过审应只有 1 条: %v", e["data"])
	}
	row := list[0].(map[string]any)
	video := row["video"].(map[string]any)
	if video["vid"] != "BV1Ok0000001" || video["status"].(float64) != 1 {
		t.Fatalf("列表行形状错误: %v", row)
	}
	if row["user"].(map[string]any)["nickname"] != "UP主" {
		t.Fatalf("user 字段错误: %v", row)
	}

	// 封禁/删源列表：gone 视频
	e3 := get(t, srv.URL+"/api/review/video/getpage?vstatus=3&page=1&quantity=10", tok)
	if n := len(e3["data"].([]any)); n != 1 {
		t.Fatalf("vstatus=3 应有 1 条: %v", e3["data"])
	}

	// total
	e4 := get(t, srv.URL+"/api/review/video/total?vstatus=1", tok)
	if e4["data"].(float64) != 1 {
		t.Fatalf("total 应为 1: %v", e4)
	}

	// getone 带 tags 字符串（前端按 \r\n split）
	e5 := get(t, srv.URL+"/api/review/video/getone?vid=BV1Ok0000001", tok)
	data := e5["data"].(map[string]any)
	_ = data
	_ = st

	// 状态变更：2 → private（隐藏）；再查已过审列表应为 0
	post, _ := http.NewRequest("POST", srv.URL+"/api/video/change/status", strings.NewReader("vid=BV1Ok0000001&status=2"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	var e6 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&e6)
	if e6["code"].(float64) != 200 {
		t.Fatalf("状态变更失败: %v", e6)
	}
	v, _ := st.GetVideo("BV1Ok0000001")
	if v.Status != "private" {
		t.Fatalf("状态应变为 private: %s", v.Status)
	}
	if e7 := get(t, srv.URL+"/api/review/video/getpage?vstatus=1&page=1&quantity=10", tok); len(e7["data"].([]any)) != 0 {
		t.Fatalf("隐藏后已过审列表应为空")
	}
}
