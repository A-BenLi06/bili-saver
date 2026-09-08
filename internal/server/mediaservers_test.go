package server

// 媒体库在线探测测试：类型健康路径、在线判定、离线与超时。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"bili-saver/internal/config"
)

func TestProbeMediaServerByType(t *testing.T) {
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, typ := range []string{"emby", "jellyfin"} {
		st := probeMediaServer(context.Background(), config.MediaServer{Name: typ, Type: typ, URL: srv.URL})
		if !st.Online {
			t.Errorf("%s 应在线: %+v", typ, st)
		}
	}
	if !paths["/System/Info/Public"] {
		t.Error("emby/jellyfin 应探测 /System/Info/Public")
	}

	plex := probeMediaServer(context.Background(), config.MediaServer{Name: "plex", Type: "plex", URL: srv.URL})
	if !plex.Online || !paths["/identity"] {
		t.Errorf("plex 应探测 /identity: %+v", plex)
	}
	kodi := probeMediaServer(context.Background(), config.MediaServer{Name: "kodi", Type: "kodi", URL: srv.URL})
	if !kodi.Online || !paths["/"] {
		t.Errorf("kodi 应探测根路径: %+v", kodi)
	}
}

func TestProbeMediaServerAuthIsOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 鉴权保护实例：有响应即在线
	}))
	defer srv.Close()
	st := probeMediaServer(context.Background(), config.MediaServer{Name: "e", Type: "emby", URL: srv.URL})
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
	st := probeMediaServer(context.Background(), config.MediaServer{Name: "x", Type: "emby", URL: srv.URL})
	if st.Online {
		t.Fatalf("不可达应离线: %+v", st)
	}
	if st.Note == "" {
		t.Error("离线应有原因说明")
	}
	// 空地址：不发起请求
	empty := probeMediaServer(context.Background(), config.MediaServer{Name: "y", Type: "plex"})
	if empty.Online || empty.Note == "" {
		t.Fatalf("空地址应离线且带说明: %+v", empty)
	}
}
