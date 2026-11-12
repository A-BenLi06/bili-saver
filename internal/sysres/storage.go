package sysres

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// Storage 是数据目录所在文件系统的容量，不代表归档文件的大小。
type Storage struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"` // 当前可用容量；部分文件系统另有保留空间。
	UsedPercent float64 `json:"used_percent"`
}

var storageResources storageCache

// ReadStorageCached 只读文件系统容量，每十秒采样一次，避免遍历归档文件。
// 不可访问时返回 nil；不把采集失败伪装成空磁盘。
func ReadStorageCached(dataDir string) *Storage {
	return storageResources.get(filepath.Clean(dataDir), disk.Usage, time.Now)
}

type storageCache struct {
	mu    sync.Mutex
	path  string
	at    time.Time
	value *Storage
}

func (c *storageCache) get(path string, read func(string) (*disk.UsageStat, error), now func() time.Time) *Storage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || c.path != path || now().Sub(c.at) >= 10*time.Second {
		c.value = nil
		if u, err := read(path); err == nil && u != nil && u.Total > 0 {
			c.value = &Storage{Total: u.Total, Used: u.Used, Free: u.Free, UsedPercent: u.UsedPercent}
		}
		c.path, c.at = path, now()
	}
	if c.value == nil {
		return nil
	}
	copy := *c.value
	return &copy
}
