package images

import (
	"context"
	"os"
	"path/filepath"
)

type imageFlight struct {
	done chan struct{}
	err  error
}

// 并发归档共享头像时只下载一次。结果只保留到本轮请求结束，失败仍可重试。
func (l *Localizer) fetchMissing(ctx context.Context, rel, url string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	full := filepath.Join(l.DataDir, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		return false, nil
	}
	l.mu.Lock()
	if l.inflight == nil {
		l.inflight = make(map[string]*imageFlight)
	}
	if pending := l.inflight[full]; pending != nil {
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-pending.done:
			return false, pending.err
		}
	}
	pending := &imageFlight{done: make(chan struct{})}
	l.inflight[full] = pending
	l.mu.Unlock()
	added, err := l.fetchFile(ctx, rel, url)
	l.mu.Lock()
	pending.err = err
	delete(l.inflight, full)
	close(pending.done)
	l.mu.Unlock()
	return added, err
}
