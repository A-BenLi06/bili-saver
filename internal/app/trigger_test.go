package app

// 新视频触发器三态解析测试。

import "testing"

func TestTriggerOn(t *testing.T) {
	cases := []struct {
		srcAuto int
		global  bool
		want    bool
	}{
		{1, false, true},  // 源强制开：全局关也下载
		{-1, true, false}, // 源强制关：全局开也不下载
		{0, true, true},   // 跟随全局开
		{0, false, false}, // 跟随全局关
		{2, false, false}, // 未知值按跟随处理
	}
	for _, c := range cases {
		if got := triggerOn(c.srcAuto, c.global); got != c.want {
			t.Errorf("triggerOn(%d, %v) = %v, want %v", c.srcAuto, c.global, got, c.want)
		}
	}
}
