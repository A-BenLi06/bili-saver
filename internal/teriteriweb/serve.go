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

// 前缀命中即转发给核心主 handler（与 deploy/nginx.conf 的反代范围一致）。
var forwardedPrefixes = []string{"api/", "media/", "static/", "ws/", "im"}

// SiteHandler 返回站点 handler：dist 静态文件 + SPA 回退，其余转发核心。
func SiteHandler(dist string, main http.Handler) http.Handler {
	fs := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		for _, prefix := range forwardedPrefixes {
			if p == prefix[:len(prefix)-1] || strings.HasPrefix(p, prefix) {
				main.ServeHTTP(w, r)
				return
			}
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
