package jobs

import (
	"math"
	"sync"
	"time"
)

// progressReporter 串行化并发回调，500ms 内合并写入；只首次到达 1 立即写。
// latest 始终保留最新回调，终态携带它，关闭后拒绝迟到回调覆盖终态。
type progressReporter struct {
	mu         sync.Mutex
	write      func(float64, string)
	latest     float64
	detail     string
	lastF      float64
	lastDetail string
	lastAt     time.Time
	closed     bool
}

func (p *progressReporter) report(f float64, detail string) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return
	}
	f = max(0, min(1, f))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.latest, p.detail = f, detail
	if !p.lastAt.IsZero() {
		if f == p.lastF && detail == p.lastDetail {
			return
		}
		if time.Since(p.lastAt) < 500*time.Millisecond && !(f == 1 && p.lastF < 1) {
			return
		}
	}
	p.lastAt, p.lastF, p.lastDetail = time.Now(), f, detail
	p.write(f, detail)
}

func (p *progressReporter) finish() (float64, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return p.latest, p.detail
}
