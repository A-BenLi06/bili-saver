package media

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"bili-saver/internal/store"
)

// ---- NFO 生成（Kodi 规范；Emby/Jellyfin 兼容；只写 origin 层数据） ----

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// genreTags 前 N 个原生标签作类型（genre），全部标签作 tag。
func (m *vmeta) genreTags() string {
	var b strings.Builder
	n := 0
	for _, t := range m.biliTags {
		if n >= 5 {
			break
		}
		b.WriteString(fmt.Sprintf("\n  <genre>%s</genre>", xmlEscape(t)))
		n++
	}
	return b.String()
}

func (m *vmeta) tagTags() string {
	var b strings.Builder
	for _, t := range m.biliTags {
		b.WriteString(fmt.Sprintf("\n  <tag>%s</tag>", xmlEscape(t)))
	}
	return b.String()
}

// ownerActor UP主 actor 块（含头像 thumb）。
func (m *vmeta) ownerActor() string {
	role := "UP主"
	return fmt.Sprintf(`  <actor>
    <name>%s</name>
    <role>%s</role>
    <order>0</order>
    <thumb>%s</thumb>
  </actor>`, xmlEscape(m.v.OwnerName), xmlEscape(role), xmlEscape(m.v.OwnerFace))
}

// ratings 块：Jellyfin/Emby/Kodi(19+) 通用的具名评分（点赞率 0-10）。
func (m *vmeta) ratingsXML() string {
	r, votes := m.rating()
	return fmt.Sprintf(`  <ratings>
    <rating name="bilibili" default="true" max="10">
      <value>%.2f</value>
      <votes>%d</votes>
    </rating>
  </ratings>
  <rating>%.2f</rating>
  <votes>%d</votes>`, r, votes, r, votes)
}

func (m *vmeta) setXML() string {
	if m.setName == "" {
		return ""
	}
	return fmt.Sprintf("\n  <set>%s</set>", xmlEscape(m.setName))
}

