// Package logging 统一 slog 初始化：stdout + 用户可选文件 + 按日服务日志
// （<data_dir>/logs/service-YYYYMMDD.log，供日志页导出）+ 内存环形缓冲
// （供管理端日志页实时尾随）。捕获器在 writer 层实现，对 slog 零侵入。
package logging

import (
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
	ringMu  sync.Mutex
	ringBuf []Line
	ringSeq uint64
	logsDir string
	dirOnce sync.Once
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

// dailyFileWriter 按日期切换文件（service-YYYYMMDD.log），懒打开。
type dailyFileWriter struct {
	mu  sync.Mutex
	dir string
	day string
	f   *os.File
}

func (w *dailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	day := time.Now().Format("20060102")
	if w.f == nil || day != w.day {
		if w.f != nil {
			_ = w.f.Close()
		}
		if err := os.MkdirAll(w.dir, 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(filepath.Join(w.dir, "service-"+day+".log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		w.f, w.day = f, day
	}
	return w.f.Write(p)
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
		ws = append(ws, &dailyFileWriter{dir: logsDir})
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
