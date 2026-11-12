package collector

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestExistingSnapshotCorruptionIsNotAccepted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snapshot.json.gz")
	var mu sync.Mutex
	if err := persistSnapshot(p, []byte(`{"ok":true}`), &mu); err != nil {
		t.Fatal(err)
	}
	if err := persistSnapshot(p, []byte(`{"ok":true}`), &mu); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("nonempty corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := persistSnapshot(p, []byte(`{"ok":true}`), &mu); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	body, _ := os.ReadFile(p)
	if string(body) != "nonempty corruption" {
		t.Fatal("corrupt original overwritten without review")
	}
}
