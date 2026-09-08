package server

// 第三方媒体库在线状态探测：按类型打各自的健康端点，
// 收到任意 HTTP 响应（含 401/403 鉴权保护）即视为在线，超时/连接失败为离线。

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"bili-saver/internal/config"
)

// probeTimeout 单实例探测超时。
const probeTimeout = 3 * time.Second

// probePath 按类型返回健康检查路径（相对服务根）。
func probePath(serverType string) string {
	switch strings.ToLower(serverType) {
	case "emby", "jellyfin":
		return "/System/Info/Public" // 无需鉴权的公开信息端点
	case "plex":
		return "/identity"
	default: // kodi / teriteri / 未知类型：根路径
		return "/"
	}
}

// MediaServerStatus 单实例探测结果。
type MediaServerStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	Online    bool   `json:"online"`
	LatencyMS int64  `json:"latency_ms"`
	Note      string `json:"note"`
}

// probeMediaServer 探测单个实例；online=在超时内收到任意 HTTP 响应。
func probeMediaServer(ctx context.Context, cfg config.MediaServer) MediaServerStatus {
	st := MediaServerStatus{Name: cfg.Name, Type: cfg.Type, URL: cfg.URL}
	if cfg.URL == "" {
		st.Note = "未配置地址"
		return st
	}
	u := strings.TrimRight(cfg.URL, "/") + probePath(cfg.Type)
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		st.Note = "地址无效: " + err.Error()
		return st
	}
	req.Header.Set("User-Agent", "bili-saver-probe/1.0")
	start := time.Now()
	resp, err := probeClient.Do(req)
	st.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		st.Note = "连接失败: " + ctxErrText(ctx, err)
		return st
	}
	defer resp.Body.Close()
	st.Online = true
	st.Note = fmt.Sprintf("HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		st.Note += "（需鉴权）"
	}
	return st
}

func ctxErrText(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "超时(" + strconv.Itoa(int(probeTimeout.Milliseconds())) + "ms)"
	}
	return err.Error()
}

var probeClient = &http.Client{
	// 不跟随重定向：能建立连接并有响应即认为在线
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

// mediaServerStatuses 并发探测配置列表 + 内置 teriteri 适配态。
func (s *Server) mediaServerStatuses(ctx context.Context) []MediaServerStatus {
	cfgs := append([]config.MediaServer{}, s.Deps.Cfg.Media.Servers...)
	// teriteri：内置适配随核心进程运行，天然在线；前端地址另配时优先展示配置项
	hasTeriteri := false
	for _, c := range cfgs {
		if strings.EqualFold(c.Type, "teriteri") {
			hasTeriteri = true
		}
	}
	var builtin *MediaServerStatus
	if s.Deps.Cfg.Teriteri.Enabled && !hasTeriteri {
		builtin = &MediaServerStatus{
			Name: "teriteri（内置适配）", Type: "teriteri",
			URL: s.Deps.Cfg.Teriteri.FrontendURL, Online: true,
			Note: "随核心进程运行",
		}
	}
	out := make([]MediaServerStatus, len(cfgs))
	var wg sync.WaitGroup
	for i := range cfgs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = probeMediaServer(ctx, cfgs[i])
		}(i)
	}
	wg.Wait()
	res := make([]MediaServerStatus, 0, len(out)+1)
	if builtin != nil {
		res = append(res, *builtin)
	}
	for _, st := range out {
		if st.Name == "" { // 未命名实例按类型+地址显示
			st.Name = strings.ToUpper(st.Type) + " " + st.URL
		}
		res = append(res, st)
	}
	return res
}
