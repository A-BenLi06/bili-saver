package archivepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableHistoricalPathsAndBoundaries(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{`media/BV/p1/file.mp4`, `media\BV\p1\file.mp4`, `data\media\BV\p1\file.mp4`, `C:\old\data\media\BV\p1\file.mp4`} {
		got, err := Key(root, key)
		if err != nil || got != "media/BV/p1/file.mp4" {
			t.Fatalf("%s: %s %v", key, got, err)
		}
	}
	for _, key := range []string{`../secret`, `media/../../secret`, `C:\unrelated\secret`} {
		if _, err := Resolve(root, key); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Log("symlink unavailable")
		return
	}
	if _, err := Resolve(root, "escape/missing.mp4"); err == nil {
		t.Fatal("escaped through ancestor symlink")
	}
}
