package sysres

import (
	"sync"
	"time"
)

const resourceSampleTTL = time.Second

var resources resourceCache

// ReadCached 合并多个页面同时轮询的昂贵 OS 采样，共享同一秒的差分基线。
// Read 保留即时读取语义；返回排行的副本，调用方修改不会污染缓存。
func ReadCached(limit int) (Snapshot, []ProcRow) {
	if limit <= 0 || limit > 16 {
		limit = 8
	}
	return resources.get(limit, Read, time.Now)
}

type resourceCache struct {
	mu       sync.Mutex
	valid    bool
	limit    int
	at       time.Time
	snapshot Snapshot
	rows     []ProcRow
}

func (c *resourceCache) get(limit int, read func(int) (Snapshot, []ProcRow), now func() time.Time) (Snapshot, []ProcRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || c.limit != limit || now().Sub(c.at) >= resourceSampleTTL {
		c.snapshot, c.rows = read(limit)
		c.at, c.limit, c.valid = now(), limit, true
	}
	return c.snapshot, append([]ProcRow(nil), c.rows...)
}
