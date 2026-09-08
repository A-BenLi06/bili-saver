package store

import "testing"

// 评论全文搜索端到端：UpsertComment 自动维护 FTS 索引 → SearchCommentsFull 联表命中。
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
	hits, err = s.SearchCommentsFull("感谢", "", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("重复 upsert 后应仍命中 1 条: %v %v", hits, err)
	}
}
