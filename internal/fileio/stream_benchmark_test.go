package fileio

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

var streamChunks = []int{128, 384, 640, 900, 1280, 1750, 2500, 3500, 5000, 7000, 10000, 32768, 65536, 128, 384, 640, 900, 1280, 1750}

func streamProducer(body []byte, chunks []int) func(io.Writer) error {
	return func(w io.Writer) error {
		for left, i := body, 0; len(left) != 0; i++ {
			n := min(len(left), chunks[i%len(chunks)])
			if _, err := w.Write(left[:n]); err != nil {
				return err
			}
			left = left[n:]
		}
		return nil
	}
}

func BenchmarkStreamWrite(b *testing.B) {
	body := bytes.Repeat([]byte("simulated archived data\n"), 32768)
	for _, tc := range []struct {
		name    string
		chunks  []int
		replace bool
	}{
		{"Mixed", streamChunks, false},
		{"Large", []int{65536}, false},
		{"Replace", streamChunks, true},
		{"Short", []int{701}, false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "archive")
			contents := body
			if tc.name == "Short" {
				contents = body[:4096]
			}
			b.SetBytes(int64(len(contents)))
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				b.Fatal(err)
			}
			produce := streamProducer(contents, tc.chunks)
			next := bytes.Clone(contents)
			next[100] ^= 1
			alternate := streamProducer(next, tc.chunks)
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				fn := produce
				if tc.replace && i%2 == 0 {
					fn = alternate
				}
				if changed, err := Write(path, fn); err != nil || changed != tc.replace {
					b.Fatalf("changed=%v, err=%v", changed, err)
				}
			}
		})
	}
}
