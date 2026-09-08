package store

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCommentLifecycle(t *testing.T) {
	s := openTest(t)
	const t0 = int64(1000)
	const t1 = int64(2000)
	// 首次归档：两条评论
	for _, id := range []int64{1, 2} {
		if err := s.UpsertComment(&Comment{
			Rpid: id, Bvid: "BV1xx", Content: "评论", FirstSeenAt: t0, LastSeenAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	vis, del, err := s.CountCommentsByStatus("BV1xx")
	if err != nil || vis != 2 || del != 0 {
		t.Fatalf("初始计数错误: %d %d %v", vis, del, err)
	}

	// 第二次同步（t1）：只见到 rpid=1
	if err := s.UpsertComment(&Comment{Rpid: 1, Bvid: "BV1xx", Content: "评论", FirstSeenAt: t0, LastSeenAt: t1}); err != nil {
		t.Fatal(err)
	}
	n, err := s.MarkMissingComments("BV1xx", t1, false)
	if err != nil || n != 1 {
		t.Fatalf("应标记 1 条消失: %d %v", n, err)
	}
	vis, del, _ = s.CountCommentsByStatus("BV1xx")
	if vis != 1 || del != 1 {
		t.Fatalf("对账后计数错误: vis=%d del=%d", vis, del)
	}
	// 消失的评论数据仍在
	c, err := func() (*Comment, error) {
		return scanComment(s.DB.QueryRow(`SELECT ` + commentCols + ` FROM comments WHERE rpid=2`))
	}()
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "deleted" || c.Content != "评论" || c.DeletedAt == 0 {
		t.Fatalf("消失评论应保留正文并带 deleted_at: %+v", c)
	}
	// 它再次出现 → 恢复 visible
	if err := s.UpsertComment(&Comment{Rpid: 2, Bvid: "BV1xx", Content: "评论", FirstSeenAt: t0, LastSeenAt: t1 + 10}); err != nil {
		t.Fatal(err)
	}
	vis, del, _ = s.CountCommentsByStatus("BV1xx")
	if vis != 2 || del != 0 {
		t.Fatalf("复活后计数错误: vis=%d del=%d", vis, del)
	}
}

func TestDanmakuLifecycle(t *testing.T) {
	s := openTest(t)
	const t0 = int64(1000)
	const t1 = int64(2000)
	for _, id := range []int64{10, 11} {
		if err := s.UpsertDanmaku(&Danmaku{Dmid: id, Bvid: "BV1xx", Cid: 7, Content: "d", FirstSeenAt: t0, LastSeenAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertDanmaku(&Danmaku{Dmid: 10, Bvid: "BV1xx", Cid: 7, Content: "d", FirstSeenAt: t0, LastSeenAt: t1}); err != nil {
		t.Fatal(err)
	}
	n, err := s.MarkMissingDanmaku(7, t1, false)
	if err != nil || n != 1 {
		t.Fatalf("弹幕消失标记错误: %d %v", n, err)
	}
	if rows, _ := s.ListDanmaku(7, "visible"); len(rows) != 1 {
		t.Fatalf("可见弹幕应为 1: %d", len(rows))
	}
}

func TestVideoTagsOriginSeparation(t *testing.T) {
	s := openTest(t)
	if err := s.AddTags("BV1xx", []string{"搞笑", "教程"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTags("BV1xx", []string{"搞笑", "AI生成"}, "plugin:ai-tagger"); err != nil {
		t.Fatal(err)
	}
	tags, err := s.VideoTags("BV1xx")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags["bilibili"]) != 2 || len(tags["plugin:ai-tagger"]) != 2 {
		t.Fatalf("标签按 origin 分组错误: %+v", tags)
	}
}

func TestStatsHistory(t *testing.T) {
	s := openTest(t)
	if err := s.AppendStats(&StatsSnapshot{Bvid: "BV1xx", Ts: 1, View: 100, Like: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStats(&StatsSnapshot{Bvid: "BV1xx", Ts: 2, View: 200, Like: 20}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.ListStats("BV1xx")
	if err != nil || len(hist) != 2 || hist[1].View != 200 {
		t.Fatalf("统计时间序列错误: %+v %v", hist, err)
	}
}

func TestFTSSearch(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BV1xx", Title: "原神 稻妻 攻略", FirstArchivedAt: Now()}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetVideo("BV1xx")
	if err := s.ReindexVideoFTS(v); err != nil {
		t.Fatal(err)
	}
	ids, err := s.SearchVideosFTS("稻妻", 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("FTS 搜索失败: %v %v", ids, err)
	}
}

func TestSourceRoundTrip(t *testing.T) {
	s := openTest(t)
	src := &Source{Type: "video", ExtID: "BV1xx", Title: "t", Watch: true, CreatedAt: Now()}
	id, err := s.UpsertSource(src)
	if err != nil || id == 0 {
		t.Fatalf("UpsertSource 应返回真实 id: %d %v", id, err)
	}
	got, err := s.GetSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Watch {
		t.Fatal("watch=true 库取后应保持 true（回归：scanSource 覆盖）")
	}
	// 再次 upsert（冲突更新）返回同一 id 且更新 watch=false
	src.Watch = false
	id2, err := s.UpsertSource(src)
	if err != nil || id2 != id {
		t.Fatalf("冲突更新应返回同一 id: %d vs %d %v", id2, id, err)
	}
	if got, _ = s.GetSource(id); got.Watch {
		t.Fatal("watch=false 应已更新")
	}
	// 到期源应包含 watch=true 的
	src.Watch = true
	if _, err := s.UpsertSource(src); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueSources(360, Now()+7200)
	if err != nil || len(due) == 0 {
		t.Fatalf("到期源不应为空: %v %d", err, len(due))
	}
}

func TestSourceSyncPolicy(t *testing.T) {
	s := openTest(t)
	src := &Source{Type: "favlist", ExtID: "1", Watch: true, SyncMode: "once",
		IntervalCommentsMin: -1, IntervalDanmakuMin: 120, IntervalSubsMin: 0, CreatedAt: Now()}
	id, err := s.UpsertSource(src)
	if err != nil || id == 0 {
		t.Fatalf("upsert: %d %v", id, err)
	}
	got, _ := s.GetSource(id)
	if got.SyncMode != "once" || got.IntervalCommentsMin != -1 || got.IntervalDanmakuMin != 120 {
		t.Fatalf("同步策略字段往返失败: %+v", got)
	}

	// once + 已同步 → 不再到期
	_ = s.SetSourceSynced(id, Now())
	due, err := s.DueSources(360, Now()+1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.ID == id {
			t.Fatal("once 模式完成后不应再到期")
		}
	}

	// once + 未同步 → 到期
	src2 := &Source{Type: "space", ExtID: "2", Watch: true, SyncMode: "once", CreatedAt: Now()}
	id2, _ := s.UpsertSource(src2)
	due, _ = s.DueSources(360, Now()+1<<20)
	found := false
	for _, d := range due {
		if d.ID == id2 {
			found = true
		}
	}
	if !found {
		t.Fatal("once 未同步源应到期（首次触发）")
	}
}

func TestVideoSyncTimes(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BV1xx", FirstArchivedAt: Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVideoSyncTimes("BV1xx", true, true, false, false, 500); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetVideo("BV1xx")
	if v.LastStatsAt != 500 || v.LastCommentsAt != 500 || v.LastDanmakuAt != 0 || v.LastSubsAt != 0 {
		t.Fatalf("分类型时间戳错误: %+v", v)
	}
}

func TestGuardedCommentDisappearance(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BV1g", FirstArchivedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	const t0 = int64(1000)
	const t1 = int64(2000)
	// 首次归档 100 条
	for i := int64(1); i <= 100; i++ {
		if err := s.UpsertComment(&Comment{Rpid: i, Bvid: "BV1g", Content: "c", FirstSeenAt: t0, LastSeenAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	// 本轮"接口截断"：只见到 10 条（90 条将消失 → 异常）
	for i := int64(1); i <= 10; i++ {
		if err := s.UpsertComment(&Comment{Rpid: i, Bvid: "BV1g", Content: "c", FirstSeenAt: t0, LastSeenAt: t1}); err != nil {
			t.Fatal(err)
		}
	}
	marked, anomalous, err := s.MarkMissingCommentsGuarded("BV1g", t1, false, 10)
	if err != nil || marked != 0 || !anomalous {
		t.Fatalf("首轮异常应拦截: marked=%d anomalous=%v err=%v", marked, anomalous, err)
	}
	vis, del, _ := s.CountCommentsByStatus("BV1g")
	if del != 0 || vis != 100 {
		t.Fatalf("拦截后数据不应变化: vis=%d del=%d", vis, del)
	}

	// 第二轮仍然只见 10 条（连续异常）→ 确认标删
	for i := int64(1); i <= 10; i++ {
		if err := s.UpsertComment(&Comment{Rpid: i, Bvid: "BV1g", Content: "c", FirstSeenAt: t0, LastSeenAt: t1 + 5}); err != nil {
			t.Fatal(err)
		}
	}
	marked, anomalous, err = s.MarkMissingCommentsGuarded("BV1g", t1+5, false, 10)
	if err != nil || marked != 90 || anomalous {
		t.Fatalf("连续两轮异常应确认标删: marked=%d anomalous=%v err=%v", marked, anomalous, err)
	}

	// 正常小规模消失（接口正常）：直接标记，无需确认
	// 确认标删后可见仅剩 1..10；本轮只见 1..8（9、10 未出现）
	for i := int64(1); i <= 8; i++ {
		if err := s.UpsertComment(&Comment{Rpid: i, Bvid: "BV1g", Content: "c", FirstSeenAt: t0, LastSeenAt: t1 + 10}); err != nil {
			t.Fatal(err)
		}
	}
	marked, anomalous, err = s.MarkMissingCommentsGuarded("BV1g", t1+10, false, 8)
	if err != nil || marked != 2 || anomalous {
		t.Fatalf("正常消失应直接标记 2 条: marked=%d anomalous=%v err=%v", marked, anomalous, err)
	}
}

func TestGuardedDanmakuDowngrade(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BV1g", FirstArchivedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	const t0, t1 = int64(1000), int64(2000)
	// 库内 2000 条（seg.so 时代抓的）
	for i := int64(1); i <= 2000; i++ {
		if err := s.UpsertDanmaku(&Danmaku{Dmid: i, Bvid: "BV1g", Cid: 9, FirstSeenAt: t0, LastSeenAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	// 本轮回退 list.so 只见到 1200 条 → 800 将"消失"
	for i := int64(1); i <= 1200; i++ {
		if err := s.UpsertDanmaku(&Danmaku{Dmid: i, Bvid: "BV1g", Cid: 9, FirstSeenAt: t0, LastSeenAt: t1}); err != nil {
			t.Fatal(err)
		}
	}
	marked, anomalous, err := s.MarkMissingDanmakuGuarded("BV1g", 9, t1, false, 1200)
	if err != nil || marked != 0 || !anomalous {
		t.Fatalf("来源降级首轮应拦截: marked=%d anomalous=%v err=%v", marked, anomalous, err)
	}
	if rows, _ := s.ListDanmaku(9, "visible"); len(rows) != 2000 {
		t.Fatalf("拦截后弹幕不应变化: %d", len(rows))
	}
	// 第二轮仍降级 → 确认标删
	for i := int64(1); i <= 1200; i++ {
		if err := s.UpsertDanmaku(&Danmaku{Dmid: i, Bvid: "BV1g", Cid: 9, FirstSeenAt: t0, LastSeenAt: t1 + 5}); err != nil {
			t.Fatal(err)
		}
	}
	marked, anomalous, err = s.MarkMissingDanmakuGuarded("BV1g", 9, t1+5, false, 1200)
	if err != nil || marked != 800 || anomalous {
		t.Fatalf("连续两轮降级应确认标删: marked=%d anomalous=%v err=%v", marked, anomalous, err)
	}
}

func TestMediaLibraryCRUDAndRules(t *testing.T) {
	s := openTest(t)
	l := &MediaLibrary{Name: "纪录片", RootPath: "/data/lib/docu", Layout: "show",
		LinkMode: "hardlink", MetadataFormat: "json", Enabled: true, CreatedAt: Now()}
	id, err := s.UpsertMediaLibrary(l)
	if err != nil || id == 0 {
		t.Fatalf("upsert: %d %v", id, err)
	}
	got, _ := s.GetMediaLibrary(id)
	if got.RootPath != "/data/lib/docu" || got.MetadataFormat != "json" {
		t.Fatalf("库配置往返失败: %+v", got)
	}
	// 规则：tag 路由
	l2 := &MediaLibrary{Name: "美食库", RootPath: "/data/lib/food", MatchRules: `{"tags":["美食"]}`, Enabled: true, CreatedAt: Now()}
	if _, err := s.UpsertMediaLibrary(l2); err != nil {
		t.Fatal(err)
	}
	v := &Video{Bvid: "BV1f", OwnerMid: 7}
	if !l2.Matches(v, []string{"美食", "教程"}) {
		t.Fatal("tag 命中应匹配")
	}
	if l2.Matches(v, []string{"游戏"}) {
		t.Fatal("tag 未命中不应匹配")
	}
	// 默认库（空规则）匹配一切
	l3 := &MediaLibrary{Name: "默认", RootPath: "/x"}
	if !l3.Matches(v, nil) {
		t.Fatal("空规则应匹配所有视频")
	}
	// 列表与删除
	libs, _ := s.ListMediaLibraries()
	if len(libs) != 2 {
		t.Fatalf("应有 2 个库: %d", len(libs))
	}
	if err := s.DeleteMediaLibrary(id); err != nil {
		t.Fatal(err)
	}
	libs, _ = s.ListMediaLibraries()
	if len(libs) != 1 {
		t.Fatalf("删除后应剩 1: %d", len(libs))
	}
}

func TestTrackLifecycle(t *testing.T) {
	s := openTest(t)
	now := Now()
	if err := s.TrackAdd("BV1Track0001", 7, now); err != nil {
		t.Fatal(err)
	}
	// bvid 幂等：重复添加不覆盖、不报错
	if err := s.TrackAdd("BV1Track0001", 9, now+5); err != nil {
		t.Fatal(err)
	}
	tracks, err := s.ListTracks(0)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("应有 1 条记录: %d %v", len(tracks), err)
	}
	if tracks[0].SourceID != 7 || tracks[0].State != "queued" {
		t.Fatalf("首条记录字段错误: %+v", tracks[0])
	}
	// 状态回写
	if err := s.TrackSetState("BV1Track0001", "done", "", Now()); err != nil {
		t.Fatal(err)
	}
	tracks, _ = s.ListTracks(0)
	if tracks[0].State != "done" {
		t.Fatalf("回写后应为 done: %+v", tracks[0])
	}
	// 失败态带错误信息 + 不存在的 bvid 是无害空操作
	if err := s.TrackSetState("BV1Track0002", "failed", "playurl boom", Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackSetState("BV1Track0001", "failed", "boom", Now()); err != nil {
		t.Fatal(err)
	}
	tracks, _ = s.ListTracks(0)
	if tracks[0].State != "failed" || tracks[0].Error != "boom" {
		t.Fatalf("失败回写错误: %+v", tracks[0])
	}
	// 联表展示字段（视频存在时）
	if err := s.UpsertVideo(&Video{Bvid: "BV1Track0001", Title: "跟踪的视频", OwnerName: "UP", OwnerMid: 5, FirstArchivedAt: now}); err != nil {
		t.Fatal(err)
	}
	tracks, _ = s.ListTracks(0)
	if tracks[0].Title != "跟踪的视频" || tracks[0].OwnerName != "UP" || tracks[0].Downloaded != 0 {
		t.Fatalf("联表字段错误: %+v", tracks[0])
	}
}
