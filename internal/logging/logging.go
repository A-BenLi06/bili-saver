// Package logging 统一 slog 初始化。
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Setup 按级别初始化默认 logger；file 非空时同时写文件。
func Setup(level, file string) (*slog.Logger, error) {
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
	w := io.Writer(os.Stdout)
	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		w = io.MultiWriter(os.Stdout, f)
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv})
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger, nil
}
