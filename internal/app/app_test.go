package app

// 装配级冒烟测试：临时数据目录起一个完整 App（mock 掉 B 站不需要——
// 这里只测本地 API：stats/videos/teriteri 适配/media 流）。

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Listen = ":0"
	cfg.Bilibili.MinReqMs = 1
	cfg.Bilibili.CookieFile = filepath.Join(cfg.DataDir, "cookies.json")
	cfg.Normalize()
	a, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.St.Close() })
	return a
}

func TestHTTPWiring(t *testing.T) {
	a := newTestApp(t)
	// 预置一条视频与文件
	now := store.Now()
	if err := a.St.UpsertVideo(&store.Video{
		Bvid: "BV1Test00001", Aid: 111, Title: "装配测试", OwnerMid: 1, OwnerName: "up",
		Pubdate: now, Status: "ok", FirstArchivedAt: now, Downloaded: true,
		LocalPath: filepath.Join("media", "BV1Test00001", "p1", "v.mp4"),
	}); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(a.Cfg.DataDir, "media", "BV1Test00001", "p1", "v.mp4")
	_ = os.MkdirAll(filepath.Dir(mp4), 0o755)
	_ = os.WriteFile(mp4, []byte("0123456789abcdef"), 0o644)

	srv := httptest.NewServer(a.HTTPHandler())
	t.Cleanup(srv.Close)

	// v11 起 /api/v1 需登录：种子管理员登录拿会话
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if lr, lerr := client.Post(srv.URL+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"bili-saver"}`)); lerr != nil || lr.StatusCode != 200 {
		t.Fatalf("种子管理员登录失败: %v %v", lerr, lr)
	}

	// stats
	resp, err := client.Get(srv.URL + "/api/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	var stats map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	if stats["archive"] == nil {
		t.Fatal("stats 无数据")
	}

	// videos 列表（分页富结果：{total,counts,items}）
	resp2, _ := client.Get(srv.URL + "/api/v1/videos")
	var vl struct {
		Total  int64            `json:"total"`
		Counts map[string]int64 `json:"counts"`
		Items  []*store.Video   `json:"items"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&vl)
	if vl.Total != 1 || len(vl.Items) != 1 || vl.Items[0].Bvid != "BV1Test00001" {
		t.Fatalf("videos 列表错误: %+v", vl)
	}

	// teriteri 适配层共存于同一 mux
	resp3, _ := client.Get(srv.URL + "/api/video/getone?vid=BV1Test00001")
	var env struct {
		Code int `json:"code"`
	}
	_ = json.NewDecoder(resp3.Body).Decode(&env)
	if env.Code != 200 {
		t.Fatalf("teriteri getone 失败: %d", env.Code)
	}

	// media 流式播放 + Range
	req, _ := http.NewRequest("GET", srv.URL+"/media/media/BV1Test00001/p1/v.mp4", nil)
	req.Header.Set("Range", "bytes=0-3")
	resp4, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp4.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range 请求应 206: %d", resp4.StatusCode)
	}
	buf := make([]byte, 8)
	n, _ := resp4.Body.Read(buf)
	if string(buf[:n]) != "0123" {
		t.Fatalf("Range 内容错误: %q", string(buf[:n]))
	}

	// 越界路径禁止（HTTP 层会规范化为 404，处理器内为 403，均不得 200）
	resp5, _ := http.Get(srv.URL + "/media/../bili-saver.db")
	if resp5.StatusCode == http.StatusOK {
		t.Fatalf("路径穿越不得返回内容: %d", resp5.StatusCode)
	}
	resp5b, _ := http.Get(srv.URL + "/media/%2e%2e%2fbili-saver.db")
	if resp5b.StatusCode == http.StatusOK {
		t.Fatalf("编码路径穿越不得返回内容: %d", resp5b.StatusCode)
	}

	// admin UI 首页
	resp6, _ := http.Get(srv.URL + "/")
	if resp6.StatusCode != 200 {
		t.Fatalf("首页应 200: %d", resp6.StatusCode)
	}
}

func TestJobRunArchiveLocalOnly(t *testing.T) {
	// 队列去重与状态机（不触发网络）
	a := newTestApp(t)
	// 先提交后启动：避免 worker 抢先完成导致去重断言竞态
	id1, err := a.Mgr.Submit("download", map[string]any{"bvid": "BV1None"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := a.Mgr.Submit("download", map[string]any{"bvid": "BV1None"})
	if err != nil {
		t.Fatal(err)
	}
	a.Mgr.Start(t.Context())
	if id1 == 0 || id2 != 0 {
		t.Fatalf("去重失败: %d %d", id1, id2)
	}
	// 等 worker 处理（BV1None 不在库中 → failed）
	deadline := 50
	for i := 0; i < deadline; i++ {
		j, err := a.St.GetJob(id1)
		if err == nil && (j.State == "done" || j.State == "failed") {
			if j.State != "failed" {
				t.Fatalf("不存在的视频应 failed: %+v", j)
			}
			return
		}
		sleep()
	}
	t.Fatal("任务未被处理")
}

func sleep()             { time.Sleep(50 * time.Millisecond) }
func timeSleepMs(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func TestDueParts(t *testing.T) {
	a := newTestApp(t)
	a.Cfg.Collector.AdaptiveSync = false // 本用例锁定固定间隔语义；自适应行为见 adaptive_test.go
	now := store.Now()
	src := &store.Source{
		SyncIntervalMinutes: 60, // 主间隔 1 小时
		IntervalCommentsMin: 10, // 评论每 10 分钟
		IntervalDanmakuMin:  -1, // 弹幕从不
		IntervalSubsMin:     0,  // 字幕跟随主间隔
	}
	v := &store.Video{
		LastStatsAt:    0,           // 从未统计 → 到期
		LastCommentsAt: now - 300,   // 5 分钟前 → 10 分钟间隔未到
		LastDanmakuAt:  now - 99999, // 很久以前 → -1 永不到期
		LastSubsAt:     now - 300,   // 5 分钟前 → 60 分钟间隔未到
	}
	stats, comments, danmaku, subs := a.dueParts(src, v, now)
	if !stats {
		t.Error("stats 应到期")
	}
	if comments {
		t.Error("comments 5min<10min 不应到期")
	}
	if danmaku {
		t.Error("danmaku=-1 永不到期")
	}
	if subs {
		t.Error("subs 5min<60min 不应到期")
	}
	// 时间推后到全部到期
	stats, comments, danmaku, subs = a.dueParts(src, v, now+61*60)
	if !stats || !comments || danmaku || !subs {
		t.Errorf("一小时后 stats/comments/subs 应到期且 danmaku 永不: %v %v %v %v", stats, comments, danmaku, subs)
	}
}
