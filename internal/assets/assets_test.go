package assets

import (
	"bili-saver/internal/store"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImmutableObjectsDeduplicationAndIntegrity(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := Service{st, root}
	src := filepath.Join(root, "input.mp4")
	if err = os.WriteFile(src, []byte("original bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Import(context.Background(), src, store.Asset{Bvid: "BVtest", Cid: 1, Role: "video", Origin: "bilibili", Variant: "720p", Provenance: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Import(context.Background(), src, store.Asset{Bvid: "BVtest", Cid: 2, Role: "video", Origin: "bilibili", Variant: "720p", Provenance: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if a.StorageKey != b.StorageKey {
		t.Fatal("identical content was duplicated")
	}
	if err = svc.Verify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatal("input was removed")
	}
	p, err := svc.Resolve(a)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("tampered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = svc.Verify(context.Background(), a); err == nil {
		t.Fatal("same-size corruption was not detected")
	}
}
