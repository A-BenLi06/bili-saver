// web.go 多端站点管理器：二级监听生命周期 + 安装器持有。
package teriteriweb

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/metrics"
)

// Web 持有站点配置与运行状态。
type Web struct {
	Cfg *config.Config
	Log *slog.Logger
	// CarouselProvider 归档库封面轮播（一键安装构建 client 时写入）。
	CarouselProvider func() []CarouselItem

	mu        sync.Mutex
	main      http.Handler // 核心主 handler（Attach 注入，避免构造环）
	servers   map[string]*http.Server
	serving   map[string]string
	installer *Installer
}

func New(cfg *config.Config, log *slog.Logger) *Web {
	return &Web{Cfg: cfg, Log: log, serving: map[string]string{}, servers: map[string]*http.Server{}}
}

// Attach 注入核心主 handler（站点把 API/媒体/WS 前缀转回给它）。
func (w *Web) Attach(main http.Handler) {
	w.mu.Lock()
	w.main = main
	w.mu.Unlock()
}

func (w *Web) mainHandler() http.Handler {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.main
}

// Installer 返回单例安装器（首次调用时创建）。
func (w *Web) Installer() *Installer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.installer == nil {
		web := w
		w.installer = NewInstaller(w.Cfg.View().Teriteri.WebRoot, w.Log, func() {
			web.StartSites()
		})
		w.installer.Carousel = w.CarouselProvider
	}
	return w.installer
}

func (w *Web) ClientDist() string {
	return filepath.Join(w.Cfg.View().Teriteri.WebRoot, "client", "dist")
}
func (w *Web) AdminDist() string {
	return filepath.Join(w.Cfg.View().Teriteri.WebRoot, "admin", "dist")
}

// StartSites 对存在 dist 的端点启动二级监听（幂等；监听失败只记日志不中断核心）。
func (w *Web) StartSites() {
	sites := []struct {
		name string
		addr string
		dist string
	}{
		{"teriteri-client", w.Cfg.View().Teriteri.ClientAddr, w.ClientDist()},
		{"teriteri-admin", w.Cfg.View().Teriteri.AdminAddr, w.AdminDist()},
	}
	for _, s := range sites {
		if s.addr == "" || !DistExists(s.dist) {
			continue
		}
		w.mu.Lock()
		if _, ok := w.serving[s.name]; ok {
			w.mu.Unlock()
			continue
		}
		w.mu.Unlock()

		main := w.mainHandler()
		if main == nil {
			w.Log.Warn("站点启动跳过：主 handler 未就绪", "site", s.name)
			continue
		}
		h := SiteHandler(s.dist, main)
		srv := &http.Server{Addr: s.addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		w.mu.Lock()
		w.serving[s.name] = s.addr
		w.servers[s.name] = srv
		w.mu.Unlock()
		go func(name string) {
			w.Log.Info("TeriTeri 站点开始服务", "site", name, "addr", s.addr, "dist", s.dist)
			if err := metrics.ListenAndServe(srv); err != nil && err != http.ErrServerClosed {
				w.Log.Error("TeriTeri 站点退出", "site", name, "err", err)
				w.mu.Lock()
				delete(w.serving, name)
				w.mu.Unlock()
			}
		}(s.name)
	}
}

// Serving 当前已服务的站点（管理页展示用）。
func (w *Web) Serving() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]string{}
	for k, v := range w.serving {
		out[k] = v
	}
	return out
}

// Status 汇总状态（管理 API）。
func (w *Web) Status() map[string]any {
	inst := w.Installer()
	st := inst.Snapshot()
	clientDist, adminDist := inst.ClientDist(), inst.AdminDist()
	serving := w.Serving()
	return map[string]any{
		"install":      st,
		"client_ready": DistExists(clientDist),
		"admin_ready":  DistExists(adminDist),
		"client_addr":  w.Cfg.View().Teriteri.ClientAddr,
		"admin_addr":   w.Cfg.View().Teriteri.AdminAddr,
		"client_url":   localURL(w.Cfg.View().Teriteri.ClientAddr),
		"admin_url":    localURL(w.Cfg.View().Teriteri.AdminAddr),
		"serving":      serving,
		"git_npm_hint": "需要系统安装 git 与 Node.js（npm）",
	}
}

func localURL(addr string) string {
	if addr == "" {
		return ""
	}
	if len(addr) > 0 && addr[0] == ':' {
		return "http://127.0.0.1" + addr
	}
	return "http://" + addr
}

func (w *Web) Close() {
	w.mu.Lock()
	servers := make([]*http.Server, 0, len(w.servers))
	for _, srv := range w.servers {
		servers = append(servers, srv)
	}
	installer := w.installer
	w.mu.Unlock()
	if installer != nil {
		installer.Stop()
	}
	for _, srv := range servers {
		_ = srv.Close()
	}
}
