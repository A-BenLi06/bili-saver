package sysres

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ArchiveSizes 为补档原件/生成数据的文件逻辑字节数。输出媒体库、站点依赖、
// 插件程序与日志不属于补档资源；不跟随符号链接或读取文件内容。
type ArchiveSizes struct {
	VideoBytes  uint64 `json:"video_bytes"`
	OtherBytes  uint64 `json:"other_bytes"`
	TotalBytes  uint64 `json:"total_bytes"`
	Partial     bool   `json:"partial"`
	Provisional bool   `json:"provisional,omitempty"`
	Files       uint64 `json:"files"`
	UnixMs      int64  `json:"unix_ms"`
}

type ArchiveStorage struct {
	Sizes    *ArchiveSizes `json:"sizes"`
	Scanning bool          `json:"scanning"`
}

var archiveResources archiveSizeCache

// ReadArchiveStorageCached 首次请求与十分钟到期触发后台单飞扫描，HTTP不等目录遍历。
// 刷新时继续返回上次结果；路径变化后不返回旧目录的数值。
func ReadArchiveStorageCached(dataDir string) ArchiveStorage {
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return ArchiveStorage{Sizes: &ArchiveSizes{Partial: true}}
	}
	return archiveResources.get(filepath.Clean(path), scanArchiveSizesProgress, time.Now)
}

type archiveSizeCache struct {
	mu    sync.Mutex
	path  string
	at    time.Time
	busy  bool
	sizes *ArchiveSizes
}

func (c *archiveSizeCache) get(path string, scan func(string, func(ArchiveSizes)) ArchiveSizes, now func() time.Time) ArchiveStorage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != path {
		c.path, c.at, c.sizes = path, time.Time{}, nil
	}
	if !c.busy && (c.at.IsZero() || now().Sub(c.at) >= 10*time.Minute) {
		c.busy = true
		go func() {
			result := scan(path, func(progress ArchiveSizes) {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.path == path && (c.sizes == nil || c.sizes.Provisional) {
					c.sizes = &progress
				}
			})
			c.mu.Lock()
			defer c.mu.Unlock()
			c.busy = false
			if c.path == path {
				c.sizes, c.at = &result, now()
			}
		}()
	}
	result := ArchiveStorage{Scanning: c.busy}
	if c.sizes != nil {
		copy := *c.sizes
		result.Sizes = &copy
	}
	return result
}

func scanArchiveSizes(dataDir string) ArchiveSizes {
	return scanArchiveSizesProgress(dataDir, nil)
}

func scanArchiveSizesProgress(dataDir string, publish func(ArchiveSizes)) ArchiveSizes {
	var result ArchiveSizes
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		return ArchiveSizes{Partial: true, UnixMs: time.Now().UnixMilli()}
	}
	lastPublish := time.Now()
	sinceYield := 0
	progress := func() {
		if publish != nil && time.Since(lastPublish) >= time.Second {
			copy := result
			copy.TotalBytes = copy.VideoBytes + copy.OtherBytes
			copy.Provisional, copy.UnixMs = true, time.Now().UnixMilli()
			publish(copy)
			lastPublish = time.Now()
		}
	}
	for _, name := range []string{"media", "raw", "derived"} {
		root := filepath.Join(dataDir, name)
		if info, err := os.Lstat(root); os.IsNotExist(err) {
			continue
		} else if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			result.Partial = true
			continue
		}
		// 流式读256项，不排序或一次性保留海量目录项。
		stack := []string{root}
		for len(stack) > 0 {
			dir := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f, err := os.Open(dir)
			if err != nil {
				result.Partial = true
				continue
			}
			for {
				entries, readErr := f.ReadDir(256)
				sinceYield += len(entries)
				for _, entry := range entries {
					if entry.Type()&os.ModeSymlink != 0 {
						continue
					}
					if entry.IsDir() {
						stack = append(stack, filepath.Join(dir, entry.Name()))
						continue
					}
					info, err := entry.Info()
					if err != nil {
						if !os.IsNotExist(err) {
							result.Partial = true
						}
						continue
					}
					if !info.Mode().IsRegular() || info.Size() < 0 {
						continue
					}
					result.Files++
					if name == "media" && isVideoPayload(entry.Name()) {
						result.VideoBytes += uint64(info.Size())
					} else {
						result.OtherBytes += uint64(info.Size())
					}
				}
				progress()
				if sinceYield >= 256 {
					time.Sleep(2 * time.Millisecond)
					sinceYield = 0
				}
				if readErr != nil {
					if readErr != io.EOF {
						result.Partial = true
					}
					break
				}
			}
			f.Close()
		}
	}
	for _, name := range []string{"bili-saver.db", "bili-saver.db-wal", "bili-saver.db-shm"} {
		info, err := os.Lstat(filepath.Join(dataDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			result.Partial = true
			continue
		}
		if info.Mode().IsRegular() && info.Size() >= 0 {
			result.OtherBytes += uint64(info.Size())
			result.Files++
		}
	}
	result.TotalBytes = result.VideoBytes + result.OtherBytes
	result.UnixMs = time.Now().UnixMilli()
	return result
}

func isVideoPayload(name string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".part") {
		name = strings.TrimSuffix(name, ".part")
	}
	switch filepath.Ext(name) {
	case ".mp4", ".mkv", ".webm", ".flv", ".avi", ".mov", ".m4v", ".m4s", ".ts":
		return true
	}
	return false
}
