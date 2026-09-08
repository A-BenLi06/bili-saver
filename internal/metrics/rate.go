// Package metrics 提供进程内字节速率统计（滑动窗口每秒桶），
// 用于总览页展示服务实时下载（CDN→本机）与上传（本机→播放端）吞吐。
package metrics

import (
	"sync"
	"time"
)

const windowBuckets = 6

// Rate 滑动窗口速率计：Add 累加字节，BPS 返回最近 3 秒平均速率。
type Rate struct {
	mu      sync.Mutex
	buckets [windowBuckets]int64
	idx     int
	last    time.Time
	total   int64
}

// advance 推进时间桶（调用方持锁）。
func (r *Rate) advance(now time.Time) {
	if r.last.IsZero() {
		r.last = now
		return
	}
	elapsed := int(now.Sub(r.last).Seconds())
	if elapsed <= 0 {
		return
	}
	steps := elapsed
	if steps > windowBuckets {
		steps = windowBuckets
	}
	for i := 0; i < steps; i++ {
		r.idx = (r.idx + 1) % windowBuckets
		r.buckets[r.idx] = 0
	}
	r.last = now
}

// Add 记录 n 字节。
func (r *Rate) Add(n int64) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advance(time.Now())
	r.buckets[r.idx] += n
	r.total += n
}

// BPS 最近 3 秒平均速率（字节/秒）；无写入时间桶自动衰减为 0。
func (r *Rate) BPS() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advance(time.Now())
	var sum int64
	const n = 3 // 当前桶 + 前 2 桶
	for i := 0; i < n; i++ {
		sum += r.buckets[(r.idx-i+windowBuckets)%windowBuckets]
	}
	return sum / n
}

// Total 进程启动以来累计字节。
func (r *Rate) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

var (
	// Download CDN→本机（归档下载）吞吐。
	Download = &Rate{}
	// Upload 本机→播放端（/media 下发）吞吐。
	Upload = &Rate{}
)
