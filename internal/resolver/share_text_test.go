package resolver

// 分享文本兼容（【标题】 链接 形态）测试。

import "testing"

func TestParseShareText(t *testing.T) {
	// 用户实际遇到的分享文本：标题前缀 + 完整链接 + 追踪参数
	share := "【【4K高清·星光2.5修复】罗大佑 - 首尔 [LD-Decode] [4K画质修复] [无损音轨]】 https://www.bilibili.com/video/BV1Yjaz62E6w/?share_source=copy_web&vd_source=3881f02a57e88b1f1da69c226f10dd08"
	in, err := Parse(share)
	if err != nil {
		t.Fatalf("分享文本应识别成功: %v", err)
	}
	if in.Type != TypeVideo || in.ExtID != "BV1Yjaz62E6w" {
		t.Errorf("分享文本识别错误: %+v", in)
	}

	cases := []struct {
		name string
		in   string
		want Type
		ext  string
	}{
		{"标题+链接（无参数）", "罗大佑 - 首尔【4K修复】 https://www.bilibili.com/video/BV1abcdeFgHi", TypeVideo, "BV1abcdeFgHi"},
		{"链接在前标题在后", "https://www.bilibili.com/video/BV1abcdeFgHi 罗大佑 - 首尔，强烈推荐", TypeVideo, "BV1abcdeFgHi"},
		{"标题+BV号（无链接）", "安利一个视频 BV1Yjaz62E6w", TypeVideo, "BV1Yjaz62E6w"},
		{"标题+av号", "考古 av170001", TypeVideo, "av170001"},
	}
	for _, c := range cases {
		in, err := Parse(c.in)
		if err != nil {
			t.Errorf("%s: 解析失败: %v", c.name, err)
			continue
		}
		if in.Type != c.want || in.ExtID != c.ext {
			t.Errorf("%s: got %+v, want %s %s", c.name, in, c.want, c.ext)
		}
	}

	// b23 短链分享文本：提取在 Normalize（负责展开）与 Parse 均生效；
	// Parse 层不展开短链（需网络），此处直接断言提取结果。
	if got := extractShareRef("【标题】 https://b23.tv/jxKmLd9"); got != "https://b23.tv/jxKmLd9" {
		t.Errorf("b23 分享文本提取错误: %q", got)
	}

	// 回归：纯净输入不受提取影响
	pures := map[string]Type{
		"BV1Yjaz62E6w": TypeVideo,
		"av170001":     TypeVideo,
		"https://www.bilibili.com/video/BV1Yjaz62E6w/?share_source=copy_web": TypeVideo,
		"https://space.bilibili.com/579909":                                  TypeSpace,
		"579909":                                                             TypeSpace,
	}
	for in, want := range pures {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("纯净输入 %q 不应失败: %v", in, err)
			continue
		}
		if got.Type != want {
			t.Errorf("纯净输入 %q: got %s, want %s", in, got.Type, want)
		}
	}

	// 纯文本无链接 → 仍报原文错误（而非吞掉）
	if _, err := Parse("罗大佑 - 首尔 很好听"); err == nil {
		t.Error("无链接纯文本应保持报错")
	}
}
