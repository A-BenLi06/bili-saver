package sysres

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceCacheCoalescesPollsAndOwnsReturnedRows(t *testing.T) {
	var c resourceCache
	var calls atomic.Int32
	at := time.Unix(1000, 0)
	now := func() time.Time { return at }
	read := func(limit int) (Snapshot, []ProcRow) {
		calls.Add(1)
		return Snapshot{UnixMs: at.UnixMilli()}, []ProcRow{{PID: 1, CPUPct: 20, MemBytes: 100}}
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, rows := c.get(8, read, now)
			if s.UnixMs != at.UnixMilli() || rows[0].MemBytes != 100 {
				t.Error("inconsistent shared sample")
			}
			rows[0].MemBytes = 0
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("OS read %d times", calls.Load())
	}
	at = at.Add(resourceSampleTTL)
	c.get(8, read, now)
	if calls.Load() != 2 {
		t.Fatal("stale sample retained")
	}
	c.get(16, read, now)
	if calls.Load() != 3 {
		t.Fatal("different limit truncated from old sample")
	}
}