func movieNFO(m *vmeta) string {
	v := m.v
	pub := time.Unix(v.Pubdate, 0)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>%s</title>
  <originaltitle>%s</originaltitle>
  <sorttitle>%s</sorttitle>
  <tagline>哔哩哔哩补档 · UP主 %s</tagline>
  <outline>%s</outline>
  <plot>%s</plot>%s
  <premiered>%s</premiered>
  <year>%d</year>
  <runtime>%d</runtime>
  <dateadded>%s</dateadded>
  <uniqueid type="bilibili" default="true">%s</uniqueid>
  <uniqueid type="bilibili-aid">%d</uniqueid>
  <id>%s</id>%s%s
  <studio>%s</studio>
  <studio>bilibili</studio>
  <director>%s</director>
  <credits>%s</credits>
%s
  <thumb aspect="poster" preview="%s">%s</thumb>
  <fanart>
    <thumb>%s</thumb>
  </fanart>
  <country>中国大陆</country>
  <source>bilibili</source>
</movie>
`,
		xmlEscape(v.Title), xmlEscape(v.Title), xmlEscape(v.Title),
		xmlEscape(v.OwnerName),
		xmlEscape(m.outline()), xmlEscape(m.plot()), m.setXML(),
		pub.Format("2006-01-02"), pub.Year(), m.runtimeMin(),
		time.Unix(v.FirstArchivedAt, 0).Format("2006-01-02 15:04:05"),
		v.Bvid, v.Aid, v.Bvid,
		m.genreTags(), m.tagTags(),
		xmlEscape(v.OwnerName), xmlEscape(v.OwnerName), xmlEscape(v.OwnerName),
		m.ratingsXML(),
		xmlEscape(v.Pic), xmlEscape(v.Pic), xmlEscape(v.Pic))
}

// showNFO UP主剧集级元数据（聚合全部投稿：数量/时间跨度/高频标签）。
func (b *Bridge) showNFO(show string, siblings []*store.Video, tagCache map[string][]string) string {
	count := len(siblings)
	first, last := int64(0), int64(0)
	tagFreq := map[string]int{}
	for _, v := range siblings {
		if first == 0 || v.Pubdate < first {
			first = v.Pubdate
		}
		if v.Pubdate > last {
			last = v.Pubdate
		}
		seen := map[string]bool{}
		for _, t := range tagCache[v.Bvid] {
			if seen[t] {
				continue
			}
			seen[t] = true
			tagFreq[t]++
		}
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(tagFreq))
	for k, v := range tagFreq {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v == list[j].v {
			return list[i].k < list[j].k
		}
		return list[i].v > list[j].v
	})
	var genres, tags strings.Builder
	n := 0
	for _, e := range list {
		tags.WriteString(fmt.Sprintf("\n  <tag>%s</tag>", xmlEscape(e.k)))
		if n < 5 {
			genres.WriteString(fmt.Sprintf("\n  <genre>%s</genre>", xmlEscape(e.k)))
			n++
		}
	}
	ownerName, ownerMid, ownerFace := show, int64(0), ""
	if len(siblings) > 0 {
		ownerName = siblings[0].OwnerName
		ownerMid = siblings[0].OwnerMid
		ownerFace = siblings[0].OwnerFace
	}
	plot := fmt.Sprintf("%s 的哔哩哔哩投稿归档：共 %d 个视频，发布于 %s 至 %s。（由 bili-saver 补档站生成）",
		ownerName, count, time.Unix(first, 0).Format("2006-01-02"), time.Unix(last, 0).Format("2006-01-02"))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
  <title>%s</title>
  <originaltitle>%s</originaltitle>
  <tagline>哔哩哔哩补档 · %d 个视频</tagline>
  <outline>%s</outline>
  <plot>%s</plot>%s
  <premiered>%s</premiered>
  <year>%d</year>
  <studio>%s</studio>
  <studio>bilibili</studio>
  <uniqueid type="bilibili-mid" default="true">%d</uniqueid>
  <id>%d</id>%s
  <actor>
    <name>%s</name>
    <role>UP主</role>
    <order>0</order>
    <thumb>%s</thumb>
  </actor>
  <dateadded>%s</dateadded>
  <source>bilibili</source>
</tvshow>
`,
		xmlEscape(ownerName), xmlEscape(ownerName), count,
		xmlEscape(truncateRunes(plot, 120)), xmlEscape(plot), genres.String(),
		time.Unix(first, 0).Format("2006-01-02"),
		time.Unix(first, 0).Year(),
		xmlEscape(ownerName), ownerMid, ownerMid,
		tags.String(),
		xmlEscape(ownerName), xmlEscape(ownerFace),
		time.Unix(showArchivedAt(siblings), 0).Format("2006-01-02 15:04:05"))
}

func episodeNFO(m *vmeta, ep int) string {
	v := m.v
	pub := time.Unix(v.Pubdate, 0)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<episodedetails>
  <title>%s</title>
  <originaltitle>%s</originaltitle>
  <plot>%s</plot>
  <outline>%s</outline>
  <season>1</season>
  <episode>%d</episode>
  <aired>%s</aired>
  <runtime>%d</runtime>
  <dateadded>%s</dateadded>
  <uniqueid type="bilibili" default="true">%s</uniqueid>
  <uniqueid type="bilibili-aid">%d</uniqueid>
  <id>%s</id>%s%s
  <studio>%s</studio>
  <studio>bilibili</studio>
  <director>%s</director>
  <credits>%s</credits>
%s
  <thumb aspect="preview" preview="%s">%s</thumb>
  <country>中国大陆</country>
  <source>bilibili</source>
</episodedetails>
`,
		xmlEscape(v.Title), xmlEscape(v.Title),
		xmlEscape(m.plot()), xmlEscape(m.outline()),
		ep, pub.Format("2006-01-02"), m.runtimeMin(),
		time.Unix(v.FirstArchivedAt, 0).Format("2006-01-02 15:04:05"),
		v.Bvid, v.Aid, v.Bvid,
		m.genreTags(), m.tagTags(),
		xmlEscape(v.OwnerName), xmlEscape(v.OwnerName), xmlEscape(v.OwnerName),
		m.ratingsXML(),
		xmlEscape(v.Pic), xmlEscape(v.Pic))
}
