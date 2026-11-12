// Package archiveguard prevents multiple writers and provides a maintenance barrier.
package archiveguard

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

func Acquire(root string) (*Lock, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(absolute, ".bili-saver.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("archive is in use; stop its service before running another writer or maintenance command: %w", err)
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}
