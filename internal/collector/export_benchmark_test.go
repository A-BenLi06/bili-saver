package collector

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func BenchmarkRepeatedDanmakuExport(b *testing.B) {
	dir := b.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	rows := make([]*store.Danmaku, 10000)
	for i := range rows {
		rows[i] = &store.Danmaku{Dmid: int64(i + 1), Bvid: "BVExport", Cid: 7, Timepoint: i * 10, Mode: 1, Fontsize: 25, Color: 16777215, Content: "弹幕导出 & <原文>", Ctime: 100, FirstSeenAt: 1, LastSeenAt: 1}
	}
	if err := st.UpsertDanmakuBatch(rows); err != nil {
		b.Fatal(err)
	}
	c := &Collector{St: st, Cfg: &config.Config{DataDir: dir}}
	for _, mode := range []string{"legacy", "stream"} {
		b.Run(mode, func(b *testing.B) {
			write := func() error { return c.writeDanmakuXML("BVExport", 7) }
			if mode == "legacy" {
				write = func() error { return legacyDanmakuExport(c, "BVExport", 7) }
			}
			if err := write(); err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(dir, "media", "BVExport", "danmaku", "full.xml")
			at := time.Unix(1000, 0)
			if err := os.Chtimes(path, at, at); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := write(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			fi, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			bytes := 0.0
			if !fi.ModTime().Equal(at) {
				bytes = float64(fi.Size())
			}
			b.ReportMetric(bytes, "output-bytes/op")
			var indexBytes int64
			if err := st.DB.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name='idx_danmaku_export'`).Scan(&indexBytes); err == nil {
				b.ReportMetric(float64(indexBytes), "index-bytes")
			}

		})
	}
}

// 保留本轮开始时的导出实现，便于同一机器/同一数据复核时间与写入量。
func legacyDanmakuExport(c *Collector, bvid string, cid int64) error {
	rows, err := c.St.ListDanmaku(cid, "")
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Timepoint < rows[j].Timepoint })
	type row struct {
		XMLName xml.Name
		P       string `xml:"p,attr"`
		Text    string `xml:",chardata"`
	}
	doc := struct {
		XMLName    xml.Name `xml:"i"`
		Chatserver string   `xml:"chatserver"`
		Chatid     string   `xml:"chatid"`
		Maxlimit   int      `xml:"maxlimit"`
		Rows       []row    `xml:"d"`
	}{Chatserver: "chat.bilibili.com", Chatid: strconv.FormatInt(cid, 10), Maxlimit: len(rows)}
	for _, r := range rows {
		p := fmt.Sprintf("%.5f,%d,%d,%d,%d,0,%d,0", float64(r.Timepoint)/1000, r.Mode, r.Fontsize, r.Color, r.Ctime, r.Dmid)
		doc.Rows = append(doc.Rows, row{P: p, Text: r.Content})
	}
	out, err := xml.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.pageDir(bvid, 0), "danmaku", "full.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), out...), 0644)
}
