package archiveops

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"bili-saver/internal/store"
)

func TestReadCatalogPreservesExportedFieldsAndOrder(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	empty, err := ExportCatalog(st)
	if err != nil || empty.Assets == nil || len(empty.Assets) != 0 {
		t.Fatalf("empty catalog = %+v, %v", empty, err)
	}
	want := []CatalogAsset{
		{Bvid: "BV-z", Cid: 7, Role: "video", Origin: "bilibili", Variant: "dash", SHA256: "first", Size: 32, Provenance: "下载", ObservedAt: 17},
		{Bvid: "BV-a", Cid: 0, Role: "cover", Origin: "bilibili", Variant: "", SHA256: "second", Size: 16, Provenance: "legacy", ObservedAt: 3},
	}
	for _, a := range want {
		if err := st.AddAsset(&store.Asset{Bvid: a.Bvid, Cid: a.Cid, Role: a.Role, Origin: a.Origin,
			Variant: a.Variant, PolicyHash: "private policy", SHA256: a.SHA256, Size: a.Size, StorageKey: "private path",
			Provenance: a.Provenance, CreatedAt: a.ObservedAt, VerifiedAt: 99}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadCatalog(context.Background(), root)
	if err != nil || got.Version != FormatVersion || got.Kind != "archive-content-catalog" || !slices.Equal(got.Assets, want) {
		t.Fatalf("catalog = %+v, %v", got, err)
	}
}
