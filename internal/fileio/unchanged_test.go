package fileio

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteStreamingComparisonAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "file")
	data := bytes.Repeat([]byte("原件 & text"), 20000)
	write := func(data []byte) (bool, error) {
		return Write(path, func(w io.Writer) error {
			for len(data) > 0 {
				n := min(len(data), 701)
				if _, err := w.Write(data[:n]); err != nil {
					return err
				}
				data = data[n:]
			}
			return nil
		})
	}
	if changed, err := write(data); err != nil || !changed {
		t.Fatalf("initial: %v %v", changed, err)
	}
	at := time.Unix(1000, 0)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	if changed, err := write(data); err != nil || changed {
		t.Fatalf("unchanged: %v %v", changed, err)
	}
	fi, _ := os.Stat(path)
	if !fi.ModTime().Equal(at) {
		t.Fatal("unchanged file rewritten")
	}
	boom := errors.New("producer failed")
	if _, err := Write(path, func(w io.Writer) error { w.Write(data[:100000]); w.Write([]byte("different")); return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("failure corrupted original")
	}
	for _, body := range [][]byte{data[:len(data)-1], append(append([]byte(nil), data...), 1), []byte("changed"), {}, data} {
		if changed, err := write(body); err != nil || !changed {
			t.Fatalf("change: %v %v", changed, err)
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, body) {
			t.Fatal("wrong replacement")
		}
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".output-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files leaked: %v", left)
	}
}

func TestWriteEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if changed, err := WriteBytes(path, nil); !changed || err != nil {
		t.Fatalf("initial empty %v %v", changed, err)
	}
	if changed, err := WriteBytes(path, nil); changed || err != nil {
		t.Fatalf("repeat empty %v %v", changed, err)
	}
}
