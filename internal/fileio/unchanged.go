// Package fileio 提供内容比较与原子替换，重复输出只读已有文件，不创建临时件。
package fileio

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Write 按块比较流式输出；首次差异后才创建临时文件并复制已匹配的前缀。
// produce 失败不会损坏旧文件。调用者负责同一路径的并发互斥。
func Write(path string, produce func(io.Writer) error) (changed bool, err error) {
	old, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	w := &compareWriter{path: path, old: old}
	defer func() {
		if old != nil {
			old.Close()
		}
		if w.out != nil {
			w.out.Close()
			os.Remove(w.out.Name())
		}
	}()
	if err := produce(w); err != nil {
		return false, err
	}
	if w.out == nil {
		// Unconsumed read-ahead means the producer shortened the file.
		if old != nil && w.next == w.end {
			var tail [1]byte
			_, err := old.Read(tail[:])
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
		}
		if err := w.start(); err != nil {
			return false, err
		}
	}
	if err := w.out.Sync(); err != nil {
		return false, err
	}
	if err := w.out.Close(); err != nil {
		return false, err
	}
	// Windows 必须在替换前释放旧文件的打开句柄。
	if old != nil {
		if err := old.Close(); err != nil {
			return false, err
		}
	}
	if err := os.Rename(w.out.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func WriteBytes(path string, data []byte) (bool, error) {
	return Write(path, func(w io.Writer) error { _, err := w.Write(data); return err })
}

type compareWriter struct {
	path      string
	old       *os.File
	out       *os.File
	matched   int64
	next, end int
	buf       [15 * 1024]byte
}

func (w *compareWriter) start() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(w.path), ".output-*")
	if err != nil {
		return err
	}
	w.out = out
	if err := out.Chmod(0644); err != nil {
		return err
	}
	if w.matched > 0 {
		if _, err := w.old.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.CopyN(out, w.old, w.matched)
	}
	return err
}

func (w *compareWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 && w.out == nil && w.old != nil {
		n := min(len(p), len(w.buf))
		if w.next == w.end {
			size := n
			// Small writes share a refill. Large writes only read what they compare.
			if n < 1024 {
				size = len(w.buf)
			}
			got, err := io.ReadAtLeast(w.old, w.buf[:size], n)
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return total - len(p), err
			}
			if got < n {
				break
			}
			w.next, w.end = 0, got
		}
		n = min(n, w.end-w.next)
		if !bytes.Equal(p[:n], w.buf[w.next:w.next+n]) {
			break
		}
		w.next += n
		w.matched += int64(n)
		p = p[n:]
	}
	if len(p) == 0 {
		return total, nil
	}
	if w.out == nil {
		if err := w.start(); err != nil {
			return total - len(p), err
		}
	}
	n, err := w.out.Write(p)
	return total - len(p) + n, err
}
