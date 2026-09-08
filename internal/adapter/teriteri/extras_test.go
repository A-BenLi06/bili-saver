package teriteri

// extras 端点契约测试：前端启动链路依赖的端点必须永远 code=200 且返回合法集合。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func loginToken(t *testing.T, srv string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", srv+"/api/user/account/login", strings.NewReader("username=admin&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var le envelope
	_ = json.NewDecoder(resp.Body).Decode(&le)
	resp.Body.Close()
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(le.Data, &tok)
	if tok.Token == "" {
		t.Fatal("登录未返回 token")
	}
	return tok.Token
}

// TestStartupEndpointsNeverFail App.vue 创建链路逐个调用的端点：任何 404/500
// 都会导致前端弹错误 toast 并中断后续初始化（频道栏、热搜）。
func TestStartupEndpointsNeverFail(t *testing.T) {
	_, srv := newAdapterServer(t)
	urls := []string{
		"/api/category/getall",
		"/api/search/hot/get",
		"/api/search/count?keyword=test",
		"/api/search/count",
		"/api/favorite/get-all/visitor?uid=42",
		"/api/user/info/get-one?uid=42",
		"/api/video/user-works?uid=42&page=1&quantity=30",
		"/api/video/user-works-count?uid=42",
	}
	for _, u := range urls {
		resp, e := get(t, srv.URL+u, nil)
		if resp.StatusCode != 200 || e.Code != 200 {
			t.Fatalf("%s 应始终 200: http=%d code=%d", u, resp.StatusCode, e.Code)
		}
		if !json.Valid(e.Data) || strings.HasPrefix(string(e.Data), "null") {
			t.Fatalf("%s 的 data 不能是 null（前端对 null 取属性会崩渲染树）: %s", u, e.Data)
		}
	}
	// 访客收藏夹不能是空数组：前端“默认夹置顶”会得到 [undefined] 并崩掉空间主页
	_, e := get(t, srv.URL+"/api/favorite/get-all/visitor?uid=42", nil)
	var favs []map[string]any
	if err := json.Unmarshal(e.Data, &favs); err != nil || len(favs) != 1 {
		t.Fatalf("访客收藏夹应含默认夹: %s", e.Data)
	}
	if favs[0]["type"].(float64) != 1 || favs[0]["visible"].(float64) != 1 {
		t.Fatalf("默认夹需 type=1 且 visible=1: %s", e.Data)
	}
}

func TestCategoryGetallEmptyArray(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/category/getall", nil)
	var arr []any
	if err := json.Unmarshal(e.Data, &arr); err != nil || arr == nil {
		t.Fatalf("category/getall 应返回 []: %s %v", e.Data, err)
	}
}

