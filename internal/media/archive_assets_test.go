package media

import (
	"bili-saver/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditDownloadedGoneVideoIsExported(t *testing.T) {
	b, st, _ := setupBridge(t, "movie")
	if e := st.MarkVideoGone("BV1Test00001", "gone"); e != nil {
		t.Fatal(e)
	}
	r, e := b.RebuildAll()
	if e != nil {
		t.Fatal(e)
	}
	if r.Created != 1 {
		t.Fatalf("downloaded gone video omitted: created=%d", r.Created)
	}
}
func TestAuditSTRMExtraReferencesOwnPart(t *testing.T) {
	b, _, _ := setupBridge(t, "movie")
	dir := t.TempDir()
	dst := filepath.Join(dir, "P2.mp4")
	v := &store.Video{Bvid: "BVtest", LocalPath: "media/BVtest/p1/BVtest-p1.mp4"}
	lib := &store.MediaLibrary{LinkMode: "strm"}
	if e := b.emitVideo(dst, filepath.Join(b.Cfg.DataDir, "media", "BVtest", "p2", "BVtest-p2.mp4"), v, lib); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(dir, "P2.strm"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(data), "/p2/") {
		t.Fatalf("P2 stream points to wrong part: %s", data)
	}
}
func TestAuditMainSubtitleDoesNotUseOtherCID(t *testing.T) {
	b, st, _ := setupBridge(t, "movie")
	dir := filepath.Join(b.Cfg.DataDir, "media", "subtitle-fixture")
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	v, e := st.GetVideo("BV1Test00001")
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 2; i++ {
		if e = st.UpsertPage(&store.Page{Bvid: v.Bvid, Cid: int64(i), Page: i, FirstSeenAt: store.Now(), LastSeenAt: store.Now()}); e != nil {
			t.Fatal(e)
		}
	}
	for i, s := range []string{"part-one", "part-two-has-a-different-size"} {
		p := filepath.Join(dir, s+".srt")
		if e = os.WriteFile(p, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
		if e = st.UpsertSubtitle(&store.Subtitle{Bvid: v.Bvid, Cid: int64(i + 1), Lang: "en", Origin: "bilibili", Format: "srt", FilePath: p, CreatedAt: store.Now()}); e != nil {
			t.Fatal(e)
		}
	}
	rows, err := st.ListSubtitles(v.Bvid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range rows {
		t.Logf("subtitle cid=%d path=%s", sub.Cid, sub.FilePath)
	}
	if e := b.linkSubtitles(dir, "main", v, "copy"); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(dir, "main.en.srt"))
	if e != nil {
		t.Fatal(e)
	}
	if string(data) != "part-one" {
		t.Fatalf("main subtitle overwritten by another CID: %q", data)
	}
}
