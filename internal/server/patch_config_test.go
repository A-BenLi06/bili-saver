package server

// 设置页 PATCH /api/v1/config 的端到端测试（httptest，不经网络）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestPatchConfig(t *testing.T) {
	cfg := config.Defaults()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SaveConfigFile(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	// GET config 会调 CachedLogin，需要真实 client（无 cookie 文件，离线返回未登录）
	api, err := biliapi.New(filepath.Join(t.TempDir(), "cookies.json"), 1, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Deps{Cfg: cfg, API: api, St: st, ConfigPath: cfgPath})
	h := s.Handler()

	// v11 起 /api/v1 需登录：先用种子管理员换会话 cookie
	lreq := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"bili-saver"}`))
	lreq.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, lreq)
	if lw.Code != 200 {
		t.Fatalf("种子管理员登录应 200: %d", lw.Code)
	}
	sess := ""
	for _, ck := range lw.Result().Cookies() {
		if ck.Name == "bs_sess" {
			sess = ck.Value
		}
	}
	if sess == "" {
		t.Fatal("登录应签发会话 cookie")
	}

	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "bs_sess", Value: sess})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "bs_sess", Value: sess})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// 合法变更：多种类型字段一起提交
	w := do(`{"sync_default_minutes":120,"adaptive_sync":false,"auto_download":true,
		"qualities":["1080p","720p"],"media_layout":"show","concurrency":3,
		"frontend_url":"http://x.example"}`)
	if w.Code != 200 {
		t.Fatalf("合法 PATCH 应 200: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["sync_default_minutes"].(float64) != 120 || resp["adaptive_sync"] != false ||
		resp["media_layout"] != "show" || resp["concurrency"].(float64) != 3 {
		t.Errorf("PATCH 响应未反映变更: %v", resp)
	}

	// 内存已生效（GET 一致）
	rec2 := get("/api/v1/config")
	var got map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &got)
	if got["sync_default_minutes"].(float64) != 120 || got["stats_resync_hours"].(float64) != 24 {
		t.Errorf("GET 未反映 PATCH 结果: sync=%v stats=%v", got["sync_default_minutes"], got["stats_resync_hours"])
	}

	// 写盘生效：从磁盘重新 Load 应读到变更
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Collector.SyncDefaultMinutes != 120 || reloaded.Collector.AdaptiveSync ||
		reloaded.Media.Layout != "show" || len(reloaded.Download.Qualities) != 2 {
		t.Errorf("写盘内容与内存不一致: sync=%d adaptive=%v layout=%s quals=%v",
			reloaded.Collector.SyncDefaultMinutes, reloaded.Collector.AdaptiveSync,
			reloaded.Media.Layout, reloaded.Download.Qualities)
	}
	// .bak 备份存在
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Error("保存后应有 .bak 备份")
	}

	// 非法值：逐个 400
	for _, bad := range []string{
		`{"concurrency":0}`,
		`{"segments":-1}`,
		`{"sync_default_minutes":-5}`,
		`{"media_layout":"dvd"}`,
		`{"media_link_mode":"ftp"}`,
	} {
		if w := do(bad); w.Code != 400 {
			t.Errorf("非法请求 %s 应 400，实际 %d", bad, w.Code)
		}
	}
	// 非法请求不得污染内存
	if cfg.Download.Concurrency != 3 {
		t.Errorf("非法请求后 concurrency 不应变化，实际 %d", cfg.Download.Concurrency)
	}
	// 非法请求不得写盘
	reloaded2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded2.Download.Concurrency != 3 {
		t.Errorf("非法请求不应写盘，磁盘 concurrency=%d", reloaded2.Download.Concurrency)
	}
}
