package collector

import (
	"context"
	"sync"
)

// 同视频同步互斥，避免不同源/统计任务交叉改写 last_seen 与消失核对。
// 等待可取消，引用归零即回收；不同视频仍可并行。
type videoGates struct {
	mu    sync.Mutex
	items map[string]*videoGate
}

type videoGate struct {
	token chan struct{}
	refs  int
}

func (g *videoGates) acquire(ctx context.Context, bvid string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.items == nil {
		g.items = make(map[string]*videoGate)
	}
	item := g.items[bvid]
	if item == nil {
		item = &videoGate{token: make(chan struct{}, 1)}
		item.token <- struct{}{}
		g.items[bvid] = item
	}
	item.refs++
	g.mu.Unlock()
	drop := func() {
		g.mu.Lock()
		item.refs--
		if item.refs == 0 {
			delete(g.items, bvid)
		}
		g.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-item.token:
		if err := ctx.Err(); err != nil {
			item.token <- struct{}{}
			drop()
			return nil, err
		}
		return func() { item.token <- struct{}{}; drop() }, nil
	}
}
