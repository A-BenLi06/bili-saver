package logging

import (
	"strings"
	"sync"
	"time"
)

// Line 是内存尾随缓冲的一条日志，包含 slog 前缀。
type Line struct {
	Seq  uint64 `json:"seq"`
	TS   string `json:"ts"`
	Text string `json:"text"`
}

const ringCap = 4000

type logRing struct {
	lines  []Line
	oldest int
	latest uint64
}

var (
	ringMu sync.Mutex
	ring   logRing
)

type ringWriter struct{}

func (ringWriter) Write(p []byte) (int, error) {
	ringMu.Lock()
	defer ringMu.Unlock()
	for line := range strings.SplitSeq(string(p), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ring.latest++
		entry := Line{Seq: ring.latest, TS: time.Now().Format("01-02 15:04:05"), Text: line}
		if ring.lines == nil {
			ring.lines = make([]Line, 0, ringCap)
		}
		if len(ring.lines) < ringCap {
			ring.lines = append(ring.lines, entry)
		} else {
			ring.lines[ring.oldest] = entry
			ring.oldest = (ring.oldest + 1) % ringCap
		}
	}
	return len(p), nil
}

// RingSnapshot 返回 seq 之后的至多 max 行（按序）；after=0 表示从头。
func RingSnapshot(after uint64, max int) ([]Line, uint64) {
	ringMu.Lock()
	defer ringMu.Unlock()
	if after >= ring.latest {
		return nil, ring.latest
	}
	size := len(ring.lines)
	// Sequence numbers are consecutive, so a cursor maps directly to a slot.
	firstSeq := ring.latest - uint64(size) + 1
	skip := 0
	if after >= firstSeq {
		skip = int(after-firstSeq) + 1
	}
	count := size - skip
	if max > 0 {
		count = min(count, max)
	}
	start := (ring.oldest + skip) % size
	head := min(count, size-start)
	out := make([]Line, count)
	copy(out[:head], ring.lines[start:start+head])
	copy(out[head:], ring.lines[:count-head])
	return out, ring.latest
}
