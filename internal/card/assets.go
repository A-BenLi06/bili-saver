package card

// 内嵌资源：鸿蒙字体与 B 站风格图标（取自本地参考项目 astrbot_plugin_bilibili_analysis 的
// assets 目录，HarmonyOS Sans 按《HarmonyOS 字体许可协议》免费商用/再分发）。
// 卡片属于 derived 层产物，渲染只用这些离线资源 + 库内 origin 数据。

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

//go:embed assets/fonts/HarmonyOS_Sans_SC_Bold.ttf
var fontBoldFS embed.FS

//go:embed assets/fonts/HarmonyOS_Sans_SC_Regular.ttf
var fontRegularFS embed.FS

//go:embed assets/icons
var iconsFS embed.FS

type weight int

const (
	weightRegular weight = iota
	weightBold
)

var (
	fontParseOnce  sync.Once
	fontRegularTTF *opentype.Font
	fontBoldTTF    *opentype.Font
)

func loadFonts() (*opentype.Font, *opentype.Font) {
	fontParseOnce.Do(func() {
		b, err := fontBoldFS.ReadFile("assets/fonts/HarmonyOS_Sans_SC_Bold.ttf")
		if err == nil {
			fontBoldTTF, err = opentype.Parse(b)
		}
		if err != nil {
			panic("card: 解析粗体字体失败: " + err.Error())
		}
		r, err := fontRegularFS.ReadFile("assets/fonts/HarmonyOS_Sans_SC_Regular.ttf")
		if err == nil {
			fontRegularTTF, err = opentype.Parse(r)
		}
		if err != nil {
			panic("card: 解析常规字体失败: " + err.Error())
		}
	})
	return fontRegularTTF, fontBoldTTF
}

// faceCache 按字号缓存 face（同一张卡片里大量复用几种字号）。
type faceCache struct {
	mu    sync.Mutex
	faces map[faceKey]font.Face
}

type faceKey struct {
	weight weight
	size   float64
}

func newFaceCache() *faceCache { return &faceCache{faces: map[faceKey]font.Face{}} }

func (c *faceCache) face(w weight, size float64) font.Face {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := faceKey{w, size}
	if f, ok := c.faces[key]; ok {
		return f
	}
	reg, bold := loadFonts()
	src := reg
	if w == weightBold {
		src = bold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		panic("card: 创建字体 face 失败: " + err.Error())
	}
	c.faces[key] = f
	return f
}

// iconCache 图标解码 + 单色化缓存。
type iconCache struct {
	mu   sync.Mutex
	raw  map[string]image.Image
	tint map[iconKey]*image.RGBA
}

type iconKey struct {
	name string
	col  color.Color
}

func newIconCache() *iconCache {
	return &iconCache{raw: map[string]image.Image{}, tint: map[iconKey]*image.RGBA{}}
}

// icon 取原始图标（已缩放到 size）。
func (c *iconCache) icon(name string, size int) image.Image {
	c.mu.Lock()
	defer c.mu.Unlock()
	im := c.rawLocked(name)
	if im == nil {
		return nil
	}
	return resizeImage(im, size, size)
}

// rawLocked 解码并缓存原始图标（调用方必须已持有 mu）。
func (c *iconCache) rawLocked(name string) image.Image {
	if im, ok := c.raw[name]; ok {
		return im
	}
	b, err := iconsFS.ReadFile("assets/icons/" + name + ".png")
	if err != nil {
		return nil
	}
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	c.raw[name] = im
	return im
}

// tintedIcon 取单色化图标：保留 alpha 剪影，填充指定颜色。
// 输出严格合法的 premultiplied 像素（RGB ≤ A），否则合成器会产生溢出杂色。
func (c *iconCache) tintedIcon(name string, size int, col color.Color) *image.RGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := iconKey{name, col}
	if im, ok := c.tint[key]; ok {
		return im
	}
	raw := resizeImage(c.rawLocked(name), size, size)
	if raw == nil {
		return nil
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	tr, tg, tb, _ := col.RGBA()
	tR, tG, tB := uint8(tr>>8), uint8(tg>>8), uint8(tb>>8)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			_, _, _, a := raw.At(x, y).RGBA()
			a8 := uint8(a >> 8)
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(uint32(tR) * uint32(a8) / 255),
				G: uint8(uint32(tG) * uint32(a8) / 255),
				B: uint8(uint32(tB) * uint32(a8) / 255),
				A: a8,
			})
		}
	}
	c.tint[key] = out
	return out
}
