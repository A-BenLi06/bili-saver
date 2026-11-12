package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestReplaceFileFailurePreservesOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("disk write failure")
	err := replaceFile(path, func(out *os.File) error {
		if _, err := out.Write([]byte("partial")); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("lost original: %q %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leak: %v %v", entries, err)
	}
}

func BenchmarkRebuildShow(b *testing.B) {
	cfg := config.Defaults()
	cfg.DataDir = b.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	lib := &store.MediaLibrary{Name: "bench", RootPath: filepath.Join(cfg.DataDir, "library"), Layout: "show", LinkMode: "copy", MetadataFormat: "nfo", Enabled: true}
	if _, err := st.UpsertMediaLibrary(lib); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("BVbench%05d", i)
		dir := filepath.Join(cfg.DataDir, "media", id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("video"), 0644); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), make([]byte, 64<<10), 0644); err != nil {
			b.Fatal(err)
		}
		v := &store.Video{Bvid: id, Title: id, OwnerMid: 42, OwnerName: "Owner", Pubdate: int64(1700000000 + i), FirstArchivedAt: 1700000000, Status: "ok", Downloaded: true, LocalPath: filepath.Join("media", id, "video.mp4")}
		if err := st.UpsertVideo(v); err != nil {
			b.Fatal(err)
		}
		if err := st.AddTags(id, []string{"tag", "shared"}, "bilibili"); err != nil {
			b.Fatal(err)
		}
	}
	bridge := New(st, cfg, nil)
	if res, err := bridge.RebuildAll(); err != nil || len(res.Errors) > 0 {
		b.Fatalf("warmup: %+v %v", res, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res, err := bridge.RebuildAll(); err != nil || len(res.Errors) > 0 {
			b.Fatalf("rebuild: %+v %v", res, err)
		}
	}
}

// 第二轮重建必须保留实际文件时间；仅验证结果计数无法发现重复写盘。
func TestRebuildLeavesUnchangedFilesUntouched(t *testing.T) {
	b, _, root := setupBridge(t, "movie")
	if res, err := b.RebuildAll(); err != nil || len(res.Errors) != 0 {
		t.Fatalf("rebuild: %+v %v", res, err)
	}
	before := map[string]time.Time{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".nfo") || strings.HasSuffix(path, ".json") {
			// 固定旧时间，使测试不依赖文件系统时钟精度或 sleep。
			if err := os.Chtimes(path, time.Unix(100, 0), time.Unix(100, 0)); err != nil {
				return err
			}
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		before[path] = fi.ModTime()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := b.RebuildAll(); err != nil || len(res.Errors) != 0 {
		t.Fatalf("rebuild: %+v %v", res, err)
	}
	for path, ts := range before {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(ts) {
			t.Errorf("rewrote unchanged file %s", path)
		}
	}
}

func TestCopyRefreshAndMtime(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	for i, body := range []string{"first", "later"} {
		if err := os.WriteFile(src, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(int64(100+i), 0)
		if err := os.Chtimes(src, ts, ts); err != nil {
			t.Fatal(err)
		}
		if err := copyIfChanged(src, dst); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != body {
			t.Fatalf("copy: %q %v", got, err)
		}
		fi, err := os.Stat(dst)
		if err != nil || !fi.ModTime().Equal(ts) {
			t.Fatalf("mtime: %v %v", fi, err)
		}
	}
}

func TestMetadataFormatAndShowPlan(t *testing.T) {
	b, st, _ := setupBridge(t, "movie")
	v, err := st.GetVideo("BV1Test00001")
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"none", "json", "nfo"} {
		dir := filepath.Join(t.TempDir(), "entry")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		b.writeMeta(dir, format, b.gatherMeta(v), 1, "episode")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"none": 0, "json": 1, "nfo": 2}[format]
		if len(entries) != want {
			t.Fatalf("%s: files=%d want=%d", format, len(entries), want)
		}
		if format == "nfo" {
			if _, err := os.Stat(filepath.Join(dir, "episode.nfo")); err != nil {
				t.Fatal(err)
			}
		}
	}
	siblings := []*store.Video{
		{Bvid: "last", OwnerMid: 42, OwnerName: "Owner", Pubdate: 3, FirstArchivedAt: 200},
		{Bvid: "first", OwnerMid: 42, OwnerName: "Owner", Pubdate: 1, FirstArchivedAt: 100},
		{Bvid: "second", OwnerMid: 42, OwnerName: "Owner", Pubdate: 2, FirstArchivedAt: 300},
	}
	tags := map[string][]string{"first": {"z", "a", "a"}, "second": {"z", "a"}}
	p := b.planShow(siblings, tags, "nfo")
	if p.first.Bvid != "first" || p.episodes["last"] != 3 {
		t.Fatalf("wrong plan: %+v", p)
	}
	for i := 0; i < 10; i++ {
		if next := b.planShow(siblings, tags, "nfo"); string(next.nfo) != string(p.nfo) {
			t.Fatal("show metadata must be deterministic")
		}
	}
	if !strings.Contains(string(p.nfo), "<tag>a</tag>") || strings.Index(string(p.nfo), "<tag>a</tag>") > strings.Index(string(p.nfo), "<tag>z</tag>") {
		t.Fatalf("tag order: %s", p.nfo)
	}
}
