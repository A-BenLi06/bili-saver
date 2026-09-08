// adaptive.go — 自适应补档冷却引擎。
//
// 有效检测间隔 = 基准间隔 × 新发系数 × 活跃系数，clamp 到 [基准/4, min(基准×8, 14天)]：
//
//	新发系数（视频年龄 = now - pubdate；pubdate 未知按中性 1.0）：
//	  <24h → 0.5（新发加速）   <7d → 1.0（基准）
//	  <30d → 2.0（轻冷却）     ≥30d → 4.0（冷却）
//	活跃系数（最近两个统计快照的播放/评论增速；不足两个或间隔过短按 1.0）：
//	  view ≥100/小时 或 reply ≥20/天 → 0.5（热门加速）
//	  view ≥5/小时   或 reply ≥2/天  → 1.0（温热基准）
//	  有任何增长但低于上档           → 2.0（微温轻冷却）
//	  完全零增长                     → 4.0（沉寂强冷却）
//
// collector.adaptive_sync=false 时全部系数不生效（固定间隔，旧行为）。
// 语义：新发布的视频检测更勤（发布初期数据变化最快），
// 沉寂老视频自动拉长周期（省请求、风控友好）；活跃度以库内统计快照序列为准。
package app

import (
	"bili-saver/internal/store"
)

// 新发/活跃分档阈值。
const (
	freshMaxAgeSec      = 24 * 3600 // 新发加速窗口：24h
	weekSec             = 7 * 24 * 3600
	monthSec            = 30 * 24 * 3600
	hotViewPerHour      = 100          // 播放增速"热门"阈值
	hotReplyPerDay      = 20           // 评论增速"热门"阈值
	warmViewPerHour     = 5            // 播放增速"温热"阈值
	warmReplyPerDay     = 2            // 评论增速"温热"阈值
	minSnapshotGapSec   = 60           // 快照间隔低于此值不估计增速
	cooldownCapMult     = 8            // 冷却上限：基准×8
	cooldownCapAbsolute = 14 * 24 * 60 // 冷却绝对上限：14 天（分钟）
)

// ageFactor 新发系数：越新越勤。
func ageFactor(now, pubdate int64) float64 {
	if pubdate <= 0 {
		return 1.0
	}
	switch age := now - pubdate; {
	case age < freshMaxAgeSec:
		return 0.5
	case age < weekSec:
		return 1.0
	case age < monthSec:
		return 2.0
	default:
		return 4.0
	}
}

// activityFactor 活跃系数：recent 为按时间升序的最近统计快照。
func activityFactor(recent []*store.StatsSnapshot) float64 {
	if len(recent) < 2 {
		return 1.0
	}
	old, last := recent[0], recent[len(recent)-1]
	gap := last.Ts - old.Ts
	if gap < minSnapshotGapSec {
		return 1.0
	}
	hours := float64(gap) / 3600
	viewPerHour := float64(maxDelta(last.View-old.View)) / hours
	replyPerDay := float64(maxDelta(last.Reply-old.Reply)) * 24 / hours
	switch {
	case viewPerHour >= hotViewPerHour || replyPerDay >= hotReplyPerDay:
		return 0.5
	case viewPerHour >= warmViewPerHour || replyPerDay >= warmReplyPerDay:
		return 1.0
	case last.View > old.View || last.Reply > old.Reply:
		return 2.0
	default:
		return 4.0
	}
}

func maxDelta(d int) int {
	if d < 0 {
		return 0
	}
	return d
}

// syncMultiplier 该视频当前的自适应倍率（新发×活跃）。adaptive 关闭时恒为 1。
func (a *App) syncMultiplier(v *store.Video, now int64) float64 {
	if !a.Cfg.Collector.AdaptiveSync {
		return 1.0
	}
	recent, err := a.St.RecentStats(v.Bvid, 2)
	if err != nil {
		recent = nil
	}
	return ageFactor(now, v.Pubdate) * activityFactor(recent)
}

// stretchInterval 基准间隔乘倍率并 clamp：[-1 语义穿透, 基准/4, min(基准×8, 14天)]。
func stretchInterval(baseMinutes int, mult float64) int {
	if baseMinutes < 0 {
		return baseMinutes
	}
	eff := int(float64(baseMinutes) * mult)
	lo := baseMinutes / 4
	if lo < 1 {
		lo = 1
	}
	hi := baseMinutes * cooldownCapMult
	if hi > cooldownCapAbsolute {
		hi = cooldownCapAbsolute
	}
	if eff < lo {
		eff = lo
	}
	if eff > hi {
		eff = hi
	}
	return eff
}

// statsSkipAdaptive 全库统计刷新（sync_stats_all）是否应冷却跳过该视频。
// 功能关闭或全库统计禁用时永不跳过（维持原全刷行为）；从未刷新过的视频不跳过。
func (a *App) statsSkipAdaptive(v *store.Video, now int64) bool {
	if !a.Cfg.Collector.AdaptiveSync || a.Cfg.Collector.StatsResyncHours <= 0 {
		return false
	}
	if v.LastStatsAt == 0 {
		return false
	}
	base := a.Cfg.Collector.StatsResyncHours * 60
	eff := stretchInterval(base, a.syncMultiplier(v, now))
	return now-v.LastStatsAt < int64(eff)*60
}
