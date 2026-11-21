package archiveops

import (
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkExportCatalog(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "catalog.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	_, err = st.DB.Exec(`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<20000)
		INSERT INTO archive_assets (bvid,cid,role,origin,variant,policy_hash,sha256,size,storage_key,provenance,created_at,verified_at)
		SELECT printf('BV%05d',v),v,'video','bilibili','dash','sample-policy',
			printf('%064x',v),v+1000,printf('media/path/%05d',v),'fixture',10,20 FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		catalog, err := ExportCatalog(st)
		if err != nil || len(catalog.Assets) != 20000 {
			b.Fatalf("catalog has %d assets: %v", len(catalog.Assets), err)
		}
	}
}
