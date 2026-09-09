package card

// 并发回归：多个 goroutine 经 faceCache 拿到同一 face 并发绘制，
// lockedFace 必须串行化 glyph 光栅化（2026-09-09 服务崩溃根因回归测试）。

import (
	"image/color"
	"sync"
	"testing"
)

func TestConcurrentDrawSameFace(t *testing.T) {
	c := newCanvas(1200, 700, newFaceCache(), newIconCache())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				c.drawText("炸厨房选手的人生大菜！用小孩技能做大菜震撼朋友", g*20+i, 100+i, weightRegular, 24, colorWhite)
				c.drawTextCenter("MidTitle中文字体Measure", 600, 300+i, weightBold, 32, color.RGBA{0, 0, 0, 255})
			}
		}(g)
	}
	wg.Wait()
	if c.Bounds().Empty() {
		t.Fatal("画布不应为空")
	}
}
