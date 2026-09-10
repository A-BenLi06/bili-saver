package store

import "testing"

// 评论全文搜索端到端：UpsertComment 落库 + HealCommentsFTS 批量补索引 →
// SearchCommentsFull 联表命中。（2026-09-11 起 FTS 维护从 UpsertComment 移出：
// 逐条存在性检查是 FTS5 全表扫描、平方级开销，改为采集轮末尾一次批量补齐。）
func TestCommentSearchFull(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertVideo(&Video{Bvid: "BV1zz", Title: "美食教程 红烧肉", OwnerName: "老王", FirstArchivedAt: Now()}); err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"跟着做了一遍，感谢教程", "红烧肉看起来真好吃"} {
		if err := s.UpsertComment(&Comment{
			Rpid: int64(100 + i), Bvid: "BV1zz", Content: content,
			MemberName: "观众", FirstSeenAt: Now(), LastSeenAt: Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.HealCommentsFTS(); err != nil || n != 2 {
		t.Fatalf("补索引应新增 2 行: n=%d err=%v", n, err)
	}
	hits, err := s.SearchCommentsFull("感谢", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Content != "跟着做了一遍，感谢教程" {
		t.Fatalf("期望命中 1 条，got %v", hits)
	}
	if hits[0].VideoTitle != "美食教程 红烧肉" || hits[0].OwnerName != "老王" || hits[0].Bvid != "BV1zz" {
		t.Fatalf("联表字段缺失: %+v", hits[0])
	}
	// 状态过滤
	hits, err = s.SearchCommentsFull("感谢", "visible", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("visible 过滤失败: %v %v", hits, err)
	}
	hits, err = s.SearchCommentsFull("感谢", "deleted", 0)
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted 过滤失败: %v %v", hits, err)
	}
	// 幂等：重复 upsert 不产生重复索引行（导致重复命中）
	if err := s.UpsertComment(&Comment{
		Rpid: 100, Bvid: "BV1zz", Content: "跟着做了一遍，感谢教程",
		MemberName: "观众", FirstSeenAt: Now(), LastSeenAt: Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.HealCommentsFTS(); err != nil || n != 0 {
		t.Fatalf("重复 upsert 后补索引应幂等新增 0 行: n=%d err=%v", n, err)
	}
	hits, err = s.SearchCommentsFull("感谢", "", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("重复 upsert 后应仍命中 1 条: %v %v", hits, err)
	}
}

// 批量落库语义与单条一致：upsert 幂等（重写同 rpid 只更新计数/seen，
// 不动 first_seen_at/content），失败时可整批回滚由事务保证。
func TestUpsertCommentsBatch(t *testing.T) {
	s := openTest(t)
	t0 := Now()
	batch := []*Comment{
		{Rpid: 1, Bvid: "BV1b", Content: "一", FirstSeenAt: t0, LastSeenAt: t0},
		{Rpid: 2, Bvid: "BV1b", Content: "二", FirstSeenAt: t0, LastSeenAt: t0},
		{Rpid: 3, Bvid: "BV1b", Content: "三", FirstSeenAt: t0, LastSeenAt: t0},
	}
	if err := s.UpsertCommentsBatch(batch); err != nil {
		t.Fatal(err)
	}
	// 空批无害
	if err := s.UpsertCommentsBatch(nil); err != nil {
		t.Fatal(err)
	}
	// 重写同 rpid：content/first_seen 保留，计数与 last_seen 更新、复活 deleted
	batch[0].LikeCount = 9
	batch[0].Content = "一改"
	batch[0].LastSeenAt = t0 + 10
	if err := s.UpsertCommentsBatch(batch[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListComments("BV1b", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条评论: %d", len(got))
	}
	var c1 *Comment
	for _, c := range got {
		if c.Rpid == 1 {
			c1 = c
		}
	}
	if c1.Content != "一" || c1.LikeCount != 9 || c1.LastSeenAt != t0+10 || c1.FirstSeenAt != t0 {
		t.Fatalf("upsert 语义不符: %+v", c1)
	}
}

// 弹幕批量落库：与单条同语义（重写同 dmid 复活并前移 last_seen）。
func TestUpsertDanmakuBatch(t *testing.T) {
	s := openTest(t)
	t0 := Now()
	if err := s.UpsertDanmakuBatch([]*Danmaku{
		{Dmid: 1, Bvid: "BV1d", Cid: 9, Content: "a", FirstSeenAt: t0, LastSeenAt: t0},
		{Dmid: 2, Bvid: "BV1d", Cid: 9, Content: "b", FirstSeenAt: t0, LastSeenAt: t0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDanmakuBatch([]*Danmaku{{Dmid: 1, Bvid: "BV1d", Cid: 9, Content: "a", FirstSeenAt: t0, LastSeenAt: t0 + 5}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListDanmaku(9, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("应有 2 条弹幕: %v %v", rows, err)
	}
	if rows[0].Dmid == 1 && rows[0].LastSeenAt != t0+5 {
		t.Fatalf("last_seen 未更新: %+v", rows[0])
	}
}
