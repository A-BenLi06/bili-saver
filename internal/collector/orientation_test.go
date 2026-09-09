package collector

// 分区/横竖屏采集（v9）的派生与兜底逻辑测试。

import (
	"testing"

	"bili-saver/internal/biliapi"
)

func TestOrientationOf(t *testing.T) {
	cases := []struct {
		name string
		d    biliapi.Dimension
		want string
	}{
		{"横屏", biliapi.Dimension{Width: 1920, Height: 1080, Rotate: 0}, "horizontal"},
		{"竖屏", biliapi.Dimension{Width: 1080, Height: 1920, Rotate: 0}, "vertical"},
		{"正方形按横屏", biliapi.Dimension{Width: 1080, Height: 1080}, "horizontal"},
		{"旋转90互换", biliapi.Dimension{Width: 1920, Height: 1080, Rotate: 90}, "vertical"},
		{"旋转270互换", biliapi.Dimension{Width: 1080, Height: 1920, Rotate: 270}, "horizontal"},
		{"尺寸未知为空", biliapi.Dimension{}, ""},
		{"旋转180不互换", biliapi.Dimension{Width: 1920, Height: 1080, Rotate: 180}, "horizontal"},
	}
	for _, c := range cases {
		if got := orientationOf(c.d); got != c.want {
			t.Errorf("%s: orientationOf(%+v) = %q, want %q", c.name, c.d, got, c.want)
		}
	}
}

func TestPickTname(t *testing.T) {
	if got := pickTname(&biliapi.VideoView{TName: "美食"}); got != "美食" {
		t.Errorf("tname 有值应优先: %q", got)
	}
	if got := pickTname(&biliapi.VideoView{TName: "", TNameV2: "野生动物"}); got != "野生动物" {
		t.Errorf("tname 空应回退 tname_v2: %q", got)
	}
	// 双双为空时回退本地分区表（76=美食制作，2026-09 排行接口实测）
	if got := pickTname(&biliapi.VideoView{TID: 76}); got != "美食制作" {
		t.Errorf("tname/tname_v2 空应回退本地分区表: %q", got)
	}
	if got := pickTname(&biliapi.VideoView{}); got != "" {
		t.Errorf("全空且未知 tid 应为空串: %q", got)
	}
}
