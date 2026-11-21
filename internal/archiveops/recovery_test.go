package archiveops

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/assets"
	"bili-saver/internal/store"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	key := `media/BVfixture/p1/BVfixture-p1.mp4`
	p := filepath.Join(root, filepath.FromSlash(key))
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("fixture video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertVideo(&store.Video{Bvid: "BVfixture", Downloaded: true, LocalPath: `media\BVfixture\p1\BVfixture-p1.mp4`, FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertPage(&store.Page{Bvid: "BVfixture", Cid: 1, Page: 1}); err != nil {
		t.Fatal(err)
	}
	service := assets.Service{Store: st, Root: root}
	if _, err = service.IndexLegacy(context.Background(), p, store.Asset{Bvid: "BVfixture", Cid: 1, Role: "video", Origin: "bilibili", Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"code":0,"data":{"bvid":"BVfixture"}}`)
	raw := filepath.Join(root, "raw", "fixture.json.gz")
	if err = os.MkdirAll(filepath.Dir(raw), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Write(body)
	gz.Close()
	f.Close()
	if err = st.AddSnapshot(&store.Snapshot{Endpoint: "/x/web-interface/view", BodyPath: `raw\fixture.json.gz`, RespHash: fmt.Sprintf("%x", sha256.Sum256(body)), FetchedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "cookies.json"), []byte("private-fixture-cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBackupRestoreRelocationAndReadOnlyCheck(t *testing.T) {
	ctx := context.Background()
	root := fixture(t)
	before, _, err := assets.Digest(ctx, filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Check(ctx, root)
	if err != nil || !report.DatabaseOK || len(report.Issues) > 0 {
		t.Fatal(report, err)
	}
	after, _, _ := assets.Digest(ctx, filepath.Join(root, "bili-saver.db"))
	if before != after {
		t.Fatal("check modified database")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	m, err := Backup(ctx, root, backup)
	if err != nil {
		t.Fatal(err)
	}
	// The backup manifest describes the original media bytes.
	const wantMedia = "media/BVfixture/p1/BVfixture-p1.mp4"
	mediaHash := fmt.Sprintf("%x", sha256.Sum256([]byte("fixture video bytes")))
	found := false
	for _, entry := range m.Files {
		if entry.Key == wantMedia {
			found = true
			if entry.SHA256 != mediaHash || entry.Size != int64(len("fixture video bytes")) {
				t.Fatalf("invalid media manifest entry: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("media missing from backup manifest")
	}
	if !m.Sensitive || m.Purpose != "private-backup" {
		t.Fatal("private state was not classified")
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(ctx, backup, destination); err != nil {
		t.Fatal(err)
	}
	report, err = Check(ctx, destination)
	if err != nil || !report.DatabaseOK || len(report.Issues) > 0 {
		t.Fatal(report, err)
	}
	if _, err = os.Stat(filepath.Join(destination, "config.yaml")); err != nil {
		t.Fatal("portable startup configuration missing", err)
	}
	if _, err = os.Stat(filepath.Join(root, "media", "BVfixture", "p1", "BVfixture-p1.mp4")); err != nil {
		t.Fatal("source was changed", err)
	}
	catalog, err := ReadCatalog(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(catalog)
	var decoded map[string]any
	if err = json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Assets) != 1 {
		t.Fatal(catalog)
	}
	for _, forbidden := range []string{"cookies", "token", "storage_key", "local_path", "password"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatal("catalog contains node state")
		}
	}
}

func TestRestoreRefusesCorruptionAndExistingDestinations(t *testing.T) {
	root := fixture(t)
	backup := filepath.Join(t.TempDir(), "backup")
	m, err := Backup(context.Background(), root, backup)
	if err != nil {
		t.Fatal(err)
	}
	existing := t.TempDir()
	marker := filepath.Join(existing, "keep")
	os.WriteFile(marker, []byte("keep"), 0600)
	if err = Restore(context.Background(), backup, existing); err == nil {
		t.Fatal("overwrote destination")
	}
	if b, _ := os.ReadFile(marker); string(b) != "keep" {
		t.Fatal("existing data changed")
	}
	for _, f := range m.Files {
		if f.Key == "cookies.json" {
			os.WriteFile(filepath.Join(backup, f.Key), []byte("tampered"), 0600)
		}
	}
	destination := filepath.Join(t.TempDir(), "corrupt")
	if err = Restore(context.Background(), backup, destination); err == nil {
		t.Fatal("accepted corrupted backup")
	}
	if _, err = os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("published failed restore")
	}
}

func TestMaintenanceRefusesActiveWriter(t *testing.T) {
	root := fixture(t)
	lock, err := archiveguard.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = Check(context.Background(), root); err == nil {
		t.Fatal("checked active archive")
	}
	if _, err = Backup(context.Background(), root, filepath.Join(t.TempDir(), "backup")); err == nil {
		t.Fatal("backed up active writer")
	}
}

func TestDisappearanceReviewNeverRepairsOrigin(t *testing.T) {
	root := fixture(t)
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(`WITH RECURSIVE n(v) AS(SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<150) INSERT INTO danmaku(dmid,bvid,cid,first_seen_at,last_seen_at,status,deleted_at) SELECT v,'BVfixture',1,1,1,'deleted',42 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	report, err := Check(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DisappearanceReview) != 1 || report.DisappearanceReview[0].Count != 150 {
		t.Fatalf("missing review candidate: %+v", report.DisappearanceReview)
	}
	db, err := openReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM danmaku WHERE status='deleted' AND deleted_at=42`).Scan(&count); err != nil || count != 150 {
		t.Fatal("review changed origin", count, err)
	}
}