func TestSearchHotFromTags(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/search/hot/get", nil)
	var items []struct {
		Content string `json:"content"`
		Type    int    `json:"type"`
	}
	if err := json.Unmarshal(e.Data, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "搞笑" {
		t.Fatalf("热搜应含归档标签: %+v", items)
	}
}

// TestCommentShapeContract teriteri-client 渲染评论依赖的确切字段名与类型：
// user.avatar_url（非 avatar）、数字型 vip/auth、parentId+toUser（缺失会让
// 前端渲染 effect 抛错、评论区永久冻结在“正在加载…”）。
func TestCommentShapeContract(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/comment/get?vid=BV1Test00001&offset=0&type=1", nil)
	var data struct {
		Comments []struct {
			ID       int64           `json:"id"`
			RootID   int64           `json:"rootId"`
			ParentID int64           `json:"parentId"`
			ToUser   *map[string]any `json:"toUser"`
			User     struct {
				UID       int64  `json:"uid"`
				Nickname  string `json:"nickname"`
				AvatarURL string `json:"avatar_url"`
				Vip       int    `json:"vip"`
				Auth      int    `json:"auth"`
				Exp       int    `json:"exp"`
			} `json:"user"`
			Replies []struct {
				RootID   int64           `json:"rootId"`
				ParentID int64           `json:"parentId"`
				ToUser   *map[string]any `json:"toUser"`
				User     struct {
					AvatarURL string `json:"avatar_url"`
				} `json:"user"`
			} `json:"replies"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Comments) != 2 {
		t.Fatalf("应有 2 条主楼: %d", len(data.Comments))
	}
	for _, c := range data.Comments {
		if c.ToUser == nil {
			t.Fatalf("评论 %d 缺少 toUser 对象", c.ID)
		}
		if c.User.AvatarURL == "" || c.User.Vip != 0 || c.User.Auth != 0 {
			t.Fatalf("评论 %d user 字段形状错误: %+v", c.ID, c.User)
		}
		for _, r := range c.Replies {
			if r.ParentID != r.RootID {
				t.Fatalf("楼中楼 parentId 必须等于 rootId（否则前端读 toUser.uid 崩渲染）: %+v", r)
			}
			if r.ToUser == nil || r.User.AvatarURL == "" {
				t.Fatalf("楼中楼缺少 toUser 或 avatar_url: %+v", r)
			}
		}
	}
}

func TestSearchCountShape(t *testing.T) {
	st, srv := newAdapterServer(t)
	v, _ := st.GetVideo("BV1Test00001")
	_ = st.ReindexVideoFTS(v)
	_, e := get(t, srv.URL+"/api/search/count?keyword=%E6%B5%8B%E8%AF%95", nil)
	var arr []int
	if err := json.Unmarshal(e.Data, &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 2 || arr[0] != 1 || arr[1] != 0 {
		t.Fatalf("search/count 应为 [视频数,0]: %v", arr)
	}
}

func TestAuthedExtras(t *testing.T) {
	_, srv := newAdapterServer(t)
	tok := loginToken(t, srv.URL)
	h := map[string]string{"Authorization": "Bearer " + tok}

	// 未登录 → 401 + not login（前端按未登录处理，不算故障）
	resp, _ := get(t, srv.URL+"/api/favorite/get-all/user", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("收藏夹未登录应 401: %d", resp.StatusCode)
	}

	_, e := get(t, srv.URL+"/api/favorite/get-all/user", h)
	var favs []map[string]any
	if err := json.Unmarshal(e.Data, &favs); err != nil || len(favs) != 1 || favs[0]["type"].(float64) != 1 {
		t.Fatalf("默认收藏夹形状错误: %s", e.Data)
	}

	_, e2 := get(t, srv.URL+"/api/comment/get-like-and-dislike", h)
	var ld map[string]json.RawMessage
	if err := json.Unmarshal(e2.Data, &ld); err != nil {
		t.Fatal(err)
	}
	if ld["userLike"] == nil || ld["userDislike"] == nil {
		t.Fatalf("赞踩集合缺失: %s", e2.Data)
	}

	for _, u := range []string{
		"/api/msg/chat/recent-list",
		"/api/msg/chat-detailed/get-more",
		"/api/video/user-love",
		"/api/video/user-collect",
	} {
		if r, ee := get(t, srv.URL+u, h); r.StatusCode != 200 || ee.Code != 200 {
			t.Fatalf("%s 登录态应 200", u)
		}
	}
}

func TestUserSpace(t *testing.T) {
	_, srv := newAdapterServer(t)
	_, e := get(t, srv.URL+"/api/user/info/get-one?uid=42", nil)
	var u map[string]any
	if err := json.Unmarshal(e.Data, &u); err != nil {
		t.Fatal(err)
	}
	if u["nickname"] != "UP主" || u["state"].(float64) != 0 || u["worksCount"].(float64) != 1 {
		t.Fatalf("UP主资料错误: %s", e.Data)
	}

	_, e2 := get(t, srv.URL+"/api/video/user-works?uid=42&page=1&quantity=30", nil)
	var works struct {
		Count int `json:"count"`
		List  []struct {
			Video struct {
				Vid   string `json:"vid"`
				Title string `json:"title"`
			} `json:"video"`
		} `json:"list"`
	}
	if err := json.Unmarshal(e2.Data, &works); err != nil {
		t.Fatal(err)
	}
	if works.Count != 1 || len(works.List) != 1 || works.List[0].Video.Vid != "BV1Test00001" {
		t.Fatalf("投稿列表错误: %+v", works)
	}

	_, e3 := get(t, srv.URL+"/api/video/user-works-count?uid=42", nil)
	var n float64
	if err := json.Unmarshal(e3.Data, &n); err != nil || n != 1 {
		t.Fatalf("投稿数应为 1: %s", e3.Data)
	}

	// 不存在的用户 → 404（前端跳 /notfound）
	_, e4 := get(t, srv.URL+"/api/user/info/get-one?uid=999", nil)
	if e4.Code != 404 {
		t.Fatalf("未知用户应 404: %d", e4.Code)
	}
	// 站长（uid=1）即使无投稿也能呈现
	_, e5 := get(t, srv.URL+"/api/user/info/get-one?uid=1", nil)
	if e5.Code != 200 {
		t.Fatalf("站长空间应可访问: %d", e5.Code)
	}
}

// TestUserWorksRuleOrdering rule 契约以 SpaceVideo.vue 为准：
// 1=最新发布、2=最多播放、3=最多点赞（此前 1/2 写反，被用户发现）。
func TestUserWorksRuleOrdering(t *testing.T) {
	st, srv := newAdapterServer(t)
	now := store.Now()
	// 追加两条：一条更晚发布但播放低，一条更早发布但播放/点赞最高
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Test00002", Aid: 112, Title: "高播放老视频", OwnerMid: 42, OwnerName: "UP主",
		Pubdate: 1690000000, Duration: 60, Status: "ok", FirstArchivedAt: now, LastSyncAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Test00003", Aid: 113, Title: "最新低播放", OwnerMid: 42, OwnerName: "UP主",
		Pubdate: 1710000000, Duration: 60, Status: "ok", FirstArchivedAt: now, LastSyncAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendStats(&store.StatsSnapshot{Bvid: "BV1Test00002", Ts: now, View: 9999, Like: 500}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendStats(&store.StatsSnapshot{Bvid: "BV1Test00001", Ts: now, View: 100, Like: 30}); err != nil {
		t.Fatal(err)
	}
	order := func(rule string) []string {
		t.Helper()
		_, e := get(t, srv.URL+"/api/video/user-works?uid=42&rule="+rule+"&page=1&quantity=30", nil)
		var data struct {
			List []struct {
				Video struct {
					Vid string `json:"vid"`
				} `json:"video"`
			} `json:"list"`
			Count int `json:"count"`
		}
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Count != 3 || len(data.List) != 3 {
			t.Fatalf("rule=%s 应返回 3 条: count=%d", rule, data.Count)
		}
		out := make([]string, 0, 3)
		for _, it := range data.List {
			out = append(out, it.Video.Vid)
		}
		return out
	}
	// 最新发布：Test00003(1710000000) > Test00001(1700000000) > Test00002(1690000000)
	if got := order("1"); got[0] != "BV1Test00003" || got[2] != "BV1Test00002" {
		t.Fatalf("rule=1 应按发布时间倒序: %v", got)
	}
	// 最多播放：Test00002(9999) 第一
	if got := order("2"); got[0] != "BV1Test00002" {
		t.Fatalf("rule=2 应播放最高在前: %v", got)
	}
	// 最多点赞：Test00002(500) 第一，Test00001(30) 第二
	if got := order("3"); got[0] != "BV1Test00002" || got[1] != "BV1Test00001" {
		t.Fatalf("rule=3 应点赞最高在前: %v", got)
	}
}

func TestIMWebsocketHandshake(t *testing.T) {
	_, srv := newAdapterServer(t)
	// RFC 6455 示例 key，期望的 accept 值固定
	req, _ := http.NewRequest("GET", srv.URL+"/im", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 101 {
		t.Fatalf("/im 应完成 WS 握手: %d", resp.StatusCode)
	}
	if want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; resp.Header.Get("Sec-WebSocket-Accept") != want {
		t.Fatalf("accept 错误: %q", resp.Header.Get("Sec-WebSocket-Accept"))
	}
}

// TestLoginResponseIncludesUser LoginRegister.vue 登录成功后立即
// updateUser(result.data.data.user)，缺失 user 会让登录链路在
// isLogin=true 之前抛错中断（表现为“登录不上/状态不保存”）。
func TestLoginResponseIncludesUser(t *testing.T) {
	_, srv := newAdapterServer(t)
	req, _ := http.NewRequest("POST", srv.URL+"/api/user/account/login",
		strings.NewReader(`{"username":"admin","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var le envelope
	_ = json.NewDecoder(resp.Body).Decode(&le)
	var data struct {
		Token string         `json:"token"`
		User  map[string]any `json:"user"`
	}
	if err := json.Unmarshal(le.Data, &data); err != nil {
		t.Fatal(err)
	}
	if le.Code != 200 || data.Token == "" {
		t.Fatalf("JSON 登录应成功: %+v", le)
	}
	if data.User == nil || data.User["nickname"] != "admin" {
		t.Fatalf("登录响应必须带 user 对象: %s", le.Data)
	}
}

// TestSearchDoubleEncodedKeyword 前端搜索会对关键词 encodeURIComponent 一次、
// axios 参数序列化再编码一次；二次编码的关键词必须能命中。
func TestSearchDoubleEncodedKeyword(t *testing.T) {
	st, srv := newAdapterServer(t)
	v, _ := st.GetVideo("BV1Test00001")
	_ = st.ReindexVideoFTS(v)
	// “测试” → encodeURIComponent → %E6%B5%8B%E8%AF%95 → axios 再编码 → %25E6...
	_, e := get(t, srv.URL+"/api/search/video/only-pass?keyword=%25E6%25B5%258B%25E8%25AF%2595", nil)
	var cards []map[string]any
	if err := json.Unmarshal(e.Data, &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("双重编码关键词应命中 1 条: %d", len(cards))
	}
	// 单层编码（手动直调）也不受影响
	_, e2 := get(t, srv.URL+"/api/search/video/only-pass?keyword=%E6%B5%8B%E8%AF%95", nil)
	if err := json.Unmarshal(e2.Data, &cards); err != nil || len(cards) != 1 {
		t.Fatalf("单层编码关键词应命中 1 条: %v", cards)
	}
}

// TestSearchByTagFallback FTS 只索引标题/简介，热搜词全部来自标签：
// 标签命不中标题时必须回退标签匹配。
func TestSearchByTagFallback(t *testing.T) {
	_, srv := newAdapterServer(t)
	// 种子里标题是“测试视频”，标签是“搞笑”（标题中不出现）
	_, e := get(t, srv.URL+"/api/search/video/only-pass?keyword=%E6%90%9E%E7%AC%91", nil) // “搞笑”
	var cards []map[string]any
	if err := json.Unmarshal(e.Data, &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("标签搜索应命中 1 条: %d", len(cards))
	}
	_, e2 := get(t, srv.URL+"/api/search/count?keyword=%E6%90%9E%E7%AC%91", nil)
	var arr []int
	if err := json.Unmarshal(e2.Data, &arr); err != nil || len(arr) != 2 || arr[0] != 1 {
		t.Fatalf("标签搜索计数应命中: %s", e2.Data)
	}
}

// TestSiteInteractionFlow 站内互动层：点赞视频/收藏/发评论 → 列表与计数合并展示。
func TestSiteInteractionFlow(t *testing.T) {
	st, srv := newAdapterServer(t)
	tok := loginToken(t, srv.URL)
	h := map[string]string{"Authorization": "Bearer " + tok}

	post := func(url, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var e map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return e
	}

	// 点赞视频
	if e := post("/api/video/love-or-not", "vid=BV1Test00001&isLove=true&isSet=true"); e["code"].(float64) != 200 {
		t.Fatalf("点赞失败: %v", e)
	}
	// 收藏到默认夹
	if e := post("/api/video/collect", "vid=BV1Test00001&adds=0&removes="); e["code"].(float64) != 200 {
		t.Fatalf("收藏失败: %v", e)
	}
	// 发表主楼评论
	e := post("/api/comment/add", "vid=BV1Test00001&root_id=0&parent_id=0&to_user_id=0&content=站内测试评论")
	if e["code"].(float64) != 200 {
		t.Fatalf("发评论失败: %v", e)
	}

	// user-love 含该视频
	resp, e2 := get(t, srv.URL+"/api/video/user-love?uid=1", h)
	if resp.StatusCode != 200 {
		t.Fatalf("user-love 应 200")
	}
	var loved []map[string]any
	_ = json.Unmarshal(e2.Data, &loved)
	if len(loved) != 1 || loved[0]["video"].(map[string]any)["vid"] != "BV1Test00001" {
		t.Fatalf("点赞列表错误: %v", loved)
	}

	// 收藏夹计数（默认夹 1 条）
	_, e3 := get(t, srv.URL+"/api/favorite/get-all/user", h)
	var favs []map[string]any
	_ = json.Unmarshal(e3.Data, &favs)
	found := false
	for _, f := range favs {
		if f["type"].(float64) == 1 && f["count"].(float64) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("默认收藏夹计数应为 1: %s", e3.Data)
	}

	// 评论列表：站内评论出现（最热排序追加在后），点赞可叠加
	_, e4 := get(t, srv.URL+"/api/comment/get?vid=BV1Test00001&offset=0&type=1", nil)
	var data struct {
		Comments []struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
			User    struct {
				Nickname string `json:"nickname"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(e4.Data, &data); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, c := range data.Comments {
		if c.Content == "站内测试评论" && c.User.Nickname == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("站内评论应出现在列表中: %s", e4.Data)
	}

	// getone 点赞计数叠加（B站快照 30 + 站内 1）
	_, e5 := get(t, srv.URL+"/api/video/getone?vid=BV1Test00001", nil)
	var d5 struct {
		Stats map[string]int `json:"stats"`
	}
	_ = json.Unmarshal(e5.Data, &d5)
	if d5.Stats["good"] != 31 {
		t.Fatalf("点赞数应叠加站内 1 赞（30+1）: %v", d5.Stats)
	}
	_ = st
}
