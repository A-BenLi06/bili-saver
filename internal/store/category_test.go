package store

// v9 分区列与媒体库 categories 路由测试。

import (
	"testing"
)

func TestMatchRulesCategories(t *testing.T) {
	lib := &MediaLibrary{MatchRules: `{"categories":["美食","动画"]}`}
	food := &Video{Tname: "美食"}
	film := &Video{Tname: "电影"}
	verticalFood := &Video{Tname: "美食", Orientation: "vertical"}

	if !lib.Matches(food, nil) {
		t.Error("分区命中美食应匹配")
	}
	if !lib.Matches(verticalFood, nil) {
		t.Error("分区命中美食（竖屏）应匹配")
	}
	if lib.Matches(film, nil) {
		t.Error("分区电影不应匹配")
	}
	if lib.Matches(&Video{Tid: 211}, nil) {
		t.Error("仅有 tid 无 tname 不应按分区匹配（categories 按名称）")
	}

	// 空规则 = 默认库全匹配
	defaultLib := &MediaLibrary{}
	if !defaultLib.Matches(film, nil) {
		t.Error("空规则默认库应全匹配")
	}

	// 标签与分区是"或"关系：分区不中但标签中即匹配
	tagLib := &MediaLibrary{MatchRules: `{"tags":["纪录片"],"categories":["美食"]}`}
	if !tagLib.Matches(film, []string{"纪录片"}) {
		t.Error("分区未中但标签命中应匹配")
	}
	if tagLib.Matches(film, nil) {
		t.Error("分区与标签均未中不应匹配")
	}
}

func TestVideoCategoryRoundTrip(t *testing.T) {
	s := openTest(t)
	v := &Video{
		Bvid: "BV1CatTest1", Title: "分区测试", Status: "ok",
		FirstArchivedAt: Now(), Pubdate: Now(),
		Tid: 211, Tname: "美食", Orientation: "vertical",
	}
	if err := s.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVideo("BV1CatTest1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tid != 211 || got.Tname != "美食" || got.Orientation != "vertical" {
		t.Errorf("round-trip 不一致: tid=%d tname=%q orientation=%q", got.Tid, got.Tname, got.Orientation)
	}
	// 二次 upsert（更新路径）：分区变化应随最新值
	v2 := *got
	v2.Tid, v2.Tname, v2.Orientation = 182, "影视杂谈", "horizontal"
	if err := s.UpsertVideo(&v2); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetVideo("BV1CatTest1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Tid != 182 || got2.Tname != "影视杂谈" || got2.Orientation != "horizontal" {
		t.Errorf("更新路径分区应随最新值: %+v", got2)
	}
}
