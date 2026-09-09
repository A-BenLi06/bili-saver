// Package jobs 提供持久化任务队列与定时调度：
// 任务落库（queued/running/done/failed + progress），崩溃恢复时重排队；
// 调度器按源的重同步间隔与全库统计周期自动提交任务。
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

// Handler 是某类任务的执行体。
type Handler func(ctx context.Context, j *store.Job, progress ProgressFn) error

// ProgressFn 汇报任务进度（0..1 与明细文本）。
type ProgressFn func(f float64, detail string)

// Manager 任务队列。
type Manager struct {
	St       *store.Store
	Log      *slog.Logger
	workers  int
	queue    chan int64
	mu       sync.Mutex
	handlers map[string]Handler
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

func NewManager(st *store.Store, log *slog.Logger, workers int) *Manager {
	if workers < 1 {
		workers = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		St: st, Log: log, workers: workers,
		queue:    make(chan int64, 2048),
		handlers: map[string]Handler{},
	}
}

// Register 注册任务类型（Start 之前调用）。
func (m *Manager) Register(jobType string, h Handler) {
	m.mu.Lock()
	m.handlers[jobType] = h
	m.mu.Unlock()
}

// Submit 入队一个任务；同类型同载荷的活动任务会去重。
func (m *Manager) Submit(jobType string, payload any) (int64, error) {
	pj, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	dup, err := m.St.HasActiveJob(jobType, string(pj))
	if err != nil {
		return 0, err
	}
	if dup {
		return 0, nil
	}
	id, err := m.St.CreateJob(jobType, string(pj))
	if err != nil {
		return 0, err
	}
	m.enqueue(id)
	return id, nil
}

func (m *Manager) enqueue(id int64) {
	select {
	case m.queue <- id:
	default:
		// 队列满：留在 DB queued 状态，恢复循环会捡起
		m.Log.Warn("任务队列已满，任务留在库中稍后执行", "job", id)
	}
}

// Start 启动 worker 与崩溃恢复；返回调度器 context。
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	ctx2, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	// 崩溃恢复：running→queued，并重新入队所有 queued
	if _, err := m.St.DB.Exec(`UPDATE jobs SET state='queued', updated_at=? WHERE state='running'`, store.Now()); err != nil {
		m.Log.Warn("任务恢复失败", "err", err)
	}
	if rows, err := m.St.ListJobs(1000, []string{"queued"}); err == nil {
		for i := len(rows) - 1; i >= 0; i-- { // 旧任务先入队（FIFO）
			m.enqueue(rows[i].ID)
		}
	}
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker(ctx2)
	}
}

// Stop 停止 worker。
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.started || m.cancel == nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-m.queue:
			if !ok {
				return
			}
			m.process(ctx, id)
		}
	}
}

func (m *Manager) process(ctx context.Context, id int64) {
	j, err := m.St.GetJob(id)
	if err != nil {
		return
	}
	m.mu.Lock()
	h, ok := m.handlers[j.Type]
	m.mu.Unlock()
	if !ok {
		_ = m.St.UpdateJobState(id, "failed", "", "未注册的任务类型 "+j.Type, 0)
		return
	}
	_ = m.St.UpdateJobState(id, "running", "", "", j.Progress)
	// 处理器 panic 视为任务失败，绝不允许带崩整个服务（worker 是共享长驻 goroutine）。
	defer func() {
		if r := recover(); r != nil {
			m.Log.Error("任务处理器 panic", "job", id, "type", j.Type, "panic", r, "stack", string(debug.Stack()))
			_ = m.St.UpdateJobState(id, "failed", "", fmt.Sprintf("处理器 panic: %v", r), j.Progress)
		}
	}()

	var lastUp time.Time
	progress := func(f float64, detail string) {
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		if time.Since(lastUp) < 500*time.Millisecond && f < 1 {
			return
		}
		lastUp = time.Now()
		_ = m.St.UpdateJobState(id, "running", detail, "", f)
	}

	err = h(ctx, j, progress)
	if ctx.Err() != nil {
		_ = m.St.UpdateJobState(id, "queued", "", "服务停止，任务重新排队", j.Progress)
		m.enqueue(id)
		return
	}
	if err != nil {
		_ = m.St.UpdateJobState(id, "failed", "", err.Error(), j.Progress)
		return
	}
	_ = m.St.UpdateJobState(id, "done", "", "", 1)
}

// Scheduler 定时把到期任务送入队列。
type Scheduler struct {
	St    *store.Store
	Mgr   *Manager
	Log   *slog.Logger
	Cfg   *config.Config // 活读取：设置页 PATCH config 后下一 tick 生效
	Every time.Duration  // 检查频率
}

// 新发加速窗口与节奏（adaptive 开启时生效）：发布 24h 内的视频按 45min
// 节奏轻量刷统计（touch_stats），不等源级同步周期——发布初期数据变化最快。
const (
	freshWindowHours  = 24
	freshStatsMinutes = 45
)

// Run 阻塞运行直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	if s.Every <= 0 {
		s.Every = time.Minute
	}
	t := time.NewTicker(s.Every)
	defer t.Stop()
	s.tick(ctx) // 启动即检查一次
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	if s.Cfg.Collector.AdaptiveSync {
		s.freshSweep()
	}
	due, err := s.St.DueSources(s.Cfg.Collector.SyncDefaultMinutes, store.Now())
	if err != nil {
		s.Log.Warn("查询到期源失败", "err", err)
		return
	}
	for _, src := range due {
		if _, err := s.Mgr.Submit("sync_source", map[string]any{"source_id": src.ID}); err == nil && src.ID != 0 {
			s.Log.Info("调度源同步", "source", src.Title, "type", src.Type)
		}
	}
	if hours := s.Cfg.Collector.StatsResyncHours; hours > 0 {
		if s.statsDue(hours) {
			if _, err := s.Mgr.Submit("sync_stats_all", map[string]any{}); err == nil {
				s.Log.Info("调度全库统计重同步")
			}
		}
	}
}

// statsDue 判断上一个 sync_stats_all 任务是否已超出 hours 周期。
// 按类型查最近一条，避免其它任务完成后误触发全库刷新。
func (s *Scheduler) statsDue(hours int) bool {
	var last int64
	var found bool
	if err := s.St.DB.QueryRow(`SELECT created_at FROM jobs WHERE type='sync_stats_all' ORDER BY id DESC LIMIT 1`).Scan(&last); err == nil {
		found = true
	}
	if !found {
		return true
	}
	return store.Now()-last >= int64(hours)*3600
}

// freshSweep 新发加速：提交窗口内统计已到期的轻量刷新任务。
// 队列按载荷去重；完成后 last_stats_at 前移，自然进入下一轮 45min 冷却。
func (s *Scheduler) freshSweep() {
	bvids, err := s.St.FreshStatsDue(store.Now(), freshWindowHours, freshStatsMinutes, 100)
	if err != nil {
		s.Log.Warn("新发加速扫描失败", "err", err)
		return
	}
	for _, bvid := range bvids {
		if _, err := s.Mgr.Submit("touch_stats", map[string]any{"bvid": bvid}); err == nil && bvid != "" {
			s.Log.Info("新发视频统计加速", "bvid", bvid)
		}
	}
}
