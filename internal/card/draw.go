package card

// 绘图基元。参数体系对齐 astrbot_plugin_bilibili_analysis 的 renderer.py：
// SDF 抗锯齿圆角矩形、椭圆柔光背景、玻璃卡（阴影+高光）、chip、文字排版。

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// Canvas 一张正在绘制的卡片画布。
type Canvas struct {
	*image.RGBA
	faces *faceCache
	icons *iconCache
}

func newCanvas(w, h int, faces *faceCache, icons *iconCache) *Canvas {
	return &Canvas{RGBA: image.NewRGBA(image.Rect(0, 0, w, h)), faces: faces, icons: icons}
}

// ---- 颜色 ----

func hexCol(s string) color.RGBA {
	var r, g, b uint8
	if len(s) == 7 {
		for i := 1; i < 7; i += 2 {
			v := hexVal(s[i])<<4 | hexVal(s[i+1])
			switch i {
			case 1:
				r = v
			case 3:
				g = v
			case 5:
				b = v
			}
		}
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func rgba(c color.Color) (fr, fg, fb, fa float64) {
	r, g, b, a := c.RGBA()
	return float64(r>>8) / 255, float64(g>>8) / 255, float64(b>>8) / 255, float64(a>>8) / 255
}

// withAlpha 返回同色不同 alpha 的颜色。
func withAlpha(col color.RGBA, a uint8) color.RGBA {
	col.A = a
	return col
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func blend(dst *image.RGBA, x, y int, sr, sg, sb, sa float64) {
	if x < dst.Rect.Min.X || x >= dst.Rect.Max.X || y < dst.Rect.Min.Y || y >= dst.Rect.Max.Y {
		return
	}
	i := dst.PixOffset(x, y)
	dr := float64(dst.Pix[i]) / 255
	dg := float64(dst.Pix[i+1]) / 255
	db := float64(dst.Pix[i+2]) / 255
	da := float64(dst.Pix[i+3]) / 255
	dst.Pix[i] = uint8(clamp01(sr*sa+dr*(1-sa)) * 255)
	dst.Pix[i+1] = uint8(clamp01(sg*sa+dg*(1-sa)) * 255)
	dst.Pix[i+2] = uint8(clamp01(sb*sa+db*(1-sa)) * 255)
	dst.Pix[i+3] = uint8(clamp01(sa+da*(1-sa)) * 255)
}

// ---- 形状 ----

// roundedCoverage 像素 (x,y)（像素中心 +0.5）对圆角矩形的覆盖度 [0,1]。
func roundedCoverage(x, y int, r image.Rectangle, radius float64) float64 {
	return clamp01(0.5 - sdfRound(float64(x)+0.5, float64(y)+0.5, r, radius))
}

// sdfRound 点到圆角矩形的带符号距离（负值在内部）。
func sdfRound(px, py float64, r image.Rectangle, radius float64) float64 {
	halfW := float64(r.Dx()) / 2
	halfH := float64(r.Dy()) / 2
	if radius > halfW {
		radius = halfW
	}
	if radius > halfH {
		radius = halfH
	}
	qx := math.Abs(px-(float64(r.Min.X)+halfW)) - (halfW - radius)
	qy := math.Abs(py-(float64(r.Min.Y)+halfH)) - (halfH - radius)
	dx := math.Max(qx, 0)
	dy := math.Max(qy, 0)
	return math.Hypot(dx, dy) + math.Min(math.Max(qx, qy), 0) - radius
}

// fillRound 纯色抗锯齿圆角矩形。
func (c *Canvas) fillRound(r image.Rectangle, radius float64, col color.Color) {
	fr, fg, fb, fa := rgba(col)
	c.fillRoundFn(r, radius, func(_, _ int) (float64, float64, float64, float64) {
		return fr, fg, fb, fa
	})
}

func (c *Canvas) fillRoundFn(r image.Rectangle, radius float64, colAt func(x, y int) (float64, float64, float64, float64)) {
	r = r.Intersect(c.Rect)
	if r.Empty() {
		return
	}
	radius = math.Max(0, radius)
	corner := int(radius) + 1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		nearEdgeY := y < r.Min.Y+corner || y >= r.Max.Y-corner
		for x := r.Min.X; x < r.Max.X; x++ {
			var cov float64 = 1
			if nearEdgeY || x < r.Min.X+corner || x >= r.Max.X-corner {
				cov = roundedCoverage(x, y, r, radius)
				if cov <= 0 {
					continue
				}
			}
			fr, fg, fb, fa := colAt(x, y)
			if cov < 1 {
				fr *= cov
				fg *= cov
				fb *= cov
				fa *= cov
			}
			blend(c.RGBA, x, y, fr, fg, fb, fa)
		}
	}
}

// strokeRound 圆角描边。
func (c *Canvas) strokeRound(r image.Rectangle, radius, outlineWidth float64, col color.Color) {
	fr, fg, fb, fa := rgba(col)
	inset := int(math.Ceil(outlineWidth))
	inner := image.Rect(r.Min.X+inset, r.Min.Y+inset, r.Max.X-inset, r.Max.Y-inset)
	innerRadius := math.Max(0, radius-outlineWidth)
	c.fillRoundFn(r, radius, func(x, y int) (float64, float64, float64, float64) {
		outer := roundedCoverage(x, y, r, radius)
		inn := roundedCoverage(x, y, inner, innerRadius)
		cov := clamp01(outer - inn)
		return fr * cov, fg * cov, fb * cov, fa * cov
	})
}

// drawShadow 玻璃卡投影：模糊圆角矩形的 SDF 近似（高斯模糊的覆盖度衰减）。
func (c *Canvas) drawShadow(r image.Rectangle, radius float64, col color.Color, offX, offY, blur int) {
	if blur <= 0 {
		return
	}
	fr, fg, fb, fa := rgba(col)
	if fa <= 0 {
		return
	}
	region := image.Rect(r.Min.X-blur*2, r.Min.Y-blur*2, r.Max.X+blur*2, r.Max.Y+blur*2).Intersect(c.Rect)
	shifted := image.Rect(r.Min.X+offX, r.Min.Y+offY, r.Max.X+offX, r.Max.Y+offY)
	fblur := float64(blur)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			d := sdfRound(float64(x)+0.5, float64(y)+0.5, shifted, radius)
			a := clamp01(0.5-d/fblur) * fa
			if a <= 0 {
				continue
			}
			blend(c.RGBA, x, y, fr*a, fg*a, fb*a, a)
		}
	}
}

