package app

// 自适应补档冷却（adaptive.go）单元测试：分档系数、clamp 边界、
// dueParts 自适应行为、sync_stats_all 冷却跳过、新发加速扫描选集。

import (
	"testing"

	"bili-saver/internal/store"
)

func TestAgeFactor(t *testing.T) {
	now := store.Now()
	day := int64(86400)
	cases := []struct {
		pubdate int64
		want    float64
	}{
		{0, 1.0},             // 未知发布时间 → 中性
		{now - 3600, 0.5},    // 1h
		{now - 23*3600, 0.5}, // 23h
		{now - 25*3600, 1.0}, // 25h
		{now - 6*day, 1.0},   // 6d
		{now - 8*day, 2.0},   // 8d
		{now - 29*day, 2.0},  // 29d
		{now - 31*day, 4.0},  // 31d
	}
	for _, c := range cases {
		if got := ageFactor(now, c.pubdate); got != c.want {
			t.Errorf("ageFactor(pubdate 偏移 %d) = %v, want %v", now-c.pubdate, got, c.want)
		}
	}
}

func TestActivityFactor(t *testing.T) {
	now := store.Now()
	mk := func(ts int64, view, reply int) *store.StatsSnapshot {
		return &store.StatsSnapshot{Ts: ts, View: view, Reply: reply}
	}
	h := func(n int64) int64 { return now + n*3600 }
	cases := []struct {
		name  string
		snaps []*store.StatsSnapshot
		want  float64
	}{
		{"不足两个快照", []*store.StatsSnapshot{mk(now, 10, 1)}, 1.0},
		{"快照间隔过短", []*store.StatsSnapshot{mk(now, 10, 1), mk(now+30, 99999, 99)}, 1.0},
		{"热门高播放", []*store.StatsSnapshot{mk(now, 0, 0), mk(h(2), 1000, 0)}, 0.5},       // 500/h
		{"热门高评论", []*store.StatsSnapshot{mk(now, 0, 0), mk(h(1), 10, 40)}, 0.5},        // 960/天
		{"温热", []*store.StatsSnapshot{mk(now, 0, 0), mk(h(2), 20, 0)}, 1.0},            // 10/h
		{"微温有增长", []*store.StatsSnapshot{mk(now, 1000, 10), mk(h(24), 1010, 10)}, 2.0}, // 10/24h
		{"完全沉寂", []*store.StatsSnapshot{mk(now, 1000, 10), mk(h(24), 1000, 10)}, 4.0},
	}
	for _, c := range cases {
		if got := activityFactor(c.snaps); got != c.want {
			t.Errorf("%s: activityFactor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStretchInterval(t *testing.T) {
	cases := []struct {
		base int
		mult float64
		want int
	}{
		{360, 1.0, 360},    // 中性不变
		{360, 0.25, 90},    // 加速下限：基准/4
		{360, 0.5, 180},    // 新发加速
		{360, 16, 2880},    // 冷却上限：基准×8
		{10, 0.1, 2},       // 小基准：10/4=2 下限抬升
		{1, 0.1, 1},        // 极小基准下限 ≥1
		{100000, 8, 20160}, // 绝对上限：14 天
		{-1, 4, -1},        // "从不"语义穿透
	}
	for _, c := range cases {
		if got := stretchInterval(c.base, c.mult); got != c.want {
			t.Errorf("stretchInterval(%d, %v) = %d, want %d", c.base, c.mult, got, c.want)
		}
	}
}

// TestDuePartsAdaptive 新发热门视频加速到期、老旧沉寂视频冷却未到期。
func TestDuePartsAdaptive(t *testing.T) {
	a := newTestApp(t)
	now := store.Now()
	src := &store.Source{SyncIntervalMinutes: 360} // 全部跟随主间隔 6h

	// 新发热门：0.5×0.5=0.25 → 有效 90min → 100min 前的评论也视为到期
	if err := a.St.UpsertVideo(&store.Video{
		Bvid: "BV1FreshHot", Title: "新发热门", Pubdate: now - 3600, Status: "ok",
		FirstArchivedAt: now, LastCommentsAt: now - 100*60,
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*store.StatsSnapshot{
		{Bvid: "BV1FreshHot", Ts: now - 7200, View: 0, Reply: 0},
		{Bvid: "BV1FreshHot", Ts: now - 3600, View: 1000, Reply: 5}, // 1000/h 热门
	} {
		if err := a.St.AppendStats(s); err != nil {
			t.Fatal(err)
		}
	}
	v, err := a.St.GetVideo("BV1FreshHot")
	if err != nil {
		t.Fatal(err)
	}
	stats, comments, danmaku, subs := a.dueParts(src, v, now)
	// danmaku/subs last=0（从未刷新）按语义恒到期，不在此断言
	if !stats || !comments {
		t.Errorf("新发热门有效间隔 90min，100min 前的 stats/comments 应到期: %v %v（danmaku=%v subs=%v）", stats, comments, danmaku, subs)
	}

	// 老旧沉寂：4×4=16 → clamp 8× → 有效 48h → 24h 前的 stats/comments 不应到期
	if err := a.St.UpsertVideo(&store.Video{
		Bvid: "BV1OldQuiet", Title: "老旧沉寂", Pubdate: now - 40*86400, Status: "ok",
		FirstArchivedAt: now, LastStatsAt: now - 24*3600, LastCommentsAt: now - 24*3600,
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*store.StatsSnapshot{
		{Bvid: "BV1OldQuiet", Ts: now - 48*3600, View: 5000, Reply: 100},
		{Bvid: "BV1OldQuiet", Ts: now - 24*3600, View: 5000, Reply: 100}, // 零增长
	} {
		if err := a.St.AppendStats(s); err != nil {
			t.Fatal(err)
		}
	}
	v2, err := a.St.GetVideo("BV1OldQuiet")
	if err != nil {
		t.Fatal(err)
	}
	stats2, comments2, _, _ := a.dueParts(src, v2, now)
	if stats2 || comments2 {
		t.Errorf("老旧沉寂 24min×60 应被冷却（有效 48h）: stats=%v comments=%v", stats2, comments2)
	}

	// 关闭自适应：同一条老旧沉寂视频按固定 6h 间隔应到期
	a.Cfg.Collector.AdaptiveSync = false
	stats3, comments3, _, _ := a.dueParts(src, v2, now)
	if !stats3 || !comments3 {
		t.Errorf("adaptive 关闭应回退固定间隔到期: %v %v", stats3, comments3)
	}
}

// TestStatsSkipAdaptive sync_stats_all 的冷却跳过判定。
func TestStatsSkipAdaptive(t *testing.T) {
	a := newTestApp(t)
	a.Cfg.Collector.StatsResyncHours = 24 // 基准 1440min
	now := store.Now()
	if err := a.St.UpsertVideo(&store.Video{
		Bvid: "BV1Skip", Title: "沉寂", Pubdate: now - 40*86400, Status: "ok",
		FirstArchivedAt: now, LastStatsAt: now - 24*3600,
	}); err != nil {
		t.Fatal(err)
	}
	// 沉寂快照 → 冷却上限 8×1440=11520min → 24h 未刷应跳过
	for _, s := range []*store.StatsSnapshot{
		{Bvid: "BV1Skip", Ts: now - 48*3600, View: 100},
		{Bvid: "BV1Skip", Ts: now - 24*3600, View: 100},
	} {
		if err := a.St.AppendStats(s); err != nil {
			t.Fatal(err)
		}
	}
	v, err := a.St.GetVideo("BV1Skip")
	if err != nil {
		t.Fatal(err)
	}
	if !a.statsSkipAdaptive(v, now) {
		t.Error("沉寂老视频 24h 未刷应冷却跳过")
	}
	// 从未刷新过 → 不跳过
	v.LastStatsAt = 0
	if a.statsSkipAdaptive(v, now) {
		t.Error("从未刷新的视频不应跳过")
	}
	v.LastStatsAt = now - 24*3600
	// 关闭自适应 → 永不跳过（旧行为全刷）
	a.Cfg.Collector.AdaptiveSync = false
	if a.statsSkipAdaptive(v, now) {
		t.Error("adaptive 关闭不应跳过")
	}
}

// TestFreshStatsDue 新发加速扫描的选集边界。
func TestFreshStatsDue(t *testing.T) {
	a := newTestApp(t)
	now := store.Now()
	mk := func(bvid string, pubdate, lastStats int64) {
		if err := a.St.UpsertVideo(&store.Video{
			Bvid: bvid, Title: bvid, Pubdate: pubdate, Status: "ok",
			FirstArchivedAt: now, LastStatsAt: lastStats,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("BV1FreshDue", now-3600, now-2*3600)   // 新发 + 2h 未刷 → 命中
	mk("BV1FreshRecent", now-3600, now-60)    // 新发 + 1min 前刚刷 → 不命中
	mk("BV1OldDue", now-90*86400, now-2*3600) // 老视频到期也不归新发扫 → 不命中
	mk("BV1FreshGone", now-3600, 0)           // 新发但已消失 → 不命中
	if err := a.St.MarkVideoGone("BV1FreshGone", "gone"); err != nil {
		t.Fatal(err)
	}
	mk("BV1FreshNoStats", now-7200, 0) // 新发从未刷 → 命中
	got, err := a.St.FreshStatsDue(now, 24, 45, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"BV1FreshDue": true, "BV1FreshNoStats": true}
	if len(got) != len(want) {
		t.Fatalf("FreshStatsDue 应命中 %v，实际 %v", want, got)
	}
	for _, b := range got {
		if !want[b] {
			t.Errorf("不应命中 %s，实际 %v", b, got)
		}
	}
}
