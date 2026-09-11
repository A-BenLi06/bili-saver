// Package server 提供管理 REST API、媒体文件流（HTTP Range）、
// 内嵌管理页面静态资源，并把 teriteri 适配层挂到同一 mux。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"bili-saver/internal/adapter/teriteri"
	"bili-saver/internal/adapter/teriteriadmin"
	"bili-saver/internal/biliapi"
	"bili-saver/internal/card"
	"bili-saver/internal/collector"
	"bili-saver/internal/config"
	"bili-saver/internal/downloader"
	"bili-saver/internal/jobs"
	"bili-saver/internal/media"
	"bili-saver/internal/metrics"
	"bili-saver/internal/plugin"
	"bili-saver/internal/store"
	"bili-saver/internal/teriteriweb"
)

//go:embed static
var staticFS embed.FS

// Deps 汇集服务器依赖。
type Deps struct {
	Cfg  *config.Config
	St   *store.Store
	API  *biliapi.Client
	Col  *collector.Collector
	Dl   *downloader.Downloader
	Mgr  *jobs.Manager
	Bus  *plugin.Bus
	Med  *media.Bridge
	Card *card.Generator
	Log  *slog.Logger
	Teri *teriteriweb.Web // teriteri 多端站点管理（nil 则无一键安装能力）
	// ConfigPath 配置文件路径；非空时 PATCH /api/v1/config 会把变更写盘（.bak 备份）。
	ConfigPath string
}

// Server HTTP 服务。
type Server struct {
	Deps Deps
	mux  *http.ServeMux
	teri *teriteri.Adapter
}

func New(d Deps) *Server {
	s := &Server{Deps: d, mux: http.NewServeMux()}
	if d.Cfg.Teriteri.Enabled {
		// 密码自助修改：经此回调写回配置文件（两套适配层共享同一账号体系）
		passHook := func(npw string) error {
			d.Cfg.Teriteri.AdminPass = npw
			d.Cfg.Normalize()
			if err := d.Cfg.Validate(); err != nil {
				return err
			}
			if d.ConfigPath != "" {
				return config.SaveConfigFile(d.ConfigPath, d.Cfg)
			}
			return nil
		}
		s.teri = &teriteri.Adapter{
			St: d.St, DataDir: d.Cfg.DataDir,
			AdminUser: d.Cfg.Teriteri.AdminUser, AdminPass: d.Cfg.Teriteri.AdminPass,
			JWTSecret:    d.Cfg.Teriteri.JWTSecret,
			PasswordHook: passHook,
			Log:          d.Log,
		}
		s.teri.Register(s.mux)
		// teriteri-admin（管理员端）适配层：共用 JWT 与登录账号
		admin := &teriteriadmin.Adapter{
			St: d.St, DataDir: d.Cfg.DataDir,
			AdminUser: d.Cfg.Teriteri.AdminUser, AdminPass: d.Cfg.Teriteri.AdminPass,
			JWTSecret: d.Cfg.Teriteri.JWTSecret,
			Log:       d.Log,
		}
		admin.Register(s.mux)
	}

	s.registerAdmin()
	s.registerMedia()
	s.registerStatic()
	return s
}

func (s *Server) Handler() http.Handler { return s.auth(s.mux) }

// authUser 当前请求身份（由 auth 中间件解析并注入 context）。
type authUser struct {
	Role   string // admin | user
	ID     int64  // users.id；Bearer token 方式为 0
	Name   string
	TierID int64
}

type ctxKey struct{}

func (s *Server) currentUser(r *http.Request) authUser {
	v, _ := r.Context().Value(ctxKey{}).(authUser)
	return v
}

// userAllowed 用户角色的端点白名单（多用户：仅 添加/识别/查看/管理自己的补档 + 认证）。
func userAllowed(method, path string) bool {
	switch {
	case method == "POST" && path == "/api/v1/add":
		return true
	case method == "GET" && path == "/api/v1/sources":
		return true
	case (method == "PATCH" || method == "DELETE") && strings.HasPrefix(path, "/api/v1/sources/"):
		return true
	case method == "GET" && path == "/api/v1/resolve":
		return true
	case path == "/api/v1/auth/me" || path == "/api/v1/auth/logout":
		return true
	}
	return false
}

// auth 统一鉴权：Bearer admin.token（管理员，兼容脚本）→ 会话 cookie（多用户）→
// 匿名仅放行登录端点；user 角色按白名单限流，其余端点 403。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var au authUser
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.Deps.Cfg.Admin.Token != "" {
			tok := r.Header.Get("Authorization")
			tok = strings.TrimPrefix(tok, "Bearer ")
			if tok == "" {
				tok = r.URL.Query().Get("token")
			}
			if tok == s.Deps.Cfg.Admin.Token {
				au = authUser{Role: "admin", Name: "token"}
			}
		}
		if au.Role == "" {
			if ck, err := r.Cookie("bs_sess"); err == nil {
				if u, err := s.Deps.St.UserBySession(ck.Value); err == nil {
					au = authUser{Role: u.Role, ID: u.ID, Name: u.Username, TierID: u.TierID}
				}
			}
		}
		if au.Role == "" {
			if r.URL.Path == "/api/v1/auth/login" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		if au.Role == "user" && !userAllowed(r.Method, r.URL.Path) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "用户角色无权访问该功能"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, au)))
	})
}

// registerMedia 提供本地媒体流（Range 支持），仅允许 dataDir 内的文件。
func (s *Server) registerMedia() {
	s.mux.HandleFunc("GET /media/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/media/")
		rel = filepath.ToSlash(filepath.Clean("/" + rel))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || strings.Contains(rel, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// URL 形如 /media/<local_path>，local_path 本身以 media/ 开头
		full := filepath.Join(s.Deps.Cfg.DataDir, filepath.FromSlash(rel))
		absData, err1 := filepath.Abs(s.Deps.Cfg.DataDir)
		absFull, err2 := filepath.Abs(full)
		if err1 != nil || err2 != nil || !strings.HasPrefix(absFull, absData+string(filepath.Separator)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.ServeFile(&countingWriter{ResponseWriter: w}, r, absFull)
	})
}

// countingWriter 统计 /media 下发字节（上传吞吐）。
type countingWriter struct {
	http.ResponseWriter
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	metrics.Upload.Add(int64(n))
	return n, err
}

func (s *Server) registerStatic() {
	s.mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		b, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// 页面随二进制内嵌更新，禁缓存避免浏览器停留旧版 UI
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(name, ".html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		} else if strings.HasSuffix(name, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		} else if strings.HasSuffix(name, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(b)
	})
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}
