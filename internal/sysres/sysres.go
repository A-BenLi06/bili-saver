// Package sysres 采集本进程与宿主机的 CPU/内存/网络资源快照（gopsutil，纯只读）。
// Windows/Linux/macOS 通用；采集失败的字段以零值返回，不 panic。
// CPU 百分比统一为「占全部逻辑核心容量的百分比」（0~100），进程值与系统值同尺度、
// 可直接组合成 donut 的两段。所有瞬时值基于上次调用的差分——包内维护基线，
// 首次调用进程 CPU 为 0（无基线），自第二次起为真实窗口瞬时值。
package sysres

import (
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Snapshot 一次资源快照（全部为瞬时/近窗口值）。
type Snapshot struct {
	Cores int `json:"cores"` // 逻辑核心数
	// CPU 总体（全核心近窗口利用率百分比 0~100）
	CPUPercent float64 `json:"cpu_percent"`
	// 本进程（占全部核心容量的百分比 0~100）
	ProcCPUPercent float64 `json:"proc_cpu_percent"`
	ProcRSSBytes   uint64  `json:"proc_rss_bytes"` // 常驻内存
	ProcThreads    int32   `json:"proc_threads"`
	Goroutines     int     `json:"goroutines"`
	// 宿主内存
	MemTotal uint64 `json:"mem_total"`
	MemUsed  uint64 `json:"mem_used"`
	// 网络累计字节（自开机，前端做差分算速率）
	NetSentTotal uint64 `json:"net_sent_total"`
	NetRecvTotal uint64 `json:"net_recv_total"`
	UnixMs       int64  `json:"unix_ms"`
}

type cpuSample struct {
	total float64 // user+system 累计秒
	wall  int64   // unix ms
}

var (
	mu       sync.Mutex
	selfProc *process.Process
	lastCPU  = map[int32]cpuSample{}
	lastWall int64
)

// instantPct 以差分计算单进程 CPU 占全部核心容量的百分比（0~100）。
// 首次见到该 PID 返回 0（建立基线）。
func instantPct(pid int32, ts *cpu.TimesStat, nowMs int64, cores int) float64 {
	if ts == nil {
		return 0
	}
	cur := cpuSample{total: ts.User + ts.System, wall: nowMs}
	prev, ok := lastCPU[pid]
	lastCPU[pid] = cur
	if !ok {
		return 0
	}
	dTotal := cur.total - prev.total
	dWall := float64(cur.wall-prev.wall) / 1000
	if dWall <= 0.05 || dTotal < 0 {
		return 0
	}
	pct := dTotal / dWall / float64(cores) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ProcRow 进程排行行（CPU 为占总容量百分比的差分瞬时值）。
type ProcRow struct {
	PID      int32   `json:"pid"`
	Name     string  `json:"name"`
	CPUPct   float64 `json:"cpu_pct"`
	MemBytes uint64  `json:"mem_bytes"`
	MemPct   float64 `json:"mem_pct"`
	IsSelf   bool    `json:"is_self"`
}

// Read 一次调用返回资源快照与进程排行（同一墙钟采样、同一差分基线，
// 快照中的进程字段与排行中 self 行数值严格一致）。
// CPU 百分比均为自上次调用的差分瞬时值（占全部核心容量 0~100）；
// 首次调用进程 CPU 为 0（建立基线），自第二次起为真实窗口值。
func Read(limit int) (Snapshot, []ProcRow) {
	s := Snapshot{UnixMs: time.Now().UnixMilli()}
	s.Cores = runtime.NumCPU()
	s.Goroutines = runtime.NumGoroutine()

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		s.CPUPercent = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemTotal = vm.Total
		s.MemUsed = vm.Used
	}
	if io, err := net.IOCounters(false); err == nil && len(io) > 0 {
		s.NetSentTotal = io[0].BytesSent
		s.NetRecvTotal = io[0].BytesRecv
	}

	selfPID := int32(os.Getpid())
	if limit <= 0 || limit > 16 {
		limit = 8
	}
	ps, err := process.Processes()
	if err != nil {
		return s, nil
	}
	rows := make([]ProcRow, 0, len(ps))
	mu.Lock()
	for _, p := range ps {
		name, _ := p.Name()
		if name == "" {
			continue
		}
		r := ProcRow{PID: p.Pid, Name: name, IsSelf: p.Pid == selfPID}
		if ts, err := p.Times(); err == nil {
			r.CPUPct = instantPct(p.Pid, ts, s.UnixMs, s.Cores)
		}
		if mi, err := p.MemoryInfo(); err == nil {
			r.MemBytes = mi.RSS
			if s.MemTotal > 0 {
				r.MemPct = float64(mi.RSS) / float64(s.MemTotal) * 100
			}
		}
		rows = append(rows, r)
	}
	lastWall = s.UnixMs
	mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].CPUPct > rows[j].CPUPct })
	// 快照独立于排行截断：本进程低负载时仍必须展示真实 RSS/CPU。
	for _, r := range rows {
		if r.IsSelf {
			s.ProcCPUPercent = r.CPUPct
			s.ProcRSSBytes = r.MemBytes
			break
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if p, err := process.NewProcess(selfPID); err == nil {
		if th, err := p.NumThreads(); err == nil {
			s.ProcThreads = th
		}
	}
	return s, rows
}
