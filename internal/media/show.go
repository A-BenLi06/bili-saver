package media

import (
	"bili-saver/internal/store"
	"fmt"
)

// showPlan 只在单轮重建内使用：排序/集号/聚合 NFO 各计算一次，
// 不跨轮缓存，下一轮会自然看到新增视频、标签与原始元数据。
type showPlan struct {
	name          string
	first         *store.Video
	episodes      map[string]int
	nfo           []byte
	sharedWritten bool
}

func (b *Bridge) planShow(siblings []*store.Video, tags map[string][]string, format string) *showPlan {
	sortByID(siblings)
	p := &showPlan{first: siblings[0], episodes: make(map[string]int, len(siblings))}
	p.name = sanitizeFS(p.first.OwnerName)
	if p.name == "" {
		p.name = fmt.Sprintf("mid%d", p.first.OwnerMid)
	}
	for i, v := range siblings {
		p.episodes[v.Bvid] = i + 1
	}
	if format == "nfo" {
		p.nfo = []byte(b.showNFO(p.name, siblings, tags))
	}
	return p
}

func showArchivedAt(siblings []*store.Video) int64 {
	var first int64
	for _, v := range siblings {
		if v.FirstArchivedAt > 0 && (first == 0 || v.FirstArchivedAt < first) {
			first = v.FirstArchivedAt
		}
	}
	return first
}