// addGlow 椭圆柔光（对应参考实现的高斯模糊椭圆叠加），box 为椭圆外接矩形。
// strength 与参考一致取 0-255 的掩码值。
func (c *Canvas) addGlow(box image.Rectangle, col color.RGBA, blur int, strength float64) {
	region := image.Rect(box.Min.X-blur, box.Min.Y-blur, box.Max.X+blur, box.Max.Y+blur).Intersect(c.Rect)
	if region.Empty() {
		return
	}
	af := float64(col.A) / 255 * (strength / 255)
	cr := float64(col.R) / 255
	cg := float64(col.G) / 255
	cb := float64(col.B) / 255
	cx := float64(box.Min.X+box.Max.X) / 2
	cy := float64(box.Min.Y+box.Max.Y) / 2
	ax := float64(box.Dx()) / 2
	ay := float64(box.Dy()) / 2
	scaleInv := 1 / math.Min(ax, ay)
	fblur := float64(blur)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			px := float64(x) + 0.5
			py := float64(y) + 0.5
			nx := (px - cx) / ax
			ny := (py - cy) / ay
			d := (math.Hypot(nx, ny) - 1) * scaleInv
			a := clamp01(0.5-d/fblur) * af
			if a <= 0 {
				continue
			}
			i := c.PixOffset(x, y)
			dr := float64(c.Pix[i]) / 255
			dg := float64(c.Pix[i+1]) / 255
			db := float64(c.Pix[i+2]) / 255
			c.Pix[i] = uint8(clamp01(cr*a+dr*(1-a)) * 255)
			c.Pix[i+1] = uint8(clamp01(cg*a+dg*(1-a)) * 255)
			c.Pix[i+2] = uint8(clamp01(cb*a+db*(1-a)) * 255)
		}
	}
}

