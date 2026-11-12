package collector

import (
	"bufio"
	"context"
	"encoding/xml"
	"io"
	"path/filepath"
	"strconv"

	"bili-saver/internal/fileio"
	"bili-saver/internal/store"
)

// writeDanmakuXML 流式导出，Go 内存不随弹幕总数增长；内容不变不写盘。
func (c *Collector) writeDanmakuXML(bvid string, cid int64) error {
	return c.writeDanmakuXMLContext(context.Background(), bvid, cid)
}

func (c *Collector) writeDanmakuXMLContext(ctx context.Context, bvid string, cid int64) error {
	path := filepath.Join(c.pageDir(bvid, 0), "danmaku", "full.xml")
	_, err := fileio.Write(path, func(w io.Writer) error {
		// 逐条 EncodeElement 会强制 flush；EncodeToken 保持固定缓冲批量 I/O。
		buffer := bufio.NewWriterSize(w, 64*1024)
		if _, err := io.WriteString(buffer, xml.Header); err != nil {
			return err
		}
		enc := xml.NewEncoder(buffer)
		enc.Indent("", "  ")
		root := xml.StartElement{Name: xml.Name{Local: "i"}}
		if err := enc.EncodeToken(root); err != nil {
			return err
		}
		rowStart := xml.StartElement{Name: xml.Name{Local: "d"}, Attr: []xml.Attr{{Name: xml.Name{Local: "p"}}}}
		rowEnd := rowStart.End()
		var pbuf [128]byte
		err := c.St.StreamDanmakuExport(ctx, cid, func(count int) error {
			for _, field := range []struct {
				name  string
				value any
			}{
				{"chatserver", "chat.bilibili.com"}, {"chatid", strconv.FormatInt(cid, 10)}, {"maxlimit", count},
			} {
				if err := enc.EncodeElement(field.value, xml.StartElement{Name: xml.Name{Local: field.name}}); err != nil {
					return err
				}
			}
			return nil
		}, func(d *store.Danmaku) error {
			rowStart.Attr[0].Value = string(appendDanmakuP(pbuf[:0], d))
			if err := enc.EncodeToken(rowStart); err != nil {
				return err
			}
			if err := enc.EncodeToken(xml.CharData(d.Content)); err != nil {
				return err
			}
			return enc.EncodeToken(rowEnd)
		})
		if err != nil {
			return err
		}
		if err := enc.EncodeToken(root.End()); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		return buffer.Flush()
	})
	return err
}

// 保持旧 p 属性的固定五位秒数，复用数字缓冲，避免每行 printf 装箱/解析格式。
func appendDanmakuP(buf []byte, d *store.Danmaku) []byte {
	buf = strconv.AppendFloat(buf, float64(d.Timepoint)/1000, 'f', 5, 64)
	for _, n := range [4]int64{int64(d.Mode), int64(d.Fontsize), d.Color, d.Ctime} {
		buf = append(buf, ',')
		buf = strconv.AppendInt(buf, n, 10)
	}
	buf = append(buf, ",0,"...)
	buf = strconv.AppendInt(buf, d.Dmid, 10)
	return append(buf, ",0"...)
}
