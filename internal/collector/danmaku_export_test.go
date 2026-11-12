package collector

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestDanmakuExportHistoryOrderAndUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, d := range []*store.Danmaku{
		{Dmid: 9, Bvid: "BVExport", Cid: 1, Timepoint: 1000, Content: "<原件> & 字符", Mode: 1, Fontsize: 25, Color: 16777215, Ctime: 100, FirstSeenAt: 1, LastSeenAt: 1},
		{Dmid: 7, Bvid: "BVExport", Cid: 1, Timepoint: 1000, Content: "已消失", Mode: 1, Fontsize: 25, Color: 16777215, Ctime: 100, FirstSeenAt: 1, LastSeenAt: 1},
		{Dmid: 3, Bvid: "BVExport", Cid: 1, Timepoint: 0, Content: "开头", Mode: 1, Fontsize: 25, Color: 16777215, Ctime: 100, FirstSeenAt: 1, LastSeenAt: 1},
	} {
		if err := st.UpsertDanmaku(d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB.Exec(`UPDATE danmaku SET status='deleted',deleted_at=2 WHERE dmid=7`); err != nil {
		t.Fatal(err)
	}
	c := &Collector{St: st, Cfg: &config.Config{DataDir: dir}}
	if err := c.writeDanmakuXML("BVExport", 1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "media", "BVExport", "danmaku", "full.xml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Chatid   string `xml:"chatid"`
		Maxlimit int    `xml:"maxlimit"`
		Rows     []struct {
			P    string `xml:"p,attr"`
			Text string `xml:",chardata"`
		} `xml:"d"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Chatid != "1" || doc.Maxlimit != 3 || len(doc.Rows) != 3 || doc.Rows[1].Text != "已消失" || doc.Rows[2].Text != "<原件> & 字符" {
		t.Fatalf("bad export: %+v", doc)
	}
	if !strings.HasPrefix(doc.Rows[0].P, "0.00000,") || !strings.HasPrefix(doc.Rows[1].P, "1.00000,") {
		t.Fatal("not ordered in seconds")
	}
	at := time.Unix(1000, 0)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE danmaku SET last_seen_at=100 WHERE cid=1`); err != nil {
		t.Fatal(err)
	}
	if err := c.writeDanmakuXML("BVExport", 1); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if !fi.ModTime().Equal(at) {
		t.Fatal("seen timestamp rewrote unchanged XML")
	}
	if err := c.writeDanmakuXML("BVExport", 99); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Maxlimit != 0 {
		t.Fatal("empty export count")
	}
}

func TestDanmakuPFormattingCompatibility(t *testing.T) {
	for _, ms := range []int{0, 1, -1, 999, 1000, 3600000, int(^uint(0) >> 1)} {
		d := &store.Danmaku{Timepoint: ms, Mode: 9, Fontsize: 25, Color: 16777215, Ctime: 1700000000, Dmid: 9223372036854775807}
		want := fmt.Sprintf("%.5f,%d,%d,%d,%d,0,%d,0", float64(d.Timepoint)/1000, d.Mode, d.Fontsize, d.Color, d.Ctime, d.Dmid)
		if got := string(appendDanmakuP(nil, d)); got != want {
			t.Fatalf("%d: %s != %s", ms, got, want)
		}
	}
}
