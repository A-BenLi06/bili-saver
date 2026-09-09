// Package teriteriweb 托管 teriteri 多端静态站点（用户端 / 管理员端）：
// 站点由核心的二级监听直接服务（SPA 回退），API/媒体/WS 前缀在进程内
// 转发给主 mux，无需额外反代进程；一键安装任务负责克隆→构建两套前端。
package teriteriweb

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// forwarded 命中即转发给核心主 handler（与 deploy/nginx.conf 的反代范围一致）。
// 注意 "im" 必须精确匹配：前缀匹配会误吞 img/*（站点图片全部 404）。
func forwarded(p string) bool {
	if p == "im" {
		return true
	}
	for _, pre := range []string{"api/", "media/", "static/", "ws/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// SiteHandler 返回站点 handler：dist 静态文件 + SPA 回退，其余转发核心。
func SiteHandler(dist string, main http.Handler) http.Handler {
	fs := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if forwarded(p) {
			// media/ 特例：站点自身构建产物里的静态媒体（如头图 webm）优先本地，
			// 未命中（真正的视频流 media/media/BV.../...）再转发核心
			if strings.HasPrefix(p, "media/") {
				if st, err := os.Stat(filepath.Join(dist, filepath.FromSlash(p))); err == nil && !st.IsDir() {
					fs.ServeHTTP(w, r)
					return
				}
			}
			main.ServeHTTP(w, r)
			return
		}
		full := filepath.Join(dist, filepath.FromSlash(p))
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		// SPA 回退：vue-router history 模式的任意路径都落到 index.html
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

// DistExists dist 是否已构建。
func DistExists(dist string) bool {
	st, err := os.Stat(filepath.Join(dist, "index.html"))
	return err == nil && !st.IsDir()
}
