package bilicats

// 映射表完整性抽查：库内已观测 tid 的名称必须与排行接口实测一致（2026-09-09）。

import "testing"

func TestKnownCategories(t *testing.T) {
	cases := map[int]string{
		21:  "日常",
		65:  "网络游戏",
		76:  "美食制作",
		86:  "特摄",
		171: "电子竞技",
		172: "手机游戏",
		173: "桌游棋牌",
		183: "影视剪辑",
		184: "预告·资讯",
		193: "MV",
		212: "美食侦探",
		218: "喵星人",
		221: "野生动物",
		228: "人文历史",
		249: "足球",
	}
	for tid, want := range cases {
		if got := Name(tid); got != want {
			t.Errorf("Name(%d) = %q, want %q", tid, got, want)
		}
	}
	if got := Name(152); got != "" {
		t.Errorf("未收录 tid=152 应返回空串: %q", got)
	}
	// 一级分区抽查
	if got := Primary(76); got != "美食" {
		t.Errorf("Primary(76) = %q, want 美食", got)
	}
	if got := Primary(171); got != "游戏" {
		t.Errorf("Primary(171) = %q, want 游戏", got)
	}
	// Names 候选去空去重
	if names := Names(76); len(names) != 2 || names[0] != "美食制作" || names[1] != "美食" {
		t.Errorf("Names(76) = %v, want [美食制作 美食]", names)
	}
	if names := Names(99999); len(names) != 0 {
		t.Errorf("未知 tid Names 应为空: %v", names)
	}
}
