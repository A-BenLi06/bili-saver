package metrics

import (
	"testing"
	"time"
)

func TestRateWindowAndDecay(t *testing.T) {
	r := &Rate{}
	r.Add(3000)                      // 当前秒桶
	if got := r.BPS(); got != 1000 { // 3s 窗口平均：3000/3
		t.Fatalf("期望平均 1000 B/s，得到 %d", got)
	}
	r.Add(3000)
	if got := r.BPS(); got != 2000 { // 6000/3
		t.Fatalf("期望平均 2000 B/s，得到 %d", got)
	}
	if r.Total() != 6000 {
		t.Fatalf("累计应为 6000: %d", r.Total())
	}
	// 模拟 6 秒无写入：窗口推进后速率衰减为 0
	r.last = r.last.Add(-7 * time.Second)
	if got := r.BPS(); got != 0 {
		t.Fatalf("无写入 7s 后速率应衰减为 0: %d", got)
	}
	if r.Total() != 6000 {
		t.Fatalf("累计不受衰减影响: %d", r.Total())
	}
}

func TestAddZero(t *testing.T) {
	r := &Rate{}
	r.Add(0)
	r.Add(-5)
	if r.Total() != 0 || r.BPS() != 0 {
		t.Fatal("非正数字节不应计入")
	}
}
