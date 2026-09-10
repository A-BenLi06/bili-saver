package server

// 第三方媒体库在线状态探测：按类型打各自的健康端点，
// 收到任意 HTTP 响应（含 401/403 鉴权保护）即视为在线，超时/连接失败为离线。
// 总览页恒定显示五种规范类型；未配置实例的类型显示「未检测到」。

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

// canonicalTypes 总览页恒定显示的五种媒体库类型（有序）。
var canonicalTypes = []string{"teriteri", "emby", "jellyfin", "plex", "kodi"}

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

// MediaServerInstance 单实例探测结果。
type MediaServerInstance struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Online    bool   `json:"online"`
	LatencyMS int64  `json:"latency_ms"`
	Note      string `json:"note"`
}

// MediaServerStatus 一个类型行的聚合状态（总览页恒显示五行）。
type MediaServerStatus struct {
	Type      string                `json:"type"`
	Online    bool                  `json:"online"`
	Detected  bool                  `json:"detected"` // 是否配置了实例（或内置适配可用）
	Note      string                `json:"note"`
	LatencyMS int64                 `json:"latency_ms"`
	Instances []MediaServerInstance `json:"instances"`
}

// probeMediaServerInstance 探测单个实例；online=在超时内收到任意 HTTP 响应。
func probeMediaServerInstance(ctx context.Context, cfg config.MediaServer) MediaServerInstance {
	inst := MediaServerInstance{Name: cfg.Name, URL: cfg.URL}
	if cfg.URL == "" {
		inst.Note = "未配置地址"
		return inst
	}
	u := strings.TrimRight(cfg.URL, "/") + probePath(cfg.Type)
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		inst.Note = "地址无效: " + err.Error()
		return inst
	}
	req.Header.Set("User-Agent", "bili-saver-probe/1.0")
	start := time.Now()
	resp, err := probeClient.Do(req)
	inst.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		inst.Note = "连接失败: " + ctxErrText(ctx, err)
		return inst
	}
	defer resp.Body.Close()
	inst.Online = true
	inst.Note = fmt.Sprintf("HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		inst.Note += "（需鉴权）"
	}
	return inst
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

// mediaServerStatuses 聚合五种规范类型的在线状态（未配置的类型输出「未检测到」行）。
func (s *Server) mediaServerStatuses(ctx context.Context) []MediaServerStatus {
	grouped := map[string][]config.MediaServer{}
	for _, c := range s.Deps.Cfg.Media.Servers {
		t := strings.ToLower(strings.TrimSpace(c.Type))
		if t == "" {
			t = "kodi"
		}
		grouped[t] = append(grouped[t], c)
	}

	out := make([]MediaServerStatus, 0, len(canonicalTypes))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, typ := range canonicalTypes {
		instances := grouped[typ]
		row := MediaServerStatus{Type: typ, Instances: []MediaServerInstance{}}
		if len(instances) == 0 && typ == "teriteri" && s.Deps.Cfg.Teriteri.Enabled {
			// 内置适配随核心进程运行；前端地址仅作展示链接
			row.Detected = true
			row.Online = true
			row.Note = "内置适配 · 随核心进程运行"
			row.Instances = append(row.Instances, MediaServerInstance{
				Name: "内置适配", URL: s.Deps.Cfg.Teriteri.FrontendURL, Online: true,
				Note: "随核心进程运行",
			})
			mu.Lock()
			out = append(out, row)
			mu.Unlock()
			continue
		}
		if len(instances) == 0 {
			row.Note = "未检测到"
			mu.Lock()
			out = append(out, row)
			mu.Unlock()
			continue
		}
		row.Detected = true
		results := make([]MediaServerInstance, len(instances))
		for i := range instances {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = probeMediaServerInstance(ctx, instances[i])
			}(i)
		}
		wg.Wait()
		onlineCount := 0
		for _, r := range results {
			if r.Online {
				onlineCount++
				if row.LatencyMS == 0 || r.LatencyMS < row.LatencyMS {
					row.LatencyMS = r.LatencyMS
				}
			}
			row.Instances = append(row.Instances, r)
		}
		row.Online = onlineCount > 0
		if len(results) == 1 {
			row.Note = results[0].Note
		} else {
			row.Note = fmt.Sprintf("在线 %d/%d", onlineCount, len(results))
		}
		mu.Lock()
		out = append(out, row)
		mu.Unlock()
	}
	return out
}
