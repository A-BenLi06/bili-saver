package collector

// 快照正文按内容寻址；每次抓取仍单独记录时间、参数与状态码。
// 历史时间戳文件继续可读，不修改或清理已有 origin 数据。

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
)

// 固定条带锁防止同一正文并发写入，不维护随快照数量增长的锁表。
var snapshotLocks [64]sync.Mutex
var snapshotGzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

func (c *Collector) SnapshotHook() biliapi.SnapshotHook {
	return func(endpoint string, params url.Values, status int, body []byte) error {
		fetchedAt := store.Now()
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		rel := filepath.Join("raw", "objects", hash[:2], hash[2:4], hash+".json.gz")
		full := filepath.Join(c.Cfg.View().DataDir, rel)
		if err := persistSnapshot(full, body, &snapshotLocks[int(sum[0])%len(snapshotLocks)]); err != nil {
			return fmt.Errorf("snapshot body: %w", err)
		}
		if err := c.St.AddSnapshot(&store.Snapshot{
			Endpoint: endpoint, ParamsJSON: params.Encode(), RespHash: hash,
			BodyPath: filepath.ToSlash(rel), StatusCode: status, FetchedAt: fetchedAt,
		}); err != nil {
			return fmt.Errorf("snapshot index: %w", err)
		}
		return nil
	}
}

func persistSnapshot(path string, body []byte, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().IsRegular() && fi.Size() > 0 {
			return nil
		}
		return fmt.Errorf("快照目标不是有效文件: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	gz := snapshotGzipPool.Get().(*gzip.Writer)
	gz.Reset(f)
	_, writeErr := gz.Write(body)
	gzipErr := gz.Close()
	// 避免池里的 writer 保留文件引用；复用压缩状态与缓冲。
	gz.Reset(io.Discard)
	snapshotGzipPool.Put(gz)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if gzipErr != nil {
		return gzipErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
