package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bili-saver/internal/archivepath"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

type videoFileLink struct {
	Page    int               `json:"page"`
	Title   string            `json:"title"`
	Path    string            `json:"path"`
	Exists  bool              `json:"exists"`
	CanOpen bool              `json:"can_open"`
	OpenURL string            `json:"open_url"`
	Players []videoPlayerLink `json:"players"`
}

type videoPlayerLink struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	EntryURL string `json:"entry_url,omitempty"`
}

// Only a browser connected directly through localhost may open this machine's
// file manager. Forwarded headers are intentionally not trusted.
func localBrowser(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	host = r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func localAction(r *http.Request) bool {
	if !localBrowser(r) || r.Header.Get("X-Bili-Saver-Local") != "1" {
		return false
	}
	u, err := url.Parse(r.Header.Get("Origin"))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

// Paths are selected from an archived video, never supplied by the browser.
func (s *Server) videoFile(v *store.Video, page int) (string, bool) {
	root, err := filepath.Abs(s.Deps.Cfg.View().DataDir)
	if err != nil {
		return "", false
	}
	if page == 1 && v.LocalPath != "" {
		if _, e := archivepath.Resolve(root, v.LocalPath); e != nil {
			return "", false
		}
	}
	rel, resolveErr := s.Deps.St.PartLocalPath(v.Bvid, page)
	if resolveErr != nil {
		if page == 1 {
			rel = v.LocalPath
		} else {
			return "", false
		}
	}
	p, pathErr := archivepath.Resolve(root, rel)
	if pathErr != nil {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p, err = filepath.Abs(p)
	if err != nil || !withinPath(root, p) {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return p, false
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return p, false
	}
	physical, err := filepath.EvalSymlinks(p)
	return p, err == nil && withinPath(physicalRoot, physical)
}

func withinPath(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeMediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.User = nil
	q := u.Query()
	for k := range q {
		if secretMediaQuery(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func secretMediaQuery(k string) bool {
	switch strings.ToLower(k) {
	case "api_key", "apikey", "x-emby-token", "access_token", "token":
		return true
	}
	return false
}

func (s *Server) teriteriVideoURL(r *http.Request, vid string) string {
	base := safeMediaURL(s.Deps.Cfg.View().Teriteri.FrontendURL)
	if base == "" && s.Deps.Teri != nil {
		if addr := s.Deps.Teri.Serving()["teriteri-client"]; addr != "" {
			host, port, err := net.SplitHostPort(addr)
			if err == nil {
				if host == "" || host == "0.0.0.0" || host == "::" {
					host = r.URL.Hostname()
					if host == "" {
						host = r.Host
						if h, _, e := net.SplitHostPort(host); e == nil {
							host = h
						}
					}
				}
				base = "http://" + net.JoinHostPort(host, port)
			}
		}
	}
	if base == "" {
		for _, cfg := range s.Deps.Cfg.View().Media.Servers {
			if strings.EqualFold(strings.TrimSpace(cfg.Type), "teriteri") {
				if candidate := safeMediaURL(cfg.URL); candidate != "" {
					base = candidate
					break
				}
			}
		}
	}
	if base == "" {
		return ""
	}
	return teriteriWatchURL(base, r, vid)
}

func teriteriWatchURL(base string, r *http.Request, vid string) string {
	u, _ := url.Parse(base)
	host := r.URL.Hostname()
	if host == "" {
		host = r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	// A browser on another machine must not be sent to its own localhost.
	if (strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback()) && host != "" && !strings.EqualFold(host, "localhost") && !net.ParseIP(host).IsLoopback() {
		port := u.Port()
		u.Host = host
		if port != "" {
			u.Host = net.JoinHostPort(host, port)
		} else if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
	if u.Fragment != "" {
		u.Fragment = "/video/" + vid
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/video/" + vid
	}
	return u.String()
}

func (s *Server) videoLinks(r *http.Request, v *store.Video, pages []*store.Page) []videoFileLink {
	if len(pages) == 0 {
		pages = []*store.Page{{Page: 1, Part: v.Title}}
	}
	links := make([]videoFileLink, 0, len(pages))
	for _, pg := range pages {
		if pg.Page < 1 {
			continue
		}
		p, exists := s.videoFile(v, pg.Page)
		vid := v.Bvid
		if pg.Page > 1 {
			vid += fmt.Sprintf("-p%d", pg.Page)
		}
		players := []videoPlayerLink{}
		seenURLs := map[string]bool{}
		if target := s.teriteriVideoURL(r, vid); target != "" {
			players = append(players, videoPlayerLink{Type: "teriteri", Name: "TeriTeri 播放", URL: target})
			seenURLs[target] = true
		}
		for i, cfg := range s.Deps.Cfg.View().Media.Servers {
			typ := strings.ToLower(strings.TrimSpace(cfg.Type))
			if safeMediaURL(cfg.URL) == "" {
				continue
			}
			name := cfg.Name
			if name == "" {
				name = typ
			}
			if typ == "teriteri" {
				target := teriteriWatchURL(safeMediaURL(cfg.URL), r, vid)
				if !seenURLs[target] {
					players = append(players, videoPlayerLink{Type: typ, Name: name + " 播放", URL: target})
					seenURLs[target] = true
				}
			} else if typ == "emby" || typ == "jellyfin" {
				play := fmt.Sprintf("/api/v1/videos/%s/play/%d?page=%d", url.PathEscape(v.Bvid), i, pg.Page)
				players = append(players, videoPlayerLink{Type: typ, Name: name + " 播放", URL: play, EntryURL: play + "&entry=1"})
			}
		}
		links = append(links, videoFileLink{Page: pg.Page, Title: pg.Part, Path: p, Exists: exists, CanOpen: exists && localBrowser(r), OpenURL: fmt.Sprintf("/videos/%s/local-open?page=%d", url.PathEscape(v.Bvid), pg.Page), Players: players})
	}
	return links
}

func (s *Server) handleLocalOpen(w http.ResponseWriter, r *http.Request) {
	if !localAction(r) {
		errJSON(w, 403, errStr("请通过本机 localhost 页面打开本地位置；远程访问可复制路径"))
		return
	}
	v, err := s.Deps.St.GetVideo(r.PathValue("bvid"))
	if err != nil {
		errJSON(w, 404, errStr("视频不存在"))
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pages, err := s.Deps.St.ListPages(v.Bvid)
	if err != nil {
		errJSON(w, 500, errStr("读取分P信息失败"))
		return
	}
	known := page == 1 && len(pages) == 0
	for _, p := range pages {
		known = known || p.Page == page
	}
	p, exists := s.videoFile(v, page)
	if !known || !exists {
		errJSON(w, 404, errStr("此分P的本地文件尚未下载或已不在原位置"))
		return
	}
	s.openKnownPath(w, p)
}

func (s *Server) handleLibraryLocalOpen(w http.ResponseWriter, r *http.Request) {
	if !localAction(r) {
		errJSON(w, 403, errStr("请通过本机 localhost 页面打开媒体库目录；远程访问可复制路径"))
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	libs, err := s.Deps.St.ListMediaLibraries()
	if err != nil {
		errJSON(w, 500, errStr("读取媒体库失败"))
		return
	}
	for _, lib := range libs {
		if lib.ID == id {
			p, e := filepath.Abs(lib.RootPath)
			if e != nil {
				break
			}
			st, e := os.Stat(p)
			if e != nil || !st.IsDir() {
				errJSON(w, 404, errStr("媒体库目录尚未创建或无法访问"))
				return
			}
			s.openKnownPath(w, p)
			return
		}
	}
	errJSON(w, 404, errStr("媒体库不存在"))
}

func (s *Server) openKnownPath(w http.ResponseWriter, p string) {
	open := s.revealPath
	if open == nil {
		open = revealLocalPath
	}
	if err := open(p); err != nil {
		errJSON(w, 500, errStr("无法启动本机文件管理器，请复制路径打开"))
		return
	}
	writeJSON(w, 200, map[string]any{"opened": true, "path": p})
}

// Expose only a sanitized address; credentials stay in the server config.
func publicMediaServers(servers []config.MediaServer) []map[string]any {
	result := make([]map[string]any, 0, len(servers))
	for _, cfg := range servers {
		u, _ := url.Parse(cfg.URL)
		hasKey := false
		if u != nil {
			for k := range u.Query() {
				hasKey = hasKey || secretMediaQuery(k)
			}
		}
		result = append(result, map[string]any{"name": cfg.Name, "type": cfg.Type, "url": safeMediaURL(cfg.URL), "auth_configured": hasKey})
	}
	return result
}