// ---- 图片 ----

// resizeImage 高质量缩放（CatmullRom）。
func resizeImage(src image.Image, w, h int) image.Image {
	if src == nil || w <= 0 || h <= 0 {
		return nil
	}
	b := src.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return src
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, b, xdraw.Src, nil)
	return out
}

// drawImageCover 把图片按 cover 方式铺进矩形（等比缩放、居中裁切），带圆角遮罩。
func (c *Canvas) drawImageCover(src image.Image, r image.Rectangle, radius float64) {
	if src == nil || r.Empty() {
		return
	}
	b := src.Bounds()
	scale := math.Max(float64(r.Dx())/float64(b.Dx()), float64(r.Dy())/float64(b.Dy()))
	cw := int(math.Ceil(float64(b.Dx()) * scale))
	ch := int(math.Ceil(float64(b.Dy()) * scale))
	scaled := resizeImage(src, cw, ch)
	if scaled == nil {
		return
	}
	layer := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	ox := (r.Dx() - cw) / 2
	oy := (r.Dy() - ch) / 2
	draw.Draw(layer, layer.Bounds(), scaled, image.Point{-ox, -oy}, draw.Src)
	if radius > 0 {
		for y := 0; y < r.Dy(); y++ {
			for x := 0; x < r.Dx(); x++ {
				cov := roundedCoverage(x, y, layer.Bounds(), radius)
				if cov >= 1 {
					continue
				}
				i := layer.PixOffset(x, y)
				layer.Pix[i] = uint8(float64(layer.Pix[i]) * cov)
				layer.Pix[i+1] = uint8(float64(layer.Pix[i+1]) * cov)
				layer.Pix[i+2] = uint8(float64(layer.Pix[i+2]) * cov)
				layer.Pix[i+3] = uint8(float64(layer.Pix[i+3]) * cov)
			}
		}
	}
	draw.Draw(c.RGBA, r, layer, image.Point{}, draw.Over)
}

// drawAvatar 圆角方形头像（x,y 为左上角）；src 为 nil 时画渐变首字母占位。
func (c *Canvas) drawAvatar(src image.Image, x, y, d int, name string) {
	r := image.Rect(x, y, x+d, y+d)
	if src != nil {
		c.drawImageCover(src, r, float64(d)/2)
		return
	}
	c.fillRoundFn(r, float64(d)/2, func(_, yy int) (float64, float64, float64, float64) {
		t := float64(yy-r.Min.Y) / math.Max(1, float64(d-1))
		tr, tg, tb, _ := rgba(hexCol("#8FD8FF"))
		br, bg, bb, _ := rgba(hexCol("#FFB6CE"))
		return tr + (br-tr)*t, tg + (bg-tg)*t, tb + (bb-tb)*t, 1
	})
	initial := "U"
	for _, ru := range normalizeText(name) {
		if ru != ' ' {
			initial = string(ru)
			break
		}
	}
	c.drawTextCenter(initial, x+d/2, y+d/2, weightBold, float64(d)*0.42, colorWhite)
}

// ---- 文字 ----

var colorWhite = color.RGBA{255, 255, 255, 255}

func (c *Canvas) face(w weight, size float64) font.Face { return c.faces.face(w, size) }

// textBaseline 垂直中心 cy 对应的文字基线。
func textBaseline(f font.Face, cy int) int {
	m := f.Metrics()
	return cy + int(math.Round(float64(m.Ascent-m.Descent)/(2*65536)))
}

