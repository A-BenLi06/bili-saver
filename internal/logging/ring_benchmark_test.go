package logging

import "testing"

func BenchmarkRingSnapshot(b *testing.B) {
	for _, test := range []struct {
		name  string
		after uint64
		want  int
	}{
		{"CaughtUp", ringCap, 0},
		{"Recent", ringCap - 8, 8},
		{"Backlog", 0, 1000},
	} {
		b.Run(test.name, func(b *testing.B) {
			previousBuf, previousSeq := ringBuf, ringSeq
			b.Cleanup(func() { ringBuf, ringSeq = previousBuf, previousSeq })
			ringBuf = make([]Line, ringCap)
			ringSeq = ringCap
			for i := range ringBuf {
				ringBuf[i] = Line{Seq: uint64(i + 1), Text: "synthetic log line"}
			}
			for b.Loop() {
				lines, _ := RingSnapshot(test.after, 1000)
				if len(lines) != test.want {
					b.Fatalf("returned %d lines, want %d", len(lines), test.want)
				}
			}
		})
	}
}
