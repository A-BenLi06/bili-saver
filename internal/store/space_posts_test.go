package store

// v10 UP主动态/图文归档：upsert 幂等、消失标记与防刷库防御。

import (
	"fmt"
	"testing"
)

func mkPost(mid int64, id string) *SpacePost {
	return &SpacePost{
		Mid: mid, PostID: id, Dtype: "DYNAMIC_TYPE_WORD", Kind: "dynamic",
		Content: "内容 " + id, Origin: "bilibili", Raw: fmt.Sprintf(`{"id_str":%q}`, id),
		Pubdate: 1700000000, FirstSeenAt: 1700000000, LastSeenAt: 1700000000,
	}
}

func TestSpacePostUpsertAndList(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"d1", "d2"} {
		if err := s.UpsertSpacePost(mkPost(42, id)); err != nil {
			t.Fatal(err)
		}
	}
	vis, del, err := s.CountSpacePosts(42, "")
	if err != nil {
		t.Fatal(err)
	}
	if vis != 2 || del != 0 {
		t.Fatalf("visible=%d deleted=%d, want 2/0", vis, del)
	}
	// 二次 upsert：last_seen 更新、first_seen 保持、内容随新
	p := mkPost(42, "d1")
	p.Content = "更新后的内容"
	p.LastSeenAt = 1700000500
	if err := s.UpsertSpacePost(p); err != nil {
		t.Fatal(err)
	}
	posts, err := s.ListSpacePosts(42, "", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range posts {
		if got.PostID == "d1" {
			if got.Content != "更新后的内容" || got.FirstSeenAt != 1700000000 || got.LastSeenAt != 1700000500 {
				t.Errorf("upsert 字段语义错误: %+v", got)
			}
		}
	}
	// kind 过滤
	if err := s.UpsertSpacePost(&SpacePost{Mid: 42, PostID: "a1", Kind: "article", Origin: "bilibili", FirstSeenAt: 1, LastSeenAt: 1}); err != nil {
		t.Fatal(err)
	}
	arts, err := s.ListSpacePosts(42, "article", false, 10, 0)
	if err != nil || len(arts) != 1 || arts[0].PostID != "a1" {
		t.Errorf("kind=article 过滤错误: %v %v", arts, err)
	}
}

func TestMarkSpacePostsGone(t *testing.T) {
	s := openTest(t)
	// 造 10 条
	for i := 0; i < 10; i++ {
		if err := s.UpsertSpacePost(mkPost(7, fmt.Sprintf("d%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// 非全量拉取：不标记
	if n, skipped := s.MarkSpacePostsGone(7, []string{"dynamic"}, map[string]bool{"d0": true}, false); n != 0 || !skipped {
		t.Errorf("非全量应跳过: n=%d skipped=%v", n, skipped)
	}
	// 全量但只见到 3 条（消失 7/10=70%>30% 且 >50 不满足条数 → 仍标记）
	seen := map[string]bool{"d0": true, "d1": true, "d2": true}
	n, skipped := s.MarkSpacePostsGone(7, []string{"dynamic"}, seen, true)
	if skipped || n != 7 {
		t.Errorf("小规模消失应标记: n=%d skipped=%v", n, skipped)
	}
	vis, del, _ := s.CountSpacePosts(7, "")
	if vis != 3 || del != 7 {
		t.Errorf("标记后 visible=%d deleted=%d, want 3/7", vis, del)
	}
	// 大规模消失防御（对齐弹幕阈值：消失 >50 条且占比 >30% → 跳过标记）
	s2 := openTest(t)
	for i := 0; i < 200; i++ {
		if err := s2.UpsertSpacePost(mkPost(8, fmt.Sprintf("x%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	seen2 := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen2[fmt.Sprintf("x%d", i)] = true
	}
	n2, skipped2 := s2.MarkSpacePostsGone(8, []string{"dynamic"}, seen2, true)
	if !skipped2 || n2 != 0 {
		t.Errorf("大面积消失应触发防御跳过: n=%d skipped=%v", n2, skipped2)
	}
	// 阈值边界：消失 41/51（占比 80% 但条数未过 50）→ 按语义标记（与弹幕防御一致）
	s3 := openTest(t)
	for i := 0; i < 51; i++ {
		if err := s3.UpsertSpacePost(mkPost(9, fmt.Sprintf("y%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	seen3 := map[string]bool{}
	for i := 0; i < 10; i++ {
		seen3[fmt.Sprintf("y%d", i)] = true
	}
	n3, skipped3 := s3.MarkSpacePostsGone(9, []string{"dynamic"}, seen3, true)
	if skipped3 || n3 != 41 {
		t.Errorf("41/51 消失应照常标记: n=%d skipped=%v", n3, skipped3)
	}
	// 消失后重现：upsert 清除 deleted_at
	if err := s.UpsertSpacePost(mkPost(7, "d5")); err != nil {
		t.Fatal(err)
	}
	vis2, _, _ := s.CountSpacePosts(7, "")
	if vis2 != 4 {
		t.Errorf("重现应恢复可见: visible=%d, want 4", vis2)
	}
}
