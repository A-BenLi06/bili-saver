package collector

// 真实接口冒烟（验证阶梯第 3 级，只读+临时库）：
// 	BILI_LIVE_MID=<mid> go test -run TestLiveSpaceDynamics -v ./internal/collector/
// 未设置环境变量时跳过；走真实 B 站动态 feed + 临时 SQLite，不影响服务库。

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestLiveSpaceDynamics(t *testing.T) {
	midStr := os.Getenv("BILI_LIVE_MID")
	if midStr == "" {
		t.Skip("未设置 BILI_LIVE_MID，跳过真实接口冒烟")
	}
	mid, err := strconv.ParseInt(midStr, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api, err := biliapi.New(filepath.Join("..", "..", "data", "cookies.json"), 1,
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	col := New(st, api, cfg, nil, nil)
	bvids, err := col.SyncSpaceDynamics(t.Context(), mid)
	if err != nil {
		t.Fatalf("SyncSpaceDynamics: %v", err)
	}
	vis, del, _ := st.CountSpacePosts(mid, "")
	arts, _, _ := st.CountSpacePosts(mid, "article")
	t.Logf("动态落库: 现存=%d 已消失=%d 专栏/图文=%d 视频动态bvid=%d", vis, del, arts, len(bvids))
	if vis == 0 {
		t.Fatal("真实 UP主动态应有落库")
	}
	posts, err := st.ListSpacePosts(mid, "", false, 3, 0)
	if err != nil || len(posts) == 0 {
		t.Fatalf("列表查询失败: %v", err)
	}
	for _, p := range posts {
		t.Logf("  样例: %s type=%s kind=%s bvid=%q title=%q content=%q pics=%d raw_len=%d",
			p.PostID, p.Dtype, p.Kind, p.Bvid, p.Title, trunc(p.Content, 30), p.Pics, len(p.Raw))
		if p.Raw == "" {
			t.Error("原始 JSON 应保留")
		}
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
