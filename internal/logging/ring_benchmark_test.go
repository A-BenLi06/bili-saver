package logging

import (
	"strings"
	"testing"
)

func BenchmarkRingSnapshot(b *testing.B) {
	const latest = 2*ringCap - 700
	for _, test := range []struct {
		name  string
		after uint64
		want  int
	}{
		{"CaughtUp", latest, 0},
		{"Recent", latest - 8, 8},
		{"Backlog", 0, 1000},
	} {
		b.Run(test.name, func(b *testing.B) {
			seedBenchmarkRing(b, latest)
			for b.Loop() {
				lines, seq := RingSnapshot(test.after, 1000)
				if len(lines) != test.want || seq != latest {
					b.Fatalf("returned %d lines at sequence %d", len(lines), seq)
				}
			}
		})
	}
}

func BenchmarkRingWrite(b *testing.B) {
	b.Run("Steady", func(b *testing.B) {
		seedBenchmarkRing(b, ringCap)
		writer := ringWriter{}
		line := []byte("time=2026-11-22T12:00:00+08:00 level=INFO msg=saved archive item\n")
		for b.Loop() {
			if n, err := writer.Write(line); err != nil || n != len(line) {
				b.Fatalf("wrote %d bytes: %v", n, err)
			}
		}
	})
}

func seedBenchmarkRing(b *testing.B, count int) {
	previousBuf, previousSeq := ringBuf, ringSeq
	b.Cleanup(func() { ringBuf, ringSeq = previousBuf, previousSeq })
	ringBuf, ringSeq = nil, 0
	writer := ringWriter{}
	if _, err := writer.Write([]byte(strings.Repeat("synthetic log line\n", count))); err != nil {
		b.Fatal(err)
	}
}
