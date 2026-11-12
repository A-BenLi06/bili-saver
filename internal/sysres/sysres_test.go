package sysres

import (
	"testing"
)

func TestRead(t *testing.T) {
	s1, _ := Read(8)
	s2, rows2 := Read(8)
	if s1.Cores <= 0 || s2.Cores <= 0 {
		t.Fatalf("cores <= 0: %d %d", s1.Cores, s2.Cores)
	}
	for i, s := range []Snapshot{s1, s2} {
		if s.CPUPercent < 0 || s.CPUPercent > 100 {
			t.Fatalf("poll%d cpu percent out of range: %v", i+1, s.CPUPercent)
		}
		if s.MemTotal == 0 {
			t.Fatal("mem total is 0")
		}
		if s.MemUsed > s.MemTotal {
			t.Fatalf("mem used > total: %v > %v", s.MemUsed, s.MemTotal)
		}
		if s.NetRecvTotal == 0 {
			t.Fatal("net recv total is 0")
		}
	}
	if len(rows2) == 0 {
		t.Fatal("no processes returned")
	}
	for i := 1; i < len(rows2); i++ {
		if rows2[i-1].CPUPct < rows2[i].CPUPct {
			t.Fatalf("not sorted by cpu desc: %v < %v", rows2[i-1].CPUPct, rows2[i].CPUPct)
		}
	}
	// 一致性：快照中的进程字段必须与排行中 self 行严格相等（同一采样）
	var self *ProcRow
	for i := range rows2 {
		if rows2[i].IsSelf {
			self = &rows2[i]
			break
		}
	}
	// 排行仅包含前 8；低负载的 self 可以不在榜内，快照仍应有效。
	if s2.ProcRSSBytes == 0 {
		t.Fatal("self RSS missing from snapshot")
	}
	if self != nil && (self.CPUPct != s2.ProcCPUPercent || self.MemBytes != s2.ProcRSSBytes) {
		t.Fatalf("self row mismatch: row=%+v snap=%v/%v", *self, s2.ProcCPUPercent, s2.ProcRSSBytes)
	}
	// 瞬时语义：差分值有界
	if s2.ProcCPUPercent < 0 || s2.ProcCPUPercent > 100 {
		t.Fatalf("proc cpu out of range: %v", s2.ProcCPUPercent)
	}
}
