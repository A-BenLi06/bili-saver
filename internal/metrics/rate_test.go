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

func TestRateFractionalAdvances(t *testing.T) {
	start := time.Unix(1000, 0)
	r := &Rate{last: start, total: 3000}
	r.buckets[0] = 3000
	for _, step := range []struct {
		elapsed time.Duration
		bytes   int64
	}{
		{1500 * time.Millisecond, 3000},
		{2500 * time.Millisecond, 3000},
		{3 * time.Second, 0},
	} {
		r.advance(start.Add(step.elapsed))
		var sum int64
		for i := 0; i < 3; i++ {
			sum += r.buckets[(r.idx-i+windowBuckets)%windowBuckets]
		}
		if sum != step.bytes {
			t.Fatalf("at %v: window contains %d bytes, want %d", step.elapsed, sum, step.bytes)
		}
	}
	if r.total != 3000 {
		t.Fatalf("decay changed cumulative total: %d", r.total)
	}
}

func TestRateLongIdlePreservesBucketBoundary(t *testing.T) {
	start := time.Unix(1000, 0)
	r := &Rate{last: start}
	for i := range r.buckets {
		r.buckets[i] = 100
	}
	r.advance(start.Add(10500 * time.Millisecond))
	if r.buckets != [windowBuckets]int64{} {
		t.Fatalf("idle window retains bytes: %v", r.buckets)
	}
	// A new transfer must expire on the original whole-second boundary,
	// even when the idle gap exceeded the ring size.
	r.buckets[r.idx] = 3000
	r.advance(start.Add(13 * time.Second))
	var sum int64
	for i := 0; i < 3; i++ {
		sum += r.buckets[(r.idx-i+windowBuckets)%windowBuckets]
	}
	if sum != 0 {
		t.Fatalf("traffic survived its bucket window after idle: %d bytes", sum)
	}
}