// drawText 在 (x, baselineY) 画文字，返回宽度。
func (c *Canvas) drawText(s string, x, baselineY int, w weight, size float64, col color.Color) int {
	f := c.face(w, size)
	d := &font.Drawer{Dst: c.RGBA, Src: image.NewUniform(col), Face: f}
	d.Dot = fixed.P(x, baselineY)
	d.DrawString(s)
	return textW(f, s)
}

// textTop 以“文字行顶部”（ascent 顶）为锚点绘制，对应 PIL draw.text 的锚点语义。
func (c *Canvas) textTop(s string, x, yTop int, w weight, size float64, col color.Color) int {
	f := c.face(w, size)
	return c.drawText(s, x, yTop+int(f.Metrics().Ascent>>6), w, size, col)
}

// drawTextCenter 水平垂直居中绘制（cx/cy 为中心点）。
func (c *Canvas) drawTextCenter(s string, cx, cy int, w weight, size float64, col color.Color) int {
	f := c.face(w, size)
	tw := textW(f, s)
	return c.drawText(s, cx-tw/2, textBaseline(f, cy), w, size, col)
}

// measureString 测量字符串宽度（26.6 定点）。
func measureString(f font.Face, s string) fixed.Int26_6 {
	var total fixed.Int26_6
	prevR := rune(-1)
	for _, r := range s {
		if prevR >= 0 {
			total += f.Kern(prevR, r)
		}
		a, ok := f.GlyphAdvance(r)
		if !ok {
			continue
		}
		total += a
		prevR = r
	}
	return total
}

// runeWidth 单字符宽度。
func runeWidth(f font.Face, r rune) fixed.Int26_6 {
	a, _ := f.GlyphAdvance(r)
	return a
}

// pxToFixed 像素宽度转 26.6 定点。
func pxToFixed(px int) fixed.Int26_6 { return fixed.Int26_6(px << 6) }

// textW 文字宽度（像素）。
func textW(f font.Face, s string) int { return int(measureString(f, s).Ceil()) }

// textH 文字高度（ascent+descent，像素）。
func textH(f font.Face) int {
	m := f.Metrics()
	return int((m.Ascent + m.Descent) >> 6)
}

// wrapText 按最大宽度折行（CJK 逐字断行 + 拉丁空格优先），maxLines 截断加省略号。
func wrapText(f font.Face, s string, maxWidth, maxLines int) []string {
	s = normalizeText(s)
	if s == "" || maxLines <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	curW := fixed.Int26_6(0)
	lastSpace := -1
	truncated := false
	for _, r := range s {
		rw := runeWidth(f, r)
		if cur != "" && curW+rw > pxToFixed(maxWidth) {
			runes := []rune(cur)
			if lastSpace > 0 && lastSpace < len(runes) {
				lines = append(lines, string(runes[:lastSpace]))
				cur = string(runes[lastSpace:]) + string(r)
				curW = measureString(f, cur)
				lastSpace = -1
			} else {
				lines = append(lines, cur)
				cur = string(r)
				curW = rw
				lastSpace = -1
			}
			if len(lines) >= maxLines {
				truncated = true
				cur = ""
				break
			}
			continue
		}
		if r == ' ' {
			lastSpace = len([]rune(cur)) + 1
		}
		cur += string(r)
		curW += rw
	}
	if !truncated && cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	if len(lines) > 0 {
		last := lines[len(lines)-1]
		if truncated || measureString(f, last) > pxToFixed(maxWidth) {
			lines[len(lines)-1] = ellipsize(f, last, maxWidth)
		}
	}
	return lines
}

// ellipsize 截断并追加 "..."（与参考实现一致用三个点）。
func ellipsize(f font.Face, s string, maxWidth int) string {
	for runes := []rune(s); len(runes) > 0; runes = runes[:len(runes)-1] {
		cand := string(runes) + "..."
		if measureString(f, cand) <= pxToFixed(maxWidth) {
			return cand
		}
	}
	return "..."
}

// truncateToWidth 超宽时截断加省略号，否则原样返回。
func truncateToWidth(f font.Face, s string, maxWidth int) string {
	if textW(f, s) <= maxWidth {
		return s
	}
	return ellipsize(f, s, maxWidth)
}

