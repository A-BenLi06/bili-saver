package plugin

// 事件总线 + 进程插件执行器。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"bili-saver/internal/store"
)

// 事件名常量（collector/downloader 与此保持一致）。
const (
	EventVideoArchived    = "video.archived"
	EventVideoDiscovered  = "video.discovered"
	EventVideoDownloaded  = "video.downloaded"
	EventVideoGone        = "video.gone"
	EventCommentsResynced = "comments.resynced"
	EventSubtitleMissing  = "subtitle.missing"
	EventDailyTick        = "daily.tick"
	EventSearchQuery      = "search.query"
)

// Event 是派发给插件的事件载荷。
type Event struct {
	Event   string         `json:"event"`
	Payload map[string]any `json:"payload"`
	Config  map[string]any `json:"config"`
	Context Context        `json:"context"`
}

// Context 提供插件回写数据所需的连接信息。
type Context struct {
	DataDir string `json:"data_dir"`
	APIBase string `json:"api_base"`
	Token   string `json:"token"`
}

// op 是插件回传的一行 JSON 指令。
type op struct {
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params"`
}

// Writer 把插件产物写入 derived 层。
type Writer interface {
	AddSummary(sm *store.Summary) error
	UpsertSubtitle(sub *store.Subtitle) error
	AddTags(bvid string, names []string, origin string) error
	AddRelation(r *store.Relation) error
	AddEmbedding(entityType, entityID, model string, vec []float32, origin string) error
}

// PluginRowStore 提供插件注册表读写。
type PluginStore interface {
	UpsertPlugin(p *store.PluginRow) error
	ListPlugins() ([]*store.PluginRow, error)
	SetPluginEnabled(id string, enabled bool) error
	SetPluginConfig(id, configJSON string) error
	AddPluginRun(r *store.PluginRun) error
}

// Bus 管理插件注册与事件派发。
type Bus struct {
	st       PluginStore
	writer   Writer
	dir      string
	log      *slog.Logger
	mu       sync.RWMutex
	manifest map[string]*Manifest
	ctx      Context
	asyncWG  sync.WaitGroup
}

func NewBus(st PluginStore, writer Writer, dir string, log *slog.Logger, ctx Context) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{st: st, writer: writer, dir: dir, log: log, manifest: map[string]*Manifest{}, ctx: ctx}
}

// Reload 重新扫描插件目录并同步注册表。
func (b *Bus) Reload() error {
	ms, errs := LoadAll(b.dir)
	for _, e := range errs {
		b.log.Warn("插件清单加载失败", "err", e)
	}
	now := store.Now()
	b.mu.Lock()
	b.manifest = map[string]*Manifest{}
	for _, m := range ms {
		b.manifest[m.ID] = m
	}
	b.mu.Unlock()
	for _, m := range ms {
		mb, _ := json.Marshal(m)
		_ = b.st.UpsertPlugin(&store.PluginRow{
			ID: m.ID, Version: m.Version, Enabled: true,
			ManifestJSON: string(mb), ConfigJSON: "{}",
		})
	}
	_ = now
	return nil
}

// Emit 同步派发事件给所有订阅且启用的插件（每个插件独立超时与审计）。
// 内部并发执行，等待全部结束后返回。
func (b *Bus) Emit(event string, payload map[string]any) {
	b.mu.RLock()
	var targets []*Manifest
	for _, m := range b.manifest {
		if !manifestSubscribes(m, event) {
			continue
		}
		targets = append(targets, m)
	}
	b.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	// 查启用状态
	enabled := map[string]bool{}
	if rows, err := b.st.ListPlugins(); err == nil {
		for _, r := range rows {
			enabled[r.ID] = r.Enabled
		}
	}
	var wg sync.WaitGroup
	for _, m := range targets {
		if !enabled[m.ID] {
			continue
		}
		wg.Add(1)
		go func(m *Manifest) {
			defer wg.Done()
			b.runPlugin(m, event, payload)
		}(m)
	}
	wg.Wait()
}

// EmitAsync 异步派发（不阻塞业务路径）。Wait 可等待全部派发完成。
func (b *Bus) EmitAsync(event string, payload map[string]any) {
	b.asyncWG.Add(1)
	go func() {
		defer b.asyncWG.Done()
		b.Emit(event, payload)
	}()
}

// Wait 等待所有异步派发完成（短生命周期进程退出前调用）。
func (b *Bus) Wait() { b.asyncWG.Wait() }

func manifestSubscribes(m *Manifest, event string) bool {
	for _, e := range m.Events {
		if e == event || e == "*" {
			return true
		}
	}
	return false
}

// runPlugin 启动插件进程，写事件 JSON，逐行读取 op 并执行。
func (b *Bus) runPlugin(m *Manifest, event string, payload map[string]any) {
	start := time.Now()
	runErr := b.execPlugin(m, event, payload)
	dur := time.Since(start).Milliseconds()
	pr := &store.PluginRun{PluginID: m.ID, Event: event, DurationMs: dur, CreatedAt: store.Now()}
	if runErr != nil {
		pr.Error = runErr.Error()
		b.log.Warn("插件执行失败", "plugin", m.ID, "event", event, "err", runErr)
	} else {
		pr.Ok = true
	}
	_ = b.st.AddPluginRun(pr)
}

