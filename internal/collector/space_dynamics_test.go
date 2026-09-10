package collector

// UP主源动态/图文归档（v10）单元测试。

import (
	"testing"

	"bili-saver/internal/biliapi"
)

func TestParseSpaceScope(t *testing.T) {
	// 空/未设置 = 全部
	if sc := ParseSpaceScope(""); !sc.Video || !sc.Dynamic || !sc.DynamicVideo || !sc.Article {
		t.Error("空 scope 应全部开启")
	}
	if sc := ParseSpaceScope("all"); !sc.Video || !sc.Article {
		t.Error("all 应全部开启")
	}
	// none = 全关
	if sc := ParseSpaceScope("none"); sc.Video || sc.Dynamic || sc.DynamicVideo || sc.Article {
		t.Error("none 应全部关闭")
	}
	// 子集
	if sc := ParseSpaceScope("video"); !sc.Video || sc.Dynamic || sc.DynamicVideo || sc.Article {
		t.Errorf("仅 video: %+v", sc)
	}
	if sc := ParseSpaceScope("dynamic,article"); !sc.Dynamic || !sc.Article || sc.Video || sc.DynamicVideo {
		t.Errorf("dynamic,article: %+v", sc)
	}
	// 未知 token 忽略
	if sc := ParseSpaceScope("video,bogus"); !sc.Video || sc.Dynamic {
		t.Errorf("未知 token 应忽略: %+v", sc)
	}
}

func TestValidSpaceScope(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"all", "all", true},
		{"none", "none", true},
		{"video", "video", true},
		{" dynamic , article ", "dynamic,article", true},
		{"video,dynamic,dynamic_video,article", "all", true}, // 全选归一化为 all
		{"video,bogus", "", false},
		{"all,video", "", false}, // all 不能与其他混用
	}
	for _, c := range cases {
		got, ok := ValidSpaceScope(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ValidSpaceScope(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSpacePostOf(t *testing.T) {
	now := int64(1700000000)

	// 视频动态：提取 bvid/标题
	av := biliapi.DynFeedItem{ID: "dyn1", Type: "DYNAMIC_TYPE_AV", Raw: "{}"}
	av.Modules.ModuleDynamic.Major.Archive = &struct {
		Bvid  string `json:"bvid"`
		Title string `json:"title"`
		Desc  string `json:"desc"`
	}{Bvid: "BV1abc", Title: "视频标题", Desc: "视频简介"}
	p := spacePostOf(42, av, now)
	if p == nil || p.Kind != "dynamic" || p.Bvid != "BV1abc" || p.Title != "视频标题" {
		t.Errorf("AV 动态解析错误: %+v", p)
	}

	// 纯文字动态
	word := biliapi.DynFeedItem{ID: "dyn2", Type: "DYNAMIC_TYPE_WORD", Raw: "{}"}
	word.Modules.ModuleDynamic.Content.Text = "一条文字动态"
	p = spacePostOf(42, word, now)
	if p == nil || p.Bvid != "" || p.Content != "一条文字动态" {
		t.Errorf("WORD 动态解析错误: %+v", p)
	}

	// 专栏文章（ARTICLE major）
	art := biliapi.DynFeedItem{ID: "dyn3", Type: "DYNAMIC_TYPE_ARTICLE", Raw: "{}"}
	art.Modules.ModuleDynamic.Major.Article = &struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Desc  string `json:"desc"`
	}{ID: 99, Title: "专栏标题", Desc: "专栏摘要"}
	p = spacePostOf(42, art, now)
	if p == nil || p.Kind != "article" || p.Title != "专栏标题" {
		t.Errorf("ARTICLE 动态解析错误: %+v", p)
	}

	// 图片动态
	draw := biliapi.DynFeedItem{ID: "dyn4", Type: "DYNAMIC_TYPE_DRAW", Raw: "{}"}
	draw.Modules.ModuleDynamic.Major.Draw = &struct {
		Title string `json:"title"`
		Items []struct {
			Src string `json:"src"`
		} `json:"items"`
	}{Title: "图文", Items: []struct {
		Src string `json:"src"`
	}{{Src: "a.jpg"}, {Src: "b.jpg"}}}
	p = spacePostOf(42, draw, now)
	if p == nil || p.Kind != "dynamic" || p.Pics != 2 {
		t.Errorf("DRAW 动态解析错误: %+v", p)
	}

	// 无 id 的条目跳过
	if spacePostOf(42, biliapi.DynFeedItem{}, now) != nil {
		t.Error("空 id 应返回 nil")
	}
}