// normalizeText 去控制字符（换行/制表折叠为空格）。
func normalizeText(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			if len(out) > 0 && out[len(out)-1] != ' ' {
				out = append(out, ' ')
			}
		case r >= 32:
			out = append(out, r)
		}
	}
	return string(out)
}

// ---- 组合件（对应参考 _draw_chip / _paste_glass_card） ----

// chipOpts chip 参数（零值时回落到参考实现默认口径）。
type chipOpts struct {
	IconName  string
	PadX      int
	PadY      int
	IconSize  int
	IconGap   int
	Radius    float64
	MinW      int
	MinH      int
	Fill      color.RGBA
	TextFill  color.RGBA
	IconColor color.RGBA
	Outline   color.RGBA // A=0 表示不描边
	Bold      bool
	Size      float64
}

func (o chipOpts) normalized() chipOpts {
	if o.PadX == 0 {
		o.PadX = 16
	}
	if o.PadY == 0 {
		o.PadY = 10
	}
	if o.IconGap == 0 {
		o.IconGap = 8
	}
	if o.Radius == 0 {
		o.Radius = 16
	}
	if o.Size == 0 {
		o.Size = 22
	}
	return o
}

func (o chipOpts) boldWeight() weight {
	if o.Bold {
		return weightBold
	}
	return weightRegular
}

// chipSize 预测量 chip 尺寸（对应 _measure_chip_size）。
func (c *Canvas) chipSize(text string, withIcon bool, o chipOpts) (w, h, iconSize int) {
	o = o.normalized()
	f := c.face(o.boldWeight(), o.Size)
	tw := textW(f, text)
	is := 0
	if withIcon {
		is = o.IconSize
		if is == 0 {
			is = 26
		}
	}
	w = o.PadX*2 + tw
	if withIcon {
		w += is + o.IconGap
	}
	if w < o.MinW {
		w = o.MinW
	}
	h = textH(f)
	if withIcon && is > h {
		h = is
	}
	h += o.PadY * 2
	if h < o.MinH {
		h = o.MinH
	}
	return w, h, is
}

// chip 绘制 chip，返回总宽。
func (c *Canvas) chip(x, y int, text string, o chipOpts) int {
	withIcon := o.IconName != ""
	w, h, is := c.chipSize(text, withIcon, o)
	r := image.Rect(x, y, x+w, y+h)
	c.fillRound(r, o.Radius, o.Fill)
	if o.Outline.A > 0 {
		c.strokeRound(r, o.Radius, 1, o.Outline)
	}
	weight := o.boldWeight()
	f := c.face(weight, o.Size)
	th := textH(f)
	cx := x + o.PadX
	if withIcon {
		if icon := c.icons.tintedIcon(o.IconName, is, o.IconColor); icon != nil {
			iy := y + (h-is)/2
			draw.Draw(c.RGBA, image.Rect(cx, iy, cx+is, iy+is), icon, image.Point{}, draw.Over)
		}
		cx += is + o.IconGap
	}
	c.textTop(text, cx, y+(h-th)/2, weight, o.Size, o.TextFill)
	return w
}

// glassCard 玻璃卡：投影 + 半透明白底 + 顶部高光（对应 _paste_glass_card，无边框模式）。
func (c *Canvas) glassCard(r image.Rectangle, radius int, fill color.RGBA, shadow color.RGBA, offX, offY, blur int) {
	c.drawShadow(r, float64(radius), shadow, offX, offY, blur)
	c.fillRound(r, float64(radius), fill)
	gloss := image.Rect(r.Min.X+1, r.Min.Y+1, r.Max.X-2, r.Min.Y+int(float64(r.Dy())*0.54))
	if gloss.Max.Y > gloss.Min.Y+10 {
		c.fillRound(gloss, math.Max(10, float64(radius-6)), color.RGBA{255, 255, 255, 40})
	}
}
