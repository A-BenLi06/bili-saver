package logging

// 环形缓冲快照语义测试。

import (
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
