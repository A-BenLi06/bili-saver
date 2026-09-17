package logging

// 环形缓冲快照语义测试。

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRingSnapshot(t *testing.T) {
	ringMu.Lock()
	ringBuf = nil
	ringSeq = 0
	ringMu.Unlock()
	w := ringWriter{}
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	lines, latest := RingSnapshot(0, 0)
	if len(lines) != 10 || latest != 10 {
		t.Fatalf("应 10 行 latest=10: %d %d", len(lines), latest)
	}
	if !strings.HasSuffix(lines[0].Text, "line") {
		t.Errorf("行内容错误: %q", lines[0].Text)
	}
	// after=6 只返回 7..10
	lines, _ = RingSnapshot(6, 0)
	if len(lines) != 4 || lines[0].Seq != 7 {
		t.Errorf("after=6 应返回 4 行: %d", len(lines))
	}
	// max 限制
	lines, _ = RingSnapshot(0, 2)
	if len(lines) != 2 {
		t.Errorf("max=2 应截断: %d", len(lines))
	}
	// 空行跳过
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	lines, _ = RingSnapshot(10, 0)
	if len(lines) != 0 {
		t.Errorf("空行不应入环: %d", len(lines))
	}
}

func TestCompressOldLogs(t *testing.T) {
	dir := t.TempDir()
	// 造两个旧 .log 与一个 active
	for _, name := range []string{"service-20260910-120000.log", "service-20260911-090000.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("time=2026-09-11T09:00:00.000+08:00 level=INFO msg=test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	active := "service-20260915-010000.log"
	if err := os.WriteFile(filepath.Join(dir, active), []byte("active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	CompressOldLogs(dir, active)
	// 旧文件应变成 .gz 且原文件删除；active 原样保留
	for _, name := range []string{"service-20260910-120000.log.gz", "service-20260911-090000.log.gz", active} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s 应存在: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "service-20260910-120000.log")); !os.IsNotExist(err) {
		t.Error("已压缩的旧 .log 应被删除")
	}
	// .gz 内容可解回原文
	f, err := os.Open(filepath.Join(dir, "service-20260910-120000.log.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(gz)
	if !strings.Contains(string(body), "level=INFO") {
		t.Errorf(".gz 内容错误: %q", string(body))
	}
	// 幂等：重复执行不再产生变化（.gz 跳过，active 跳过）
	CompressOldLogs(dir, active)
	if _, err := os.Stat(filepath.Join(dir, active)); err != nil {
		t.Error("active 不应被压缩")
	}
}
