package card

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func rasterFixture(dense bool) *cardData {
	d := &cardData{
		Video: &store.Video{
			Bvid: "BV1TestCard01", Title: "档案预览 / Archive preview",
			OwnerName: "示例作者", Duration: 539, PagesCount: 2, Pubdate: 1720000000,
		},
		GeneratedAt: 1780000000,
	}
	if dense {
		cover := image.NewRGBA(image.Rect(0, 0, 320, 180))
		draw.Draw(cover, cover.Bounds(), image.NewUniform(color.RGBA{120, 170, 220, 255}), image.Point{}, draw.Src)
		d.Cover = cover
		d.Avatar = cover
		d.Video.Title = strings.Repeat("档案预览 / Archive preview ", 7)
		d.Video.Description = strings.Repeat("用于验证海报生成的合成样本。", 20)
		d.Stats = &store.StatsSnapshot{View: 135789, Like: 256, Reply: 82, Danmaku: 540}
		d.Tags = []string{"旅行", "春天", "生活记录", "示例", "自制", "归档"}
		d.DanmakuVis, d.CommentsVis, d.CommentsDel = 1200, 450, 18
		d.Comments = []commentView{
			{Name: "甲", Face: cover, Text: strings.Repeat("合成测试评论。", 15), Likes: 120, When: "一天前", Where: "上海"},
			{Name: "乙", Text: "第二条评论。", Likes: 20, When: "昨天"},
		}
	}
	return d
}

func BenchmarkRenderRaster(b *testing.B) {
	for _, name := range []string{"minimal", "dense"} {
		b.Run(name, func(b *testing.B) {
			d := rasterFixture(name == "dense")
			faces, icons := newFaceCache(), newIconCache()
			renderCard(d, faces, icons)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				renderCard(d, faces, icons)
			}
		})
	}
}