func (b *Bus) execPlugin(m *Manifest, event string, payload map[string]any) error {
	cfg := m.Config
	// 合并持久化的 per-plugin 配置
	if rows, err := b.st.ListPlugins(); err == nil {
		for _, r := range rows {
			if r.ID == m.ID && r.ConfigJSON != "" && r.ConfigJSON != "{}" {
				var persisted map[string]any
				if json.Unmarshal([]byte(r.ConfigJSON), &persisted) == nil && len(persisted) > 0 {
					merged := map[string]any{}
					for k, v := range cfg {
						merged[k] = v
					}
					for k, v := range persisted {
						merged[k] = v
					}
					cfg = merged
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Entry[0], m.Entry[1:]...)
	cmd.Dir = filepath.Join(b.dir, m.ID)
	if len(m.Env) > 0 {
		env := os.Environ()
		for k, v := range m.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = &pluginStderr{b: b, id: m.ID}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动插件: %w", err)
	}

	ev := Event{Event: event, Payload: payload, Config: cfg, Context: b.ctx}
	evJSON, _ := json.Marshal(ev)
	if _, err := stdin.Write(append(evJSON, '\n')); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	_ = stdin.Close()

	// 逐行读 op
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var o op
		if err := json.Unmarshal(line, &o); err != nil {
			b.log.Warn("插件输出无法解析", "plugin", m.ID, "line", string(line))
			continue
		}
		if err := b.applyOp(ctx, m, o); err != nil {
			b.log.Warn("插件 op 执行失败", "plugin", m.ID, "op", o.Op, "err", err)
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("插件超时（%ds）", m.TimeoutSec)
		}
		return waitErr
	}
	return nil
}

type pluginStderr struct {
	b  *Bus
	id string
}

func (p *pluginStderr) Write(d []byte) (int, error) {
	p.b.log.Info("插件日志", "plugin", p.id, "msg", string(d))
	return len(d), nil
}

// applyOp 执行插件回传的数据写入指令（只能写 derived 层）。
func (b *Bus) applyOp(ctx context.Context, m *Manifest, o op) error {
	origin := "plugin:" + m.ID
	switch o.Op {
	case "log":
		var p struct {
			Msg string `json:"msg"`
		}
		_ = json.Unmarshal(o.Params, &p)
		b.log.Info("插件", "plugin", m.ID, "msg", p.Msg)
		return nil
	case "save_summary":
		var p store.Summary
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return err
		}
		if p.Bvid == "" {
			return fmt.Errorf("bvid 必填")
		}
		p.ID, p.Origin, p.CreatedAt = 0, origin, store.Now()
		if p.Generator == "" {
			p.Generator = origin
		}
		return b.writer.AddSummary(&p)
	case "save_subtitle":
		var p struct {
			Bvid, Cid, Lang, LangDoc, Format, FilePath, Content, Model string
		}
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return err
		}
		// 允许插件直接传 content，由核心落盘
		if p.FilePath == "" && p.Content != "" {
			// 需要知道 dataDir；通过 ctx
			dir := filepath.Join(b.ctx.DataDir, "derived", sanitizeID(p.Bvid))
			_ = os.MkdirAll(dir, 0o755)
			ext := "srt"
			if p.Format == "vtt" {
				ext = "vtt"
			}
			path := filepath.Join(dir, fmt.Sprintf("%s.%s.%s", sanitizeID(p.Lang), origin2short(origin), ext))
			if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
				return err
			}
			p.FilePath = path
		}
		if p.FilePath == "" {
			return fmt.Errorf("file_path 或 content 必填")
		}
		var cid int64
		fmt.Sscanf(p.Cid, "%d", &cid)
		return b.writer.UpsertSubtitle(&store.Subtitle{
			Bvid: p.Bvid, Cid: cid, Lang: p.Lang, LangDoc: p.LangDoc,
			Origin: origin, Format: orDefaultStr(p.Format, "srt"),
			FilePath: p.FilePath, Model: p.Model, CreatedAt: store.Now(),
		})
	case "save_tag":
		var p struct {
			Bvid string   `json:"bvid"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return err
		}
		return b.writer.AddTags(p.Bvid, p.Tags, origin)
	case "save_relation":
		var p store.Relation
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return err
		}
		p.ID, p.Origin, p.CreatedAt = 0, origin, store.Now()
		return b.writer.AddRelation(&p)
	case "save_embedding":
		var p struct {
			EntityType string    `json:"entity_type"`
			EntityID   string    `json:"entity_id"`
			Model      string    `json:"model"`
			Vec        []float32 `json:"vec"`
		}
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return err
		}
		return b.writer.AddEmbedding(p.EntityType, p.EntityID, p.Model, p.Vec, origin)
	}
	return fmt.Errorf("未知 op %q", o.Op)
}

// Manifests 返回当前注册的插件清单。
func (b *Bus) Manifests() []*Manifest {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*Manifest, 0, len(b.manifest))
	for _, m := range b.manifest {
		out = append(out, m)
	}
	return out
}

func origin2short(origin string) string {
	for i := 0; i < len(origin); i++ {
		if origin[i] == ':' {
			return origin[i+1:]
		}
	}
	return origin
}

func orDefaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func sanitizeID(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
