package store

import (
	"errors"
	"testing"
)

// 读路径 DAO 回归：主楼分页/楼中楼查询/LatestStats/TopTags/CountFavItems。
// 对应读路径优化（2026-09-11）：评论区翻页不再整视频全量加载、
// 卡片渲染不再为取末值加载全量历史、热搜不再全库 N+1。

func insertCommentForTest(t *testing.T, s *Store, rpid int64, bvid string, root, like, ctime int64) {
	t.Helper()
	if err := s.UpsertComment(&Comment{
		Rpid: rpid, Bvid: bvid, RootRpid: root, Content: "c",
		LikeCount: int(like), FirstSeenAt: 1, LastSeenAt: 1,
		Ctime: ctime,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestListMainCommentsPage(t *testing.T) {
	s := openTest(t)
	// m1 可见 5 赞 / m2 可见 10 赞 / m3 已消失 99 赞；另有楼中楼不计入主楼
	insertCommentForTest(t, s, 1, "BV1p", 0, 5, 100)
	insertCommentForTest(t, s, 2, "BV1p", 0, 10, 200)
	insertCommentForTest(t, s, 3, "BV1p", 0, 99, 300)
	insertCommentForTest(t, s, 11, "BV1p", 1, 0, 150)
	if _, err := s.DB.Exec(`UPDATE comments SET status='deleted', deleted_at=1 WHERE rpid=3`); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountMainComments("BV1p")
	if err != nil || n != 3 {
		t.Fatalf("主楼数应为 3: n=%d err=%v", n, err)
	}

	// 最热：现存优先于已消失（99 赞的 m3 殿后），可见按赞降序
	page, err := s.ListMainCommentsPage("BV1p", "hot", 2, 0)
	if err != nil || len(page) != 2 || page[0].Rpid != 2 || page[1].Rpid != 1 {
		t.Fatalf("hot 第一页应为 [2,1]: %v %v", page, err)
	}
	page, err = s.ListMainCommentsPage("BV1p", "hot", 2, 2)
	if err != nil || len(page) != 1 || page[0].Rpid != 3 {
		t.Fatalf("hot 第二页应为 [3]: %v %v", page, err)
	}

	// 最新：ctime 降序
	page, err = s.ListMainCommentsPage("BV1p", "new", 10, 0)
	if err != nil || len(page) != 3 || page[0].Rpid != 3 || page[2].Rpid != 1 {
		t.Fatalf("new 应为 [3,2,1]: %v %v", page, err)
	}
}

func TestListSubCommentsAndByRoots(t *testing.T) {
	s := openTest(t)
	insertCommentForTest(t, s, 1, "BV1r", 0, 0, 100) // 主楼 A
	insertCommentForTest(t, s, 2, "BV1r", 0, 0, 100) // 主楼 B
	insertCommentForTest(t, s, 3, "BV1r", 1, 0, 120) // A 的楼中楼
	insertCommentForTest(t, s, 4, "BV1r", 1, 0, 110) // A 的楼中楼（较早）
	insertCommentForTest(t, s, 5, "BV1r", 2, 0, 130) // B 的楼中楼
	// 数据卫生守卫：root 指向自身的行不算楼中楼
	insertCommentForTest(t, s, 7, "BV1r", 7, 0, 140)

	subs, err := s.ListSubComments("BV1r", 1)
	if err != nil || len(subs) != 2 || subs[0].Rpid != 4 || subs[1].Rpid != 3 {
		t.Fatalf("A 楼中楼应为 [4,3]（ctime 升序）: %v %v", subs, err)
	}

	groups, err := s.ListSubCommentsByRoots("BV1r", []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups[1]) != 2 || len(groups[2]) != 1 {
		t.Fatalf("分组错误: %+v", groups)
	}
	if len(groups) != 2 {
		t.Fatalf("self-root 行不应出现在任何分组: %+v", groups)
	}
}

func TestLatestStats(t *testing.T) {
	s := openTest(t)
	if _, err := s.LatestStats("BV1s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("无快照应返回 ErrNotFound: %v", err)
	}
	if err := s.AppendStats(&StatsSnapshot{Bvid: "BV1s", Ts: 100, View: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStats(&StatsSnapshot{Bvid: "BV1s", Ts: 200, View: 9}); err != nil {
		t.Fatal(err)
	}
	// 乱序插入也取 ts 最大者
	if err := s.AppendStats(&StatsSnapshot{Bvid: "BV1s", Ts: 150, View: 5}); err != nil {
		t.Fatal(err)
	}
	last, err := s.LatestStats("BV1s")
	if err != nil || last.Ts != 200 || last.View != 9 {
		t.Fatalf("应取 ts=200 快照: %+v %v", last, err)
	}
}

func TestTopTags(t *testing.T) {
	s := openTest(t)
	if err := s.AddTags("BV1a", []string{"美食", "教程"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTags("BV1b", []string{"美食"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTags("BV1c", []string{"美食", "AI 标签"}, "plugin:test"); err != nil {
		t.Fatal(err)
	}
	tags, err := s.TopTags("bilibili", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "美食" || tags[0].Count != 2 || tags[1].Name != "教程" {
		t.Fatalf("热搜标签应为 [美食×2 教程×1]: %+v", tags)
	}
	// limit 生效
	tags, err = s.TopTags("bilibili", 1)
	if err != nil || len(tags) != 1 {
		t.Fatalf("limit=1 应只返回 1 条: %+v %v", tags, err)
	}
}

func TestCountFavItems(t *testing.T) {
	s := openTest(t)
	fid1, err := s.CreateFavFolder(1, "夹1")
	if err != nil {
		t.Fatal(err)
	}
	fid2, err := s.CreateFavFolder(1, "夹2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddFavItem(fid1, "BV1f", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFavItem(fid2, "BV1f", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFavItem(fid1, "BV1g", 1); err != nil {
		t.Fatal(err)
	}
	if n := s.CountFavItems("BV1f"); n != 2 {
		t.Fatalf("BV1f 应在 2 个收藏夹: %d", n)
	}
	if n := s.CountFavItems("BV1nope"); n != 0 {
		t.Fatalf("未收藏应为 0: %d", n)
	}
}
