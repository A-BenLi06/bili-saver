package archiveops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bili-saver/internal/store"
)

func TestCheckSharedAssetRetainsPerRowIssues(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic media")
	for _, name := range []string{"a.bin", "z.bin"} {
		if err := os.WriteFile(filepath.Join(media, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	for i, row := range []struct {
		key, digest string
		size        int
	}{
		{"media/z.bin", "wrong-hash", len(body)},
		{"media/a.bin", hash, len(body) + 1},
		{"media/z.bin", hash, len(body)},
		{"media/z.bin", hash, len(body) - 1},
		{"media/a.bin", hash, len(body)},
	} {
		if err := st.AddAsset(&store.Asset{Bvid: fmt.Sprintf("BV%d", i), Role: "video", Origin: "bilibili", StorageKey: row.key, SHA256: row.digest, Size: int64(row.size)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Check(context.Background(), root)
	want := []Issue{
		{"asset", "media/z.bin", "asset checksum mismatch: media/z.bin"},
		{"asset", "media/a.bin", "asset size mismatch: media/a.bin"},
		{"asset", "media/z.bin", "asset size mismatch: media/z.bin"},
	}
	if err != nil || !report.DatabaseOK || report.FilesChecked != 5 || !reflect.DeepEqual(report.Issues, want) {
		t.Fatalf("check = %+v, %v, want issues %+v", report, err, want)
	}
}
