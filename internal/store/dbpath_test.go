package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabasePathIsLiteral(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Archive #1 中文")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "archive #1.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err = os.Stat(path); err != nil {
		t.Fatal("SQLite path was interpreted as URI syntax", err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
}
