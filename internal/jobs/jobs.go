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
	"bili-saver/internal/taskflow"
)

// Handler 是某类任务的执行体。
type Handler func(ctx context.Context, j *store.Job, progress ProgressFn) error

// ProgressFn 汇报任务进度（0..1 与明细文本）。
type ProgressFn func(f float64, detail string)

// pendingJob 就绪队列条目：优先级降序、同优先级 FIFO。
type pendingJob struct {
	id   int64
	prio int
}

// Manager 任务队列。worker 从优先级就绪队列取任务：
// 优先级高者先执行（新添加补档插队），同优先级保持 FIFO。
type Manager struct {
	St             *store.Store
	Log            *slog.Logger
	workers        int
	mu             sync.Mutex
	cond           *sync.Cond
	pending        pendingQueue
	resumed        resumedQueue
	stopped        bool
	handlers       map[string]Handler
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	started        bool
	videoTypes     map[string]bool
	videoKeys      map[int64]string
	videoTokens    map[int64]int
	activeVideos   map[string]int64
	blockedVideos  map[string]*pendingQueue
	controlTypes   map[string]bool
	controlJobs    map[int64]bool
	control        pendingQueue
	controlResumed resumedQueue
}

func NewManager(st *store.Store, log *slog.Logger, workers int) *Manager {
	if workers < 1 {
		workers = 1
	}
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		St: st, Log: log, workers: workers,
		handlers: map[string]Handler{},
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// pushPendingLocked 按优先级降序插入就绪队列，同级放在末尾。调用方须持有 m.mu。
func (m *Manager) pushPendingLocked(id int64, prio int) {
	if key := m.videoKeys[id]; key != "" {
		m.enqueueVideoLocked(pendingJob{id: id, prio: prio})
		return
	}
	m.enqueueReadyLocked(id, prio)
}

// Register 注册任务类型（Start 之前调用）。
func (m *Manager) Register(jobType string, h Handler) {
	m.mu.Lock()
	m.handlers[jobType] = h
	m.mu.Unlock()
}

// Submit 入队一个任务；同类型同载荷的活动任务会去重。
func (m *Manager) Submit(jobType string, payload any) (int64, error) {
	return m.SubmitPrio(jobType, payload, 0)
}

// SubmitPrio 入队并指定优先级（>0 插队到就绪队列最前）。
func (m *Manager) SubmitPrio(jobType string, payload any, priority int) (int64, error) {
	pj, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	// 去重、落库与内存入队同一把锁，避免并发提交或启动恢复重复装载。
	m.mu.Lock()
	defer m.mu.Unlock()
	id, err := m.St.CreateActiveJobPrio(jobType, string(pj), priority)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, nil
	}
	m.submittedVideoKeyLocked(id, jobType, pj)
	m.setControlLocked(id, jobType)
	m.pushPendingLocked(id, priority)
	m.cond.Signal()
	return id, nil
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
	// 取消 context 时也唤醒空闲 worker，避免 cond.Wait 永久等待。
	context.AfterFunc(ctx2, func() {
		m.mu.Lock()
		m.cond.Broadcast()
		m.mu.Unlock()
	})

	// 崩溃恢复：running→queued，按执行顺序（优先级降序、FIFO）装载就绪队列
	if _, err := m.St.DB.Exec(`UPDATE jobs SET state='queued', updated_at=? WHERE state='running'`, store.Now()); err != nil {
		m.Log.Warn("任务恢复失败", "err", err)
	}
	if queued, err := m.St.QueuedJobs(); err != nil {
		m.Log.Warn("任务恢复装载失败", "err", err)
	} else {
		// 数据库为恢复的唯一来源，包括 Start 前 Submit 的任务。
		// 按 ID 升序进入各优先级 FIFO；只对优先级建堆，无需排序全部任务。
		m.pending = pendingQueue{}
		m.resumed = nil
		m.control = pendingQueue{}
		m.controlResumed = nil
		m.controlJobs = nil
		m.videoKeys = nil
		m.videoTokens = nil
		m.activeVideos = nil
		m.blockedVideos = nil
		for _, j := range queued {
			m.setControlLocked(j.ID, j.Type)
			m.setVideoKeyLocked(j.ID, j.Type, j.Bvid)
			m.pushPendingLocked(j.ID, j.Priority)
		}
	}
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker(ctx2)
	}
	m.mu.Unlock()
}

