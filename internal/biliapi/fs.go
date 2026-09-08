package biliapi

import (
	"errors"
	"os"
)

var errFileNotFound = errors.New("file not found")

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func osReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errFileNotFound
		}
		return nil, err
	}
	return b, nil
}
