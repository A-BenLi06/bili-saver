package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bili-saver/internal/config"
)

// URL query credentials are supported by the existing media.servers URL field;
// they are removed from all public links/status/config payloads.
func mediaEndpoint(cfg config.MediaServer) (*url.URL, string, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, "", fmt.Errorf("媒体库地址必须是HTTP或HTTPS服务根地址")
	}
	key := ""
	for k, values := range u.Query() {
		if secretMediaQuery(k) && len(values) > 0 {
			key = values[0]
			break
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u, key, nil
}

type mediaItem struct {
	ID          string            `json:"Id"`
	ServerID    string            `json:"ServerId"`
	Path        string            `json:"Path"`
	Type        string            `json:"Type"`
	IsFolder    bool              `json:"IsFolder"`
	ProviderIDs map[string]string `json:"ProviderIds"`
}

var mediaLookupClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var mediaStreamTransport = sync.OnceValue(func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 10 * time.Second
	return t
})
var mediaItemCache = struct {
	sync.Mutex
	items map[string]struct {
		item  mediaItem
		until time.Time
	}
}{items: make(map[string]struct {
	item  mediaItem
	until time.Time
})}

func mediaItems(ctx context.Context, base *url.URL, key string, q url.Values) ([]mediaItem, error) {
	u := *base
	u.Path += "/Items"
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("X-Emby-Token", key)
	}
	resp, err := mediaLookupClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("媒体库暂时无法连接，请检查接入地址")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("媒体库需要认证：请在此实例的配置地址中添加 api_key 参数（仅服务端保存）")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("媒体库条目查询失败（HTTP %d）", resp.StatusCode)
	}
	var result struct {
		Items []mediaItem `json:"Items"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, &result) != nil {
		return nil, fmt.Errorf("媒体库返回的条目数据无法读取")
	}
	return result.Items, nil
}

func matchesMediaItem(item mediaItem, bvid string, page int) bool {
	if item.ID == "" || item.IsFolder || (item.Type != "Movie" && item.Type != "Episode" && item.Type != "Video" && item.Type != "MusicVideo") {
		return false
	}
	path := strings.ReplaceAll(item.Path, "\\", "/")
	identity := false
	for k, v := range item.ProviderIDs {
		if strings.EqualFold(k, "bilibili") && v == bvid {
			identity = true
		}
	}
	bvPattern := regexp.MustCompile(`(?:^|[^a-zA-Z0-9])` + regexp.QuoteMeta(bvid) + `(?:[^a-zA-Z0-9]|$)`)
	identity = identity || bvPattern.MatchString(path)
	if !identity {
		return false
	}
	base := path[strings.LastIndex(path, "/")+1:]
	pPattern := regexp.MustCompile(`(?i)^(?:` + regexp.QuoteMeta(bvid) + `-)?p0*([0-9]+)(?:[ ./_-]|$)`)
	if part := pPattern.FindStringSubmatch(base); len(part) > 1 {
		n, _ := strconv.Atoi(part[1])
		return n == page
	}
	return page == 1 && !strings.Contains(strings.ToLower(path), "/extras/")
}

func resolveMediaItem(ctx context.Context, cfg config.MediaServer, bvid string, page int) (mediaItem, error) {
	cacheKey := fmt.Sprintf("%s\x00%s\x00%d", cfg.URL, bvid, page)
	mediaItemCache.Lock()
	cached, ok := mediaItemCache.items[cacheKey]
	mediaItemCache.Unlock()
	if ok && time.Now().Before(cached.until) {
		return cached.item, nil
	}
	base, key, err := mediaEndpoint(cfg)
	if err != nil {
		return mediaItem{}, err
	}
	q := url.Values{"Recursive": {"true"}, "Fields": {"Path,ProviderIds"}, "IncludeItemTypes": {"Movie,Episode,Video,MusicVideo"}, "Limit": {"100"}, "AnyProviderIdEquals": {"bilibili." + bvid}}
	for attempt := 0; attempt < 2; attempt++ {
		items, err := mediaItems(ctx, base, key, q)
		if err != nil {
			return mediaItem{}, err
		}
		matches := []mediaItem{}
		seen := map[string]bool{}
		for _, item := range items {
			if matchesMediaItem(item, bvid, page) && !seen[item.ID] {
				matches = append(matches, item)
				seen[item.ID] = true
			}
		}
		if len(matches) > 1 {
			return mediaItem{}, fmt.Errorf("此视频在媒体库中有多个匹配条目，请先在媒体库中确认版本")
		}
		if len(matches) == 1 {
			mediaItemCache.Lock()
			if len(mediaItemCache.items) >= 256 {
				for k := range mediaItemCache.items {
					delete(mediaItemCache.items, k)
					break
				}
			}
			mediaItemCache.items[cacheKey] = struct {
				item  mediaItem
				until time.Time
			}{matches[0], time.Now().Add(30 * time.Second)}
			mediaItemCache.Unlock()
			return matches[0], nil
		}
		q.Del("AnyProviderIdEquals")
		q.Set("SearchTerm", bvid)
	}
	return mediaItem{}, fmt.Errorf("媒体库尚未找到这个分P：请先扫描补档输出目录；不会跳转到其他同名视频")
}

func (s *Server) handleMediaPlay(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(r.PathValue("server"))
	if err != nil || i < 0 || i >= len(s.Deps.Cfg.View().Media.Servers) {
		errJSON(w, 404, errStr("媒体库连接不存在"))
		return
	}
	cfg := s.Deps.Cfg.View().Media.Servers[i]
	typ := strings.ToLower(strings.TrimSpace(cfg.Type))
	if typ != "emby" && typ != "jellyfin" {
		errJSON(w, 400, errStr("此媒体库不支持视频流入口"))
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
	for _, pg := range pages {
		known = known || pg.Page == page
	}
	if !known {
		errJSON(w, 404, errStr("这个分P不存在"))
		return
	}
	item, err := resolveMediaItem(r.Context(), cfg, v.Bvid, page)
	if err != nil {
		errJSON(w, 502, err)
		return
	}
	base, key, err := mediaEndpoint(cfg)
	if err != nil {
		errJSON(w, 400, err)
		return
	}
	if r.URL.Query().Get("entry") == "1" {
		base.Path += "/web/index.html"
		route := "/item?"
		if typ == "jellyfin" {
			route = "/details?"
		}
		q := url.Values{"id": {item.ID}}
		if item.ServerID != "" {
			q.Set("serverId", item.ServerID)
		}
		base.Fragment = "!" + route + q.Encode()
		if typ == "jellyfin" {
			base.Fragment = route + q.Encode()
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, base.String(), http.StatusFound)
		return
	}
	base.Path += "/Videos/" + url.PathEscape(item.ID) + "/stream.mp4"
	base.RawQuery = "Static=true"
	proxy := &httputil.ReverseProxy{
		Transport: mediaStreamTransport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(base)
			pr.Out.URL.Path = base.Path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = base.RawQuery
			pr.Out.Header = make(http.Header)
			for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match", "Accept"} {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
			if key != "" {
				pr.Out.Header.Set("X-Emby-Token", key)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return fmt.Errorf("unexpected media redirect")
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return fmt.Errorf("media authorization failed")
			}
			if resp.StatusCode >= 400 && resp.StatusCode != 416 {
				return fmt.Errorf("media stream unavailable")
			}
			resp.Header.Del("Set-Cookie")
			resp.Header.Del("X-Emby-Token")
			resp.Header.Del("Location")
			resp.Header.Set("Content-Disposition", "inline")
			resp.Header.Set("Referrer-Policy", "no-referrer")
			resp.Header.Set("Cache-Control", "private, no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			errJSON(w, 502, errStr("媒体库播放流不可用，请确认条目可播放和API密钥权限"))
		},
	}
	proxy.ServeHTTP(w, r)
}
