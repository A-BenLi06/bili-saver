package archiveguard

import (
	"testing"
)

func TestExclusiveLockAndRelease(t *testing.T) {
	root := t.TempDir()
	one, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if two, err := Acquire(root); err == nil {
		two.Close()
		t.Fatal("second writer acquired archive")
	}
	if err = one.Close(); err != nil {
		t.Fatal(err)
	}
	two, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	two.Close()
}
