package media

import (
	"bili-saver/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustRebuild(t *testing.T, b *Bridge) {
	t.Helper()
	r, e := b.RebuildAll()
	if e != nil || len(r.Errors) > 0 {
		t.Fatalf("rebuild: %+v %v", r, e)
	}
}
func outputFiles(t *testing.T, root, suffix string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && strings.HasSuffix(p, suffix) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestProjectionRemovesOnlyUnchangedOwnedFiles(t *testing.T) {
	b, st, root := setupBridge(t, "movie")
	mustRebuild(t, b)
	files := outputFiles(t, root, ".mp4")
	if len(files) != 1 {
		t.Fatal(files)
	}
	old := files[0]
	note := filepath.Join(filepath.Dir(old), "owner-notes.txt")
	if err := os.WriteFile(note, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	v, _ := st.GetVideo("BV1Test00001")
	v.Title = "Renamed"
	if err := st.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	mustRebuild(t, b)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale owned file retained")
	}
	if body, err := os.ReadFile(note); err != nil || string(body) != "keep" {
		t.Fatal("user file touched")
	}
	// Local modifications to a managed leaf are conflicts, never overwritten.
	current := outputFiles(t, root, ".nfo")[0]
	if err := os.WriteFile(current, []byte("manual edit"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := b.RebuildAll()
	if err != nil || len(r.Errors) == 0 {
		t.Fatal("manual modification accepted")
	}
	if body, _ := os.ReadFile(current); string(body) != "manual edit" {
		t.Fatal("manual content overwritten")
	}
}
func TestShowIdentityAndEpisodeNumbersSurviveOlderAddition(t *testing.T) {
	b, st, root := setupBridge(t, "show")
	mustRebuild(t, b)
	initial := outputFiles(t, root, ".mp4")
	if len(initial) != 1 || !strings.Contains(initial[0], "S01E01") {
		t.Fatal(initial)
	}
	original, _ := st.GetVideo("BV1Test00001")
	older := *original
	older.Bvid = "BVolder"
	older.Title = "Older"
	older.Pubdate -= 86400
	older.OwnerName = "Renamed owner"
	if err := st.UpsertVideo(&older); err != nil {
		t.Fatal(err)
	}
	mustRebuild(t, b)
	if _, err := os.Stat(initial[0]); err != nil {
		t.Fatal("existing episode renumbered or folder renamed", err)
	}
	files := outputFiles(t, root, ".mp4")
	found := false
	for _, p := range files {
		if strings.Contains(p, "BVolder") && strings.Contains(p, "S01E02") {
			found = true
		}
	}
	if !found {
		t.Fatal(files)
	}
}
func TestCollectionsRetainDeletedSourceProvenance(t *testing.T) {
	b, st, root := setupBridge(t, "collection")
	src := &store.Source{Type: "season", ExtID: "123", Title: "Original collection", Extra: "{}"}
	id, err := st.UpsertSource(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(`INSERT INTO source_videos VALUES(?,?)`, id, "BV1Test00001"); err != nil {
		t.Fatal(err)
	}
	mustRebuild(t, b)
	files := outputFiles(t, root, ".mp4")
	if len(files) != 1 || !strings.Contains(files[0], "Collections") {
		t.Fatal(files)
	}
	if err = st.DeleteSource(id); err != nil {
		t.Fatal(err)
	}
	collections, err := st.Collections("BV1Test00001")
	if err != nil || len(collections) != 1 {
		t.Fatalf("provenance lost: %v %v", collections, err)
	}
	mustRebuild(t, b)
}
func TestLibraryCannotOverlapOriginalsOrAnotherLibrary(t *testing.T) {
	b, st, _ := setupBridge(t, "movie")
	libs, _ := st.ListMediaLibraries()
	for _, root := range []string{b.Cfg.DataDir, filepath.Join(b.Cfg.DataDir, "raw", "out"), filepath.Join(libs[0].RootPath, "nested")} {
		if err := ValidateRoot(b.Cfg.DataDir, &store.MediaLibrary{ID: 999, RootPath: root}, libs); err == nil {
			t.Fatalf("accepted unsafe root %s", root)
		}
	}
}
func TestOutputAncestorSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Shows")); err != nil {
		t.Skip(err)
	}
	if _, err := outputPath(root, "Shows/entry.nfo"); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
}
