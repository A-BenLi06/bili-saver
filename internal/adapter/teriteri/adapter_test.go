package teriteri

// 适配层契约测试：验证 teriteri-client 期望的响应形状与数据忠诚标记。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func seed(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := store.Now()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Test00001", Aid: 111, Title: "测试视频", Description: "简介",
		Pic: "https://i0.hdslb.com/cover.jpg", OwnerMid: 42, OwnerName: "UP主",
		Pubdate: 1700000000, Duration: 120, PagesCount: 1, Status: "ok",
		FirstArchivedAt: now, LastSyncAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(&store.Page{Bvid: "BV1Test00001", Cid: 222, Page: 1, Part: "P1", Duration: 120, FirstSeenAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendStats(&store.StatsSnapshot{Bvid: "BV1Test00001", Ts: now, View: 100, Danmaku: 2, Like: 30, Favorite: 40, Coin: 5, Share: 6}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*store.Comment{
		{Rpid: 1, Bvid: "BV1Test00001", Content: "现存评论", LikeCount: 10, FirstSeenAt: now, LastSeenAt: now, Status: "visible",
			MemberMid: 1001, MemberName: "评论用户", MemberFace: "https://i0.hdslb.com/face1.jpg"},
		{Rpid: 2, Bvid: "BV1Test00001", Content: "消失的评论", LikeCount: 5, FirstSeenAt: now - 100, LastSeenAt: now - 100, DeletedAt: now, Status: "deleted",
			MemberMid: 1002, MemberName: "消失用户", MemberFace: "https://i0.hdslb.com/face2.jpg"},
		{Rpid: 3, Bvid: "BV1Test00001", Content: "楼中楼回复", FirstSeenAt: now, LastSeenAt: now, Status: "visible",
			ParentRpid: 1, RootRpid: 1, MemberMid: 1003, MemberName: "楼中楼用户", MemberFace: "https://i0.hdslb.com/face3.jpg"},
	} {
		if err := st.UpsertComment(c); err != nil {
			t.Fatal(err)
		}
	}
	// upsert 会把 deleted 恢复 visible，需要手动改回
	if _, err := st.DB.Exec(`UPDATE comments SET status='deleted', deleted_at=? WHERE rpid=2`, now); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*store.Danmaku{
		{Dmid: 1, Bvid: "BV1Test00001", Cid: 222, Timepoint: 5000, Content: "弹幕", FirstSeenAt: now, LastSeenAt: now},
	} {
		if err := st.UpsertDanmaku(d); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.AddTags("BV1Test00001", []string{"搞笑"}, "bilibili")
	_ = st.UpsertAIConclusion("BV1Test00001", "bili-tracer", "官方小结", "[]")
	return st
}

func newAdapterServer(t *testing.T) (*store.Store, *httptest.Server) {
	st := seed(t)
	a := &Adapter{St: st, DataDir: t.TempDir(), AdminUser: "admin", AdminPass: "pw", Log: nil}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return st, srv
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, *envelope) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var e envelope
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return resp, &e
}

func TestVideoGetone(t *testing.T) {
	_, srv := newAdapterServer(t)
	resp, e := get(t, srv.URL+"/api/video/getone?vid=BV1Test00001", nil)
	if resp.StatusCode != 200 || e.Code != 200 {
		t.Fatalf("getone 失败: http=%d code=%d", resp.StatusCode, e.Code)
	}
	var data struct {
		Video struct {
			Vid      string `json:"vid"`
			Title    string `json:"title"`
			VideoUrl string `json:"videoUrl"`
			Duration int    `json:"duration"`
			Tags     string `json:"tags"`
		} `json:"video"`
		Stats map[string]int `json:"stats"`
		User  struct {
			UID      int64  `json:"uid"`
			Nickname string `json:"nickname"`
		} `json:"user"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Video.VideoUrl == "" || !json.Valid([]byte(`"x"`)) {
		t.Fatalf("videoUrl 缺失: %q", data.Video.VideoUrl)
	}
	if len(data.Video.VideoUrl) < 6 || data.Video.VideoUrl[:6] != "/media" {
		t.Fatalf("videoUrl 应指向 /media: %q", data.Video.VideoUrl)
	}
	if data.Video.Duration != 120 || data.Video.Tags != "搞笑" {
		t.Fatalf("视频字段错误: %+v", data.Video)
	}
	if data.User.UID != 42 || data.User.Nickname != "UP主" {
		t.Fatalf("user 字段错误: %+v", data.User)
	}
	if data.Stats["play"] != 100 || data.Stats["comment"] != 3 { // 现存2 + 消失1
		t.Fatalf("stats 错误: %+v", data.Stats)
	}
}

func TestVideoGetone404(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/video/getone?vid=BV1NotFound00", nil)
	if e.Code != 404 {
		t.Fatalf("不存在的视频应返回 code 404: %d", e.Code)
	}
}

func TestDanmuList(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/danmu-list/BV1Test00001", nil)
	var items []struct {
		ID        int64  `json:"id"`
		TimePoint int    `json:"timePoint"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(e.Data, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TimePoint != 5 || items[0].Content != "弹幕" {
		t.Fatalf("弹幕字段错误: %+v", items)
	}
}

func TestCommentGetWithDeletedMarker(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/comment/get?vid=BV1Test00001&offset=0&type=1", nil)
	var data struct {
		More     bool `json:"more"`
		Comments []struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
			Love    int    `json:"love"`
			User    struct {
				UID      int64  `json:"uid"`
				Nickname string `json:"nickname"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Comments) != 2 || data.More {
		t.Fatalf("评论分页错误: more=%v n=%d", data.More, len(data.Comments))
	}
	// 现存评论在前（热度排序），且无标记
	if data.Comments[0].ID != 1 || data.Comments[0].Content != "现存评论" {
		t.Fatalf("现存评论错误: %+v", data.Comments[0])
	}
	// 消失评论带显式标记
	found := false
	for _, c := range data.Comments {
		if c.ID == 2 {
			found = true
			if c.Content == "消失的评论" || c.Content == "" {
				t.Fatalf("消失评论必须带显式标记: %q", c.Content)
			}
		}
	}
	if !found {
		t.Fatal("消失评论应与现存评论共存展示")
	}
}

func TestSearch(t *testing.T) {
	st, srv := newAdapterServer(t)
	v, _ := st.GetVideo("BV1Test00001")
	_ = st.ReindexVideoFTS(v)
	_, e := get(t, srv.URL+"/api/search/video/only-pass?keyword=%E6%B5%8B%E8%AF%95", nil) // “测试”
	var cards []map[string]any
	if err := json.Unmarshal(e.Data, &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("搜索应命中 1 条: %d", len(cards))
	}
}

func TestLoginFlow(t *testing.T) {
	_, srv := newAdapterServer(t)
	// 未登录访问受保护接口 → 401 且带 not login 头
	resp, _ := get(t, srv.URL+"/api/user/personal/info", nil)
	if resp.StatusCode != 401 || resp.Header.Get("Message") != "not login" {
		t.Fatalf("未登录应 401 + Message: not login，得到 %d %q", resp.StatusCode, resp.Header.Get("Message"))
	}
	// 登录拿 token
	req, _ := http.NewRequest("POST", srv.URL+"/api/user/account/login", strings.NewReader("username=admin&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var le envelope
	_ = json.NewDecoder(resp2.Body).Decode(&le)
	if le.Code != 200 {
		t.Fatalf("登录失败: %+v", le)
	}
	var token struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(le.Data, &token)
	if token.Token == "" {
		t.Fatal("未返回 token")
	}
	// 带 token 访问
	resp3, e3 := get(t, srv.URL+"/api/user/personal/info", map[string]string{"Authorization": "Bearer " + token.Token})
	if resp3.StatusCode != 200 || e3.Code != 200 {
		t.Fatalf("带 token 访问失败: %d", resp3.StatusCode)
	}
	// 错误密码
	req4, _ := http.NewRequest("POST", srv.URL+"/api/user/account/login", strings.NewReader("username=admin&password=wrong"))
	req4.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp4, _ := http.DefaultClient.Do(req4)
	var le4 envelope
	_ = json.NewDecoder(resp4.Body).Decode(&le4)
	if le4.Code == 200 {
		t.Fatal("错误密码不应登录成功")
	}
}

// 回归：真实 teriteri 前端发 JSON（axios.post），登录必须能解析（表单之外）。
func TestLoginJSON(t *testing.T) {
	_, srv := newAdapterServer(t)
	b, _ := json.Marshal(map[string]string{"username": "admin", "password": "pw"})
	resp, err := http.Post(srv.URL+"/api/user/account/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var le envelope
	_ = json.NewDecoder(resp.Body).Decode(&le)
	if le.Code != 200 {
		t.Fatalf("JSON 登录应成功: %+v", le)
	}
	// 错误密码的 JSON 同样要被解析（报"密码错误"而非"不能为空"）
	b2, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	resp2, _ := http.Post(srv.URL+"/api/user/account/login", "application/json", bytes.NewReader(b2))
	var le2 envelope
	_ = json.NewDecoder(resp2.Body).Decode(&le2)
	if le2.Code == 200 || !strings.Contains(le2.Message, "错误") {
		t.Fatalf("JSON 错误密码应报账号或密码错误: %+v", le2)
	}
}
