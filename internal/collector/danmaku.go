package collector

// 弹幕抓取（分段 protobuf）、full.xml 兼容导出、CC 字幕下载入库。

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/dmproto"

	"bili-saver/internal/store"
)

// collectDanmaku 抓取一个分P的全部弹幕。返回 (本轮见到条数, 是否完整, 错误)。
// 优先分段接口（seg.so，现代、按 6 分钟分段）；不可用时回退整段 XML（list.so）。
func (c *Collector) collectDanmaku(ctx context.Context, bvid string, cid int64, durationSec int, syncStart int64, isNew bool) (int, bool, error) {
	const segLenSec = 360
	maxSeg := durationSec/segLenSec + 2
	if maxSeg < 1 {
		maxSeg = 1
	}
	total := 0

	saveElems := func(elems []dmproto.DanmakuElem) error {
		if len(elems) == 0 {
			return nil
		}
		ds := make([]*store.Danmaku, 0, len(elems))
		for _, e := range elems {
			ds = append(ds, &store.Danmaku{
				Dmid: e.ID, Bvid: bvid, Cid: cid,
				Timepoint: int(e.Progress), Mode: int(e.Mode),
				Fontsize: int(e.Fontsize), Color: int64(e.Color),
				Content: e.Content, Ctime: e.Ctime,
				FirstSeenAt: syncStart, LastSeenAt: syncStart,
			})
		}
		// 单事务批量落库（一段常上千条）；事务保证要么全入要么全不入，
		// total 只在成功后累加，保证"本轮见到数"= 实际持久化数（消失防御依赖）。
		if err := c.St.UpsertDanmakuBatch(ds); err != nil {
			return err
		}
		total += len(ds)
		return nil
	}
	saveRaw := func(name string, raw []byte) {
		if len(raw) == 0 {
			return
		}
		path := filepath.Join(c.pageDir(bvid, 0), "danmaku_raw", fmt.Sprintf("%d-%s", cid, name))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			c.Log.Warn("弹幕原始件落盘失败", "err", err)
		}
	}

	// ---- 路径一：分段 protobuf ----
	elems, raw, err := c.API.DanmakuSeg(ctx, cid, 1)
	if err == nil {
		saveRaw("seg-1.proto", raw)
		if err := saveElems(elems); err != nil {
			return total, false, err
		}
		for seg := 2; seg <= maxSeg; seg++ {
			elems, raw, err := c.API.DanmakuSeg(ctx, cid, seg)
			if err != nil {
				return total, false, fmt.Errorf("段 %d: %w", seg, err)
			}
			saveRaw(fmt.Sprintf("seg-%d.proto", seg), raw)
			if err := saveElems(elems); err != nil {
				return total, false, err
			}
			if len(elems) == 0 {
				break
			}
		}
		return total, true, nil
	}
	segErr := err

	// ---- 路径二：整段 XML 回退 ----
	xelems, xraw, xerr := c.API.DanmakuListXML(ctx, cid)
	if xerr != nil {
		return 0, false, fmt.Errorf("seg: %v; list.so: %w", segErr, xerr)
	}
	saveRaw("list-full.xml", xraw)
	if err := saveElems(xelems); err != nil {
		return total, false, err
	}
	return total, true, nil
}

// danmuXMLRow 是 full.xml 的 <d> 元素。
type danmuXMLRow struct {
	XMLName xml.Name
	P       string `xml:"p,attr"`
	Text    string `xml:",chardata"`
}

type danmuXMLDoc struct {
	XMLName    xml.Name      `xml:"i"`
	Chatserver string        `xml:"chatserver"`
	Chatid     string        `xml:"chatid"`
	Maxlimit   int           `xml:"maxlimit"`
	Rows       []danmuXMLRow `xml:"d"`
}

// writeDanmakuXML 从库中导出兼容 B 站格式的 full.xml。
func (c *Collector) writeDanmakuXML(bvid string, cid int64) error {
	rows, err := c.St.ListDanmaku(cid, "")
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Timepoint < rows[j].Timepoint })
	doc := danmuXMLDoc{Chatserver: "chat.bilibili.com", Chatid: strconv.FormatInt(cid, 10), Maxlimit: len(rows)}
	for _, r := range rows {
		p := fmt.Sprintf("%.5f,%d,%d,%d,%d,0,%d,0", float64(r.Timepoint)/1000, r.Mode, r.Fontsize, r.Color, r.Ctime, r.Dmid)
		doc.Rows = append(doc.Rows, danmuXMLRow{P: p, Text: r.Content})
	}
	out, err := xml.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.pageDir(bvid, 0), "danmaku", "full.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := append([]byte(xml.Header), out...)
	return os.WriteFile(path, body, 0o644)
}

// pageDir 返回 dataDir/media/{bvid}/p{n}；n<=0 时用根目录（不区分分P的数据放这里）。
func (c *Collector) pageDir(bvid string, n int) string {
	if n <= 0 {
		return filepath.Join(c.Cfg.DataDir, "media", sanitizePath(bvid))
	}
	return filepath.Join(c.Cfg.DataDir, "media", sanitizePath(bvid), fmt.Sprintf("p%d", n))
}

// collectSubtitles 抓取 CC 字幕列表并下载 bcc 原件。
func (c *Collector) collectSubtitles(ctx context.Context, bvid string, aid, cid int64) error {
	info, err := c.API.PlayerInfo(ctx, bvid, aid, cid)
	if err != nil {
		// 无登录时部分视频拿不到字幕列表，不视为致命
		return nil
	}
	for _, sub := range info.Subtitle.Subtitles {
		if sub.SubtitleURL == "" {
			continue
		}
		raw, err := c.API.SubtitleCC(ctx, sub.SubtitleURL)
		if err != nil {
			c.Log.Warn("字幕下载失败", "bvid", bvid, "lan", sub.Lan, "err", err)
			continue
		}
		dir := filepath.Join(c.pageDir(bvid, 0), "subs")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		name := fmt.Sprintf("%d-%s.bcc.json", cid, sanitizePath(sub.Lan))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return err
		}
		if err := c.St.UpsertSubtitle(&store.Subtitle{
			Bvid: bvid, Cid: cid, Lang: sub.Lan, LangDoc: sub.LanDoc,
			Origin: "bilibili", Format: "bcc", FilePath: path, CreatedAt: store.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

var reUnsafePath = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

func sanitizePath(s string) string {
	s = reUnsafePath.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._")
	if s == "" {
		s = "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return s
}

var _ = time.Now
