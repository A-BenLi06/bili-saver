// Package logging 统一 slog 初始化：stdout + 用户可选文件 + 按日服务日志
// （<data_dir>/logs/service-YYYYMMDD.log，供日志页导出）+ 内存环形缓冲
// （供管理端日志页实时尾随）。捕获器在 writer 层实现，对 slog 零侵入。
package logging

import (
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Line 内存环形缓冲中的一条日志（整行原文，含 slog 的 time=/level= 前缀）。
type Line struct {
	Seq  uint64 `json:"seq"`
	TS   string `json:"ts"`
	Text string `json:"text"`
}

const ringCap = 4000

var (
	ringMu        sync.Mutex
	ringBuf       []Line
	ringSeq       uint64
	logsDir       string
	dirOnce       sync.Once
	activeLogName string
)

type ringWriter struct{}

func (ringWriter) Write(p []byte) (int, error) {
	ringMu.Lock()
	defer ringMu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ringSeq++
		ringBuf = append(ringBuf, Line{Seq: ringSeq, TS: time.Now().Format("01-02 15:04:05"), Text: line})
	}
	if len(ringBuf) > ringCap {
		ringBuf = ringBuf[len(ringBuf)-ringCap:]
	}
	return len(p), nil
}

// RingSnapshot 返回 seq 之后的至多 max 行（按序）；after=0 表示从头。
func RingSnapshot(after uint64, max int) ([]Line, uint64) {
	ringMu.Lock()
	defer ringMu.Unlock()
	var out []Line
	for _, l := range ringBuf {
		if l.Seq > after {
			out = append(out, l)
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	return out, ringSeq
}

// LogsDir 按日服务日志所在目录。
func LogsDir() string { return logsDir }

// runFileWriter 每次服务启动一个独立日志文件（service-YYYYMMDD-HHMMSS.log），
// 实时追加写入；旧文件由 CompressOldLogs 分次压缩归档。
type runFileWriter struct {
	mu   sync.Mutex
	dir  string
	name string
	f    *os.File
}

func (w *runFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := os.MkdirAll(w.dir, 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(filepath.Join(w.dir, w.name),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		w.f = f
	}
	return w.f.Write(p)
}

// CompressOldLogs 把目录里非 active 的 .log 压缩为同名 .gz 并删除原文件
// （分天/分次归档；幂等，active 永不触碰）。best-effort，单文件失败跳过。
func CompressOldLogs(dir, active string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".log") || name == active {
			continue
		}
		src := filepath.Join(dir, name)
		if gzipCompressFile(src, src+".gz") {
			_ = os.Remove(src)
		} else {
			_ = os.Remove(src + ".gz") // 清掉半成品
		}
	}
}

func gzipCompressFile(src, dst string) bool {
	in, err := os.Open(src)
	if err != nil {
		return false
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return false
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	if closeErr := gz.Close(); closeErr != nil {
		out.Close()
		return false
	}
	if copyErr != nil {
		out.Close()
		return false
	}
	return out.Close() == nil
}

// Setup 按级别初始化默认 logger：stdout + 按日服务日志（总是开启）+
// file 非空时的用户指定文件 + 内存环形缓冲。dataDir 为空则不写按日日志。
func Setup(level, file, dataDir string) (*slog.Logger, error) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	ws := []io.Writer{os.Stdout, ringWriter{}}
	if dataDir != "" {
		dirOnce.Do(func() { logsDir = filepath.Join(dataDir, "logs") })
		// 每次启动一个独立 run 文件；先把上一轮遗留的 .log 归档为 .gz（分天/分次压缩）
		activeLogName = "service-" + time.Now().Format("20060102-150405") + ".log"
		CompressOldLogs(logsDir, activeLogName)
		ws = append(ws, &runFileWriter{dir: logsDir, name: activeLogName})
		go func() { // 定时巡检：把非活动文件压缩归档
			for {
				time.Sleep(6 * time.Hour)
				CompressOldLogs(logsDir, activeLogName)
			}
		}()
	}
	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		ws = append(ws, f)
	}
	h := slog.NewTextHandler(io.MultiWriter(ws...), &slog.HandlerOptions{Level: lv})
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger, nil
}

// ActiveLogFile 当前 run 的日志文件名（空=未启用日志目录）。
func ActiveLogFile() string { return activeLogName }
