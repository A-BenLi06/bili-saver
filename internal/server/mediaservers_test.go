package server

// 媒体库在线探测测试：类型健康路径、在线判定、离线与超时、五行聚合。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"bili-saver/internal/config"
)

func TestProbeMediaServerInstanceByType(t *testing.T) {
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, typ := range []string{"emby", "jellyfin"} {
		st := probeMediaServerInstance(context.Background(), config.MediaServer{Name: typ, Type: typ, URL: srv.URL})
		if !st.Online {
			t.Errorf("%s 应在线: %+v", typ, st)
		}
	}
	if !paths["/System/Info/Public"] {
		t.Error("emby/jellyfin 应探测 /System/Info/Public")
	}

	plex := probeMediaServerInstance(context.Background(), config.MediaServer{Name: "plex", Type: "plex", URL: srv.URL})
	if !plex.Online || !paths["/identity"] {
		t.Errorf("plex 应探测 /identity: %+v", plex)
	}
	kodi := probeMediaServerInstance(context.Background(), config.MediaServer{Name: "kodi", Type: "kodi", URL: srv.URL})
	if !kodi.Online || !paths["/"] {
		t.Errorf("kodi 应探测根路径: %+v", kodi)
	}
}

func TestProbeMediaServerAuthIsOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 鉴权保护实例：有响应即在线
	}))
	defer srv.Close()
	st := probeMediaServerInstance(context.Background(), config.MediaServer{Name: "e", Type: "emby", URL: srv.URL})
	if !st.Online {
		t.Fatalf("401 也应算在线: %+v", st)
	}
	if st.Note == "" {
		t.Error("应带 HTTP 状态说明")
	}
}

func TestProbeMediaServerOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // 立即关闭 → 连接失败
	st := probeMediaServerInstance(context.Background(), config.MediaServer{Name: "x", Type: "emby", URL: srv.URL})
	if st.Online {
		t.Fatalf("不可达应离线: %+v", st)
	}
	if st.Note == "" {
		t.Error("离线应有原因说明")
	}
	empty := probeMediaServerInstance(context.Background(), config.MediaServer{Name: "y", Type: "plex"})
	if empty.Online || empty.Note == "" {
		t.Fatalf("空地址应离线且带说明: %+v", empty)
	}
}

func TestMediaServerStatusesCanonicalRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Server{Deps: Deps{Cfg: config.Defaults()}}
	s.Deps.Cfg.Teriteri.Enabled = true
	s.Deps.Cfg.Media.Servers = []config.MediaServer{
		{Name: "家庭Emby", Type: "emby", URL: srv.URL},
		// jellyfin/plex/kodi 不配置 → 未检测到
	}

	rows := s.mediaServerStatuses(context.Background())
	if len(rows) != 5 {
		t.Fatalf("应恒定返回 5 行，实际 %d", len(rows))
	}
	byType := map[string]MediaServerStatus{}
	for _, r := range rows {
		byType[r.Type] = r
	}
	if byType["teriteri"].Online != true || byType["teriteri"].Note == "" {
		t.Errorf("启用适配时 teriteri 应在线(内置): %+v", byType["teriteri"])
	}
	if !byType["emby"].Detected || !byType["emby"].Online {
		t.Errorf("已配置且可达的 emby 应在线: %+v", byType["emby"])
	}
	if byType["emby"].Instances[0].Name != "家庭Emby" {
		t.Errorf("实例名应透传: %+v", byType["emby"].Instances)
	}
	for _, typ := range []string{"jellyfin", "plex", "kodi"} {
		r := byType[typ]
		if r.Detected || r.Online || r.Note != "未检测到" {
			t.Errorf("未配置的 %s 应为 未检测到: %+v", typ, r)
		}
	}

	// teriteri 显式配置了实例时不再叠加内置项
	s.Deps.Cfg.Media.Servers = append(s.Deps.Cfg.Media.Servers,
		config.MediaServer{Name: "teri", Type: "teriteri", URL: srv.URL})
	rows = s.mediaServerStatuses(context.Background())
	byType = map[string]MediaServerStatus{}
	for _, r := range rows {
		byType[r.Type] = r
	}
	if len(byType["teriteri"].Instances) != 1 || byType["teriteri"].Instances[0].Name != "teri" {
		t.Errorf("显式配置 teriteri 时应只有该实例: %+v", byType["teriteri"])
	}
}