// Stop 停止 worker：唤醒等待者并退出；未执行任务留在 DB（下次启动恢复）。
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.started || m.cancel == nil {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	m.mu.Unlock()
	m.cond.Broadcast()
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		for m.readySizeLocked() == 0 && !m.stopped && ctx.Err() == nil {
			m.cond.Wait()
		}
		if m.stopped || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		pj := m.popReadyLocked()
		if !m.reserveVideoLocked(pj) {
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()

		m.process(ctx, pj.id)
		m.finishReservation(pj.id)
		if ctx.Err() != nil {
			return
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
	if err := m.St.UpdateJobState(id, "running", "", "", j.Progress); err != nil {
		m.Log.Error("任务启动状态写入失败", "job", id, "err", err)
		return
	}
	reporter := &progressReporter{latest: j.Progress, write: func(f float64, detail string) {
		if err := m.St.UpdateJobState(id, "running", detail, "", f); err != nil {
			m.Log.Warn("任务进度保存失败", "job", id, "err", err)
		}
	}}
	// finish 与并发回调互斥，终态写入后迟到的回调不会恢复 running。
	defer func() {
		f, detail := reporter.finish()
		if r := recover(); r != nil {
			m.Log.Error("任务处理器 panic", "job", id, "type", j.Type, "panic", r, "stack", string(debug.Stack()))
			_ = m.St.UpdateJobState(id, "failed", detail, fmt.Sprintf("处理器 panic: %v", r), f)
		}
	}()
	m.mu.Lock()
	control := m.controlJobs[id]
	m.mu.Unlock()
	var yield func(context.Context) error
	if !control {
		yield = m.runOneControl
	}
	ctx = taskflow.WithHooks(ctx, reporter.report, yield)
	err = h(ctx, j, reporter.report)
	f, detail := reporter.finish()
	if ctx.Err() != nil {
		_ = m.St.UpdateJobState(id, "queued", detail, "服务停止，任务重新排队", f)
		m.mu.Lock()
		m.pending.push(id, j.Priority)
		m.mu.Unlock()
		m.cond.Signal()
		return
	}
	if err != nil {
		_ = m.St.UpdateJobState(id, "failed", detail, err.Error(), f)
		return
	}
	_ = m.St.UpdateJobState(id, "done", detail, "", 1)
}

// Scheduler 定时把到期任务送入队列。
type Scheduler struct {
	St    *store.Store
	Mgr   *Manager
	Log   *slog.Logger
	Cfg   *config.Config // 活读取：设置页 PATCH config 后下一 tick 生效
	Every time.Duration  // 检查频率

	lastPruneAt int64 // 上次终态任务清理时间（仅 Run goroutine 访问，无需锁）
}

// sync_source 防刷库退避：某源连续失败达 backoffMinFails 次后，提交间隔按 2 的幂
// 拉长（上限 backoffMaxSeconds）。连续失败数取最近 20 条同类任务里该源的前导
// failed 计数，成功任务即清零自然解除（PITFALLS P-009：坏源曾以 ~1 条/分钟
// 失败刷库，淹没其他真实失败）。
const (
	backoffMinFails   = 5
	backoffMaxSeconds = 3600
)

func (s *Scheduler) syncSourceBlocked(srcID int64, now int64) bool {
	return s.sourceBlocked(s.sourceBackoffs()[srcID], now)
}

func (s *Scheduler) sourceBlocked(b sourceBackoff, now int64) bool {
	fails, lastSeen := b.fails, b.lastSeen
	if fails < backoffMinFails || lastSeen == 0 {
		return false
	}
	gap := int64(s.Every / time.Second)
	for i := backoffMinFails; i < fails && gap < backoffMaxSeconds; i++ {
		gap *= 2
	}
	if gap > backoffMaxSeconds {
		gap = backoffMaxSeconds
	}
	return now-lastSeen < gap
}

// 新发加速窗口与节奏（adaptive 开启时生效）：发布 24h 内的视频按 45min
// 节奏轻量刷统计（touch_stats），不等源级同步周期——发布初期数据变化最快。
const (
	freshWindowHours  = 24
	freshStatsMinutes = 45
)

// 终态任务滚动保留：done/failed 只留最近 1000 条，每 24h 清理一次。
// 任务表历史上被失败循环刷过 3000+ 条（P-009），不清会无限膨胀；
// 审计窗口（管理端任务页）与退避判断（最近 20 条）都不依赖更早的终态行。
const (
	pruneJobsKeep     = 1000
	pruneJobsEverySec = 24 * 3600
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
	if err := s.Mgr.PumpUserDownloads(s.Cfg.DataDir); err != nil {
		s.Log.Warn("用户下载队列调度失败", "err", err)
	}
	s.pruneJobsIfDue()
	if s.Cfg.Collector.AdaptiveSync {
		s.freshSweep()
	}
	due, err := s.St.DueSources(s.Cfg.Collector.SyncDefaultMinutes, store.Now())
	if err != nil {
		s.Log.Warn("查询到期源失败", "err", err)
		return
	}
	backoffs := s.sourceBackoffs()
	for _, src := range due {
		if ctx.Err() != nil {
			return
		}
		if s.sourceBlocked(backoffs[src.ID], store.Now()) {
			s.Log.Info("源 sync_source 连续失败，退避跳过本轮", "source", src.Title, "type", src.Type)
			continue
		}
		if id, err := s.Mgr.Submit("sync_source", map[string]any{"source_id": src.ID}); err == nil && id != 0 {
			s.Log.Info("调度源同步", "source", src.Title, "type", src.Type)
		}
	}
	if hours := s.Cfg.Collector.StatsResyncHours; hours > 0 {
		if s.statsDue(hours) {
			if id, err := s.Mgr.Submit("sync_stats_all", map[string]any{}); err == nil && id != 0 {
				s.Log.Info("调度全库统计重同步")
			}
		}
	}
}

// pruneJobsIfDue 每 24h 清理一次终态任务（滚动保留最近 pruneJobsKeep 条）。
func (s *Scheduler) pruneJobsIfDue() {
	now := store.Now()
	if s.lastPruneAt != 0 && now-s.lastPruneAt < pruneJobsEverySec {
		return
	}
	s.lastPruneAt = now
	n, err := s.St.PruneTerminalJobs(pruneJobsKeep)
	if err != nil {
		s.Log.Warn("终态任务清理失败", "err", err)
		return
	}
	if n > 0 {
		s.Log.Info("终态任务清理完成", "删除", n, "保留最近", pruneJobsKeep)
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
		if id, err := s.Mgr.Submit("touch_stats", map[string]any{"bvid": bvid}); err == nil && id != 0 {
			s.Log.Info("新发视频统计加速", "bvid", bvid)
		}
	}
}
