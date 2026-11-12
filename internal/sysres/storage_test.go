package sysres

import (
	"errors"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestStorageCache(t *testing.T) {
	var c storageCache
	at := time.Unix(100, 0)
	calls := 0
	read := func(path string) (*disk.UsageStat, error) {
		calls++
		if path == "missing" {
			return nil, errors.New("not available")
		}
		return &disk.UsageStat{Total: 100, Used: 60, Free: 35, UsedPercent: 60}, nil
	}
	now := func() time.Time { return at }
	first := c.get("data", read, now)
	first.Free = 0
	if next := c.get("data", read, now); next.Free != 35 || calls != 1 {
		t.Fatalf("cached value mutated or sampled twice: %+v calls=%d", next, calls)
	}
	at = at.Add(10 * time.Second)
	if c.get("data", read, now) == nil || calls != 2 {
		t.Fatal("cache did not expire")
	}
	if c.get("other-volume", read, now) == nil || calls != 3 {
		t.Fatal("data directory change reused wrong volume")
	}
	if c.get("missing", read, now) != nil || c.get("missing", read, now) != nil || calls != 4 {
		t.Fatal("unavailable storage must be nil and failures must be cached")
	}
	if c.get("data", read, now) == nil {
		t.Fatal("failed volume poisoned another volume")
	}
}

func TestStorageRealFilesystem(t *testing.T) {
	s := ReadStorageCached(t.TempDir())
	if s == nil || s.Total == 0 || s.Used > s.Total || s.Free > s.Total || s.UsedPercent < 0 || s.UsedPercent > 100 {
		t.Fatalf("invalid filesystem capacity: %+v", s)
	}
}
