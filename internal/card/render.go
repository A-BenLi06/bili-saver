package card

// 卡片排版：完整对齐 astrbot_plugin_bilibili_analysis renderer.py 的海报参数体系
// （960 宽、字体层级、chip/玻璃卡/分区标题的尺寸与间距），数据换成 bili-saver 的
// 归档语义：封面统计 → 标题/UP主/简介/标签 → 视频数据 → 热门评论 → 归档信息 → 二维码落款。
//
// 布局与绘制共用同一组测量结果（layoutCard），杜绝预估高度与实际绘制错位。

import (
	"image"
	"image/color"
	"image/draw"
	"strconv"

	"github.com/skip2/go-qrcode"
	xdraw "golang.org/x/image/draw"
)

// ---- 视觉令牌（与参考实现一致） ----

var (
	colAccent     = hexCol("#00AEEC")
	colAccentDeep = hexCol("#008AC5")
	colPink       = hexCol("#FB7299")
	colTextMain   = hexCol("#18191C")
	colTextSub    = hexCol("#61666D")
	colTextLight  = hexCol("#9499A0")
	colPinkSoft   = hexCol("#FFE6EF")
	colBlueSoft   = hexCol("#E9F7FF")
	colRedSoft    = hexCol("#FDEBEC")
	colRedDeep    = hexCol("#DC2626")
	colOrangeSoft = hexCol("#FFF4E5")
	colOrangeDeep = hexCol("#D97706")
)

// 卡面填充（半透明白，叠在柔光背景上形成轻玻璃层级）。
var (
	surfaceCover         = color.RGBA{255, 255, 255, 220}
	surfacePrimary       = color.RGBA{255, 255, 255, 240}
	surfaceSecondarySoft = color.RGBA{255, 255, 255, 248}
	surfaceEmphasis      = color.RGBA{255, 255, 255, 252}
	chipBrandFill        = color.RGBA{210, 239, 252, 244}
	chipPinkFill         = color.RGBA{255, 214, 228, 244}
	chipBoardFill        = color.RGBA{255, 255, 255, 248}
	pillFill             = color.RGBA{15, 24, 36, 150}
)

// ---- 布局常量（与参考实现同名常量一一对应） ----

const (
	posterW     = 960
	paddingX    = 28
	contentW    = posterW - paddingX*2 // 904
	coverRadius = 28
	sectionR    = 22
	chipR       = 16
	sectGap     = 18
	statCardGap = 12
)

// 字号层级（对应 get_font(size, weight)）。
const (
	fsTitle      = 48.0 // 标题 bold
	fsH1         = 30.0 // UP主名 bold
	fsH2         = 26.0 // 分区标题 bold
	fsBody       = 25.0 // 简介 regular
	fsSmall      = 22.0 // 标签
	fsMini       = 18.0 // 信息卡标签 / 落款次行
	fsOwnerMeta  = 19.0 // UP主统计胶囊 bold
	fsHeaderChip = 28.0 // 头部 chip bold
	fsStatVal    = 30.0 // 数据卡数值 bold
	fsFooterVal  = 22.0 // 信息卡数值 bold
	fsProto      = 20.0 // 小卡数值 bold
	fsCmtName    = 26.0 // 评论昵称 bold
	fsCmtBody    = 24.0 // 评论正文 bold
	fsMaskVal    = 22.0 // 封面胶囊 bold
)

const (
	ownerAvatarSize = 80
	cmtAvatarSize   = 64
	tagRowStep      = 44 // INFO_TAG_ROW_STEP
)

// renderCard 渲染整卡。
func renderCard(d *cardData, faces *faceCache, icons *iconCache) *image.RGBA {
	l := layoutCard(d, faces, icons)
	c := newCanvas(posterW, l.totalH, faces, icons)
	drawBackground(c, l.totalH)

	y := drawHeader(c, d, 14)
	y = drawCover(c, d, y)
	y = drawInfoCard(c, d, l, y)
	y = drawStats(c, d, y)
	y = drawComments(c, d, l, y)
	y = drawArchiveInfo(c, d, y)
	drawFooter(c, d, y)
	return c.RGBA
}

// ---- 布局测量 ----

type commentLayout struct {
	lines    []string
	lineH    int
	likeW    int
	likeH    int
	nameRowH int
	metaH    int
	headerH  int // 卡顶 padding 到 header 底
	cardH    int
}

type tagChip struct {
	text string
	x, w int
}

type cardLayout struct {
	totalH     int
	titleLines []string
	descLines  []string
	tagRows    [][]tagChip
	titleH     int
	ownerH     int
	descH      int
	tagH       int
	infoH      int
	comments   []commentLayout
}

// layoutCard 用与绘制完全相同的口径预计算所有折行与分区高度。
func layoutCard(d *cardData, faces *faceCache, icons *iconCache) *cardLayout {
	m := newCanvas(8, 8, faces, icons) // 仅借用 face/icon 缓存做测量
	l := &cardLayout{}

	// 标题（48b，宽 content-56，最多 3 行，行距 8）
	tf := m.face(weightBold, fsTitle)
	l.titleLines = wrapText(tf, orDefault(d.Video.Title, "（无标题）"), contentW-56, 3)
	l.titleH = len(l.titleLines)*int(fsTitle) + maxi(0, len(l.titleLines)-1)*8

	// 简介（25，行距 8；占位文本跳过）
	if descText := effectiveDesc(d.Video.Description); descText != "" {
		l.descLines = wrapText(m.face(weightRegular, fsBody), descText, contentW-56, 4)
		l.descH = len(l.descLines)*int(fsBody) + maxi(0, len(l.descLines)-1)*8
	}

	// 标签（22，chip pad 14/8，最多 6 个，行步进 44）
	if len(d.Tags) > 0 {
		tagf := m.face(weightRegular, fsSmall)
		n := mini(len(d.Tags), 6)
		maxW := contentW - 56
		var row []tagChip
		x := 0
		for _, t := range d.Tags[:n] {
			text := "#" + t
			if textW(tagf, text) > maxW-28 {
				text = truncateToWidth(tagf, text, maxW-28)
			}
			w, _, _ := m.chipSize(text, false, tagChipOpts())
			if x+w > maxW && len(row) > 0 {
				l.tagRows = append(l.tagRows, row)
				row = nil
				x = 0
			}
			row = append(row, tagChip{text: text, x: x, w: w})
			x += w + 10
		}
		if len(row) > 0 {
			l.tagRows = append(l.tagRows, row)
		}
		l.tagH = len(l.tagRows) * tagRowStep
	}

	// UP 主区：名字行 + 8 + 胶囊行(34)，与 80px 头像取最大
	nameRowH := maxi(textH(m.face(weightBold, fsH1)), 32)
	textBlock := nameRowH + 8 + 34
	l.ownerH = maxi(ownerAvatarSize, textBlock)

	// 信息主卡总高：18 + 标题 + 12 + UP主 + (10+简介) + (10+标签) + 18
	l.infoH = 18 + l.titleH + 12 + l.ownerH + 18
	if l.descH > 0 {
		l.infoH += 10 + l.descH
	}
	if l.tagH > 0 {
		l.infoH += 10 + l.tagH
	}

	// 热门评论（最多 2 条）
	for i := range d.Comments {
		if i >= 2 {
			break
		}
		cm := &d.Comments[i]
		var cl commentLayout
		cl.nameRowH = maxi(textH(m.face(weightBold, fsCmtName)), 32)
		cl.metaH = textH(m.face(weightRegular, fsMini))
		likeOpt := likeChipOpts()
		cl.likeW, cl.likeH, _ = m.chipSize(formatNumber(cm.Likes), true, likeOpt)
		textX := 24 + cmtAvatarSize + 14
		bodyW := maxi(180, contentW-24-textX-cl.likeW-12)
		cl.lines = wrapText(m.face(weightBold, fsCmtBody), cm.Text, bodyW, 4)
		cl.lineH = int(fsCmtBody)
		headerStack := cl.nameRowH + 4 + cl.metaH
		cl.headerH = 18 + maxi(cmtAvatarSize, headerStack)
		textH := len(cl.lines)*cl.lineH + maxi(0, len(cl.lines)-1)*8
		bodyH := maxi(maxi(textH, cl.lineH), cl.likeH)
		cl.cardH = cl.headerH + 8 + bodyH + 18
		if cl.cardH < 168 {
			cl.cardH = 168
		}
		l.comments = append(l.comments, cl)
	}

	// 总高（与各 draw 函数的推进完全一致）
	h := 14
	h += headerChipH(m) + 14
	h += contentW*9/16 + sectGap
	h += l.infoH + sectGap
	h += (26 + 14 + 122) + sectGap
	if len(l.comments) > 0 {
		h += 26 + 14
		for _, cl := range l.comments {
			h += cl.cardH + 16
		}
		h += sectGap
	}
	h += (26 + 14 + 76 + statCardGap + 72) + sectGap
	h += 190 + paddingX
	l.totalH = h
	return l
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// effectiveDesc 有效简介（参考实现同样过滤占位文本）。
func effectiveDesc(s string) string {
	t := normalizeText(s)
	switch t {
	case "", "-", "--", "无", "暂无简介", "未填写":
		return ""
	}
	return t
}

// ---- 背景（移植 _create_bili_gradient_background） ----

func drawBackground(c *Canvas, h int) {
	draw.Draw(c.RGBA, c.Rect, image.NewUniform(colorWhite), image.Point{}, draw.Src)
	c.addGlow(image.Rect(-220, -200, posterW*52/100, 560), color.RGBA{239, 249, 255, 255}, 136, 154)
	c.addGlow(image.Rect(posterW*48/100, -200, posterW+220, 560), color.RGBA{255, 239, 246, 255}, 142, 150)
	c.addGlow(image.Rect(-160, h-840, posterW*38/100, h-180), color.RGBA{246, 252, 255, 255}, 148, 44)
	c.addGlow(image.Rect(posterW*62/100, h-820, posterW+160, h-160), color.RGBA{255, 246, 250, 255}, 152, 42)
	c.addGlow(image.Rect(120, 180, posterW-100, mini(980, h)), color.RGBA{255, 255, 255, 255}, 170, 42)
	// 顶部渐变带：左(241,248,255) → 右(255,241,247)，强度随行号衰减
	bandH := mini(900, h)
	span := float64(maxi(posterW-1, 1))
	for y := 0; y < bandH; y++ {
		ratio := float64(y) / float64(maxi(bandH-1, 1))
		mask := (42.0 * (1 - ratio)) / 255
		if mask <= 0 {
			continue
		}
		lr, lg, lb := 241.0+(255-241)*ratio, 248.0+(255-248)*ratio, 255.0
		rr, rg, rb := 255.0, 241.0+(255-241)*ratio, 247.0+(255-247)*ratio
		for x := 0; x < posterW; x++ {
			xr := float64(x) / span
			i := c.PixOffset(x, y)
			c.Pix[i] = uint8(clamp01((lr+(rr-lr)*xr)/255*mask+float64(c.Pix[i])/255*(1-mask)) * 255)
			c.Pix[i+1] = uint8(clamp01((lg+(rg-lg)*xr)/255*mask+float64(c.Pix[i+1])/255*(1-mask)) * 255)
			c.Pix[i+2] = uint8(clamp01((lb+(rb-lb)*xr)/255*mask+float64(c.Pix[i+2])/255*(1-mask)) * 255)
		}
	}
}

// ---- 分区标题（移植 _draw_section_title：双圆点 + 26 粗体） ----

func sectionTitle(c *Canvas, y int, text string) int {
	f := c.face(weightBold, fsH2)
	cy := y + textH(f)/2
	if icon := c.icons.tintedIcon("solid_circle", 12, colPink); icon != nil {
		draw.Draw(c.RGBA, image.Rect(paddingX, cy-6, paddingX+12, cy+6), icon, image.Point{}, draw.Over)
	}
	if icon := c.icons.tintedIcon("solid_circle", 10, colAccent); icon != nil {
		draw.Draw(c.RGBA, image.Rect(paddingX+14, cy-5, paddingX+24, cy+5), icon, image.Point{}, draw.Over)
	}
	c.textTop(text, paddingX+32, y, weightBold, fsH2, colTextMain)
	return y + int(fsH2) + 14
}

// ---- 头部 ----

func headerBrandOpts() chipOpts {
	return chipOpts{
		IconName: "topbar_logo", IconSize: 42, PadX: 18, PadY: 8, Radius: 16,
		Fill: chipBrandFill, TextFill: colAccent, IconColor: colAccent,
		Bold: true, Size: fsHeaderChip, Outline: withAlpha(colAccent, 110),
	}
}

func headerChipH(c *Canvas) int {
	_, h, _ := c.chipSize("bili-saver", true, headerBrandOpts())
	return h
}

func drawHeader(c *Canvas, d *cardData, y int) int {
	v := d.Video
	hh := headerChipH(c)
	c.chip(paddingX, y, "bili-saver", headerBrandOpts())

	// 右侧：状态 chip + BV chip（白板）
	statusText, statusFill, statusTextCol := archiveStatus(v.Status)
	so := chipOpts{Fill: statusFill, TextFill: statusTextCol, Bold: true, Size: fsHeaderChip, PadX: 18, PadY: 8, Radius: 16}
	sw, _, _ := c.chipSize(statusText, false, so)
	c.chip(posterW-paddingX-sw, y, statusText, so)

	bo := chipOpts{Fill: chipBoardFill, TextFill: colTextMain, Bold: true, Size: fsHeaderChip, PadX: 18, PadY: 8, Radius: 16}
	bw, _, _ := c.chipSize(v.Bvid, false, bo)
	c.chip(posterW-paddingX-sw-8-bw, y, v.Bvid, bo)
	return y + hh + 14
}

// archiveStatus 状态徽章（已归档用参考实现的粉色 chip，异常态转红/橙）。
func archiveStatus(status string) (text string, fill, textCol color.RGBA) {
	switch status {
	case "ok", "":
		return "已归档", chipPinkFill, colPink
	case "gone":
		return "已消失", colRedSoft, colRedDeep
	case "private":
		return "仅自己可见", colOrangeSoft, colOrangeDeep
	default:
		return status, colOrangeSoft, colOrangeDeep
	}
}

// ---- 封面 ----

func coverPillOpts(iconName string, iconCol color.RGBA) chipOpts {
	return chipOpts{
		IconName: iconName, IconSize: 28, PadX: 16, PadY: 10, Radius: chipR,
		Fill: pillFill, TextFill: colorWhite, IconColor: iconCol,
		Bold: true, Size: fsMaskVal, Outline: color.RGBA{255, 255, 255, 56},
	}
}

func drawCover(c *Canvas, d *cardData, y int) int {
	v := d.Video
	h := contentW * 9 / 16
	box := image.Rect(paddingX, y, paddingX+contentW, y+h)
	c.glassCard(box, coverRadius, surfaceCover, withAlpha(colAccentDeep, 18), 0, 14, 24)
	inner := box.Inset(6)
	if d.Cover != nil {
		c.drawImageCover(d.Cover, inner, coverRadius-6)
	} else {
		c.fillRound(inner, coverRadius-6, hexCol("#333333"))
	}

	pillY := inner.Max.Y - 18 - (10*2 + 28)
	x := inner.Min.X + 18
	if d.Stats != nil {
		x += c.chip(x, pillY, formatNumber(d.Stats.View), coverPillOpts("views", colAccent)) + 12
		x += c.chip(x, pillY, formatNumber(d.Stats.Danmaku), coverPillOpts("bullet", colAccent)) + 12
	}
	if v.Duration > 0 {
		text := formatDuration(v.Duration)
		w, _, _ := c.chipSize(text, true, coverPillOpts("time", colPink))
		c.chip(inner.Max.X-18-w, pillY, text, coverPillOpts("time", colPink))
	}
	if v.PagesCount > 1 {
		text := "共" + strconv.Itoa(v.PagesCount) + "个P"
		w, _, _ := c.chipSize(text, false, coverPillOpts("", colPink))
		c.chip(inner.Max.X-18-w, inner.Min.Y+18, text, coverPillOpts("", colPink))
	}
	return y + h + sectGap
}

// ---- 信息主卡 ----

func tagChipOpts() chipOpts {
	return chipOpts{Fill: colPinkSoft, TextFill: colPink, Size: fsSmall, PadX: 14, PadY: 8, Radius: chipR}
}

func ownerMetaChipOpts(icon string, blue bool) chipOpts {
	if blue {
		return chipOpts{
			IconName: icon, IconSize: 18, PadX: 12, PadY: 6, Radius: 14, MinH: 34,
			Fill: colBlueSoft, TextFill: colAccentDeep, IconColor: colAccent,
			Bold: true, Size: fsOwnerMeta, Outline: withAlpha(colAccent, 82),
		}
	}
	return chipOpts{
		IconName: icon, IconSize: 18, PadX: 12, PadY: 6, Radius: 14, MinH: 34,
		Fill: colPinkSoft, TextFill: colPink, IconColor: colPink,
		Bold: true, Size: fsOwnerMeta, Outline: withAlpha(colPink, 82),
	}
}

func drawInfoCard(c *Canvas, d *cardData, l *cardLayout, y int) int {
	v := d.Video
	box := image.Rect(paddingX, y, paddingX+contentW, y+l.infoH)
	c.glassCard(box, sectionR, surfacePrimary, withAlpha(colPink, 14), 0, 8, 18)

	infoX := box.Min.X + 28
	// 标题
	cursor := box.Min.Y + 14
	for _, line := range l.titleLines {
		c.textTop(line, infoX, cursor, weightBold, fsTitle, colTextMain)
		cursor += int(fsTitle) + 8
	}

	// UP 主：头像 + 名字行 + 统计胶囊行
	ownerY := box.Min.Y + 18 + l.titleH + 12
	c.drawAvatar(d.Avatar, infoX, ownerY, ownerAvatarSize, v.OwnerName)

	nameX := infoX + ownerAvatarSize + 14
	nameF := c.face(weightBold, fsH1)
	nameRowH := maxi(textH(nameF), 32)
	name := truncateToWidth(nameF, orDefault(v.OwnerName, "未知UP主"), contentW-56-ownerAvatarSize-14-260)
	c.drawText(name, nameX, textBaseline(nameF, ownerY+nameRowH/2), weightBold, fsH1, colPink)

	// 统计胶囊（蓝粉交替，对齐参考 _build_owner_meta_item）
	metaY := ownerY + nameRowH + 8
	mx := nameX
	maxChipsW := box.Max.X - 28
	addChip := func(text, icon string, blue bool) {
		opt := ownerMetaChipOpts(icon, blue)
		w, _, _ := c.chipSize(text, true, opt)
		if mx+w > maxChipsW {
			return
		}
		mx += c.chip(mx, metaY, text, opt) + 10
	}
	addChip("mid "+strconv.FormatInt(v.OwnerMid, 10), "owner_video", true)
	if v.Pubdate > 0 {
		addChip("发布 "+relativeTime(v.Pubdate, d.GeneratedAt), "time", false)
	}
	if v.FirstArchivedAt > 0 {
		addChip("归档 "+relativeTime(v.FirstArchivedAt, d.GeneratedAt), "info_publish", true)
	}

	// 简介
	cursor = ownerY + l.ownerH
	if l.descH > 0 {
		cursor += 10
		for _, line := range l.descLines {
			c.textTop(line, infoX, cursor, weightRegular, fsBody, colTextSub)
			cursor += int(fsBody) + 8
		}
	}

	// 标签
	if len(l.tagRows) > 0 {
		cursor += 10
		for _, row := range l.tagRows {
			for _, ch := range row {
				c.chip(infoX+ch.x, cursor, ch.text, tagChipOpts())
			}
			cursor += tagRowStep
		}
	}
	return box.Max.Y + sectGap
}

// ---- 视频数据（5 卡：点赞/投币/收藏/评论/分享，与参考一致） ----

func drawStats(c *Canvas, d *cardData, y int) int {
	y = sectionTitle(c, y, "视频数据")
	statW := (contentW - statCardGap*4) / 5
	statH := 122
	type tile struct {
		icon string
		val  string
	}
	var tiles []tile
	if d.Stats != nil {
		tiles = []tile{
			{"like", formatNumber(d.Stats.Like)},
			{"coin", formatNumber(d.Stats.Coin)},
			{"favorite", formatNumber(d.Stats.Favorite)},
			{"comment", formatNumber(d.Stats.Reply)},
			{"share", formatNumber(d.Stats.Share)},
		}
	} else {
		for _, name := range []string{"like", "coin", "favorite", "comment", "share"} {
			tiles = append(tiles, tile{icon: name, val: "-"})
		}
	}
	for i, t := range tiles {
		x := paddingX + i*(statW+statCardGap)
		r := image.Rect(x, y, x+statW, y+statH)
		c.glassCard(r, sectionR, surfaceSecondarySoft, withAlpha(colPink, 14), 0, 5, 12)
		if icon := c.icons.tintedIcon(t.icon, 56, colPink); icon != nil {
			ix := x + (statW-56)/2
			draw.Draw(c.RGBA, image.Rect(ix, y+18, ix+56, y+74), icon, image.Point{}, draw.Over)
		}
		c.drawTextCenter(t.val, x+statW/2, y+94, weightBold, fsStatVal, colTextMain)
	}
	return y + statH + sectGap
}

// ---- 热门评论 ----

func likeChipOpts() chipOpts {
	return chipOpts{
		IconName: "like", Fill: colPinkSoft, TextFill: colPink, IconColor: colPink,
		Size: fsProto, Bold: true, PadX: 10, PadY: 6, Radius: chipR,
		IconSize: 30, MinW: 84, MinH: 42,
	}
}

func drawComments(c *Canvas, d *cardData, l *cardLayout, y int) int {
	if len(l.comments) == 0 {
		return y
	}
	y = sectionTitle(c, y, "热门评论")
	for i := range l.comments {
		cl := &l.comments[i]
		cm := &d.Comments[i]
		cardTop := y
		cardRect := image.Rect(paddingX, cardTop, paddingX+contentW, cardTop+cl.cardH)
		fill := surfaceSecondarySoft
		if i == 0 {
			fill = surfaceEmphasis
		}
		c.glassCard(cardRect, sectionR, fill, withAlpha(colPink, 14), 0, 5, 12)

		avatarX := cardRect.Min.X + 24
		avatarY := cardTop + 18
		c.drawAvatar(cm.Face, avatarX, avatarY, cmtAvatarSize, cm.Name)

		contentX := avatarX + cmtAvatarSize + 14
		nameF := c.face(weightBold, fsCmtName)
		nameRowTop := avatarY + maxi(0, (cmtAvatarSize-(cl.nameRowH+4+cl.metaH))/2)
		name := truncateToWidth(nameF, cm.Name, maxi(140, cardRect.Max.X-24-contentX-90))
		c.drawText(name, contentX, textBaseline(nameF, nameRowTop+cl.nameRowH/2), weightBold, fsCmtName, colPink)

		meta := cm.When
		if cm.Where != "" {
			meta += " · " + cm.Where
		}
		c.textTop(meta, contentX, nameRowTop+cl.nameRowH+4, weightRegular, fsMini, colTextSub)

		// 点赞胶囊（与最后一行正文垂直居中）
		likeX := cardRect.Max.X - 24 - cl.likeW
		textStart := cardTop + cl.headerH + 8
		lastLineTop := textStart + maxi(0, len(cl.lines)-1)*(cl.lineH+8)
		likeY := lastLineTop + maxi(0, (cl.lineH-cl.likeH)/2)
		c.chip(likeX, likeY, formatNumber(cm.Likes), likeChipOpts())

		// 正文
		textCursor := textStart
		for _, line := range cl.lines {
			c.textTop(line, contentX, textCursor, weightBold, fsCmtBody, colTextMain)
			textCursor += cl.lineH + 8
		}
		y = cardTop + cl.cardH + 16
	}
	return y + sectGap
}

// ---- 归档信息（视觉对齐参考“视频信息”区） ----

func drawArchiveInfo(c *Canvas, d *cardData, y int) int {
	v := d.Video
	y = sectionTitle(c, y, "归档信息")

	// 两张大卡：发布时间 / 首次归档
	detailW := (contentW - statCardGap) / 2
	detailH := 76
	pubText := formatFullDate(v.Pubdate)
	if v.Pubdate > 0 {
		pubText += " / " + relativeTime(v.Pubdate, d.GeneratedAt)
	}
	archText := formatFullDate(v.FirstArchivedAt)
	if v.FirstArchivedAt > 0 {
		archText += " / " + relativeTime(v.FirstArchivedAt, d.GeneratedAt)
	}
	type infoCard struct {
		label, value, icon string
	}
	details := []infoCard{
		{"发布时间/距今", pubText, "info_publish"},
		{"首次归档/距今", archText, "time"},
	}
	for i, item := range details {
		x := paddingX + i*(detailW+statCardGap)
		r := image.Rect(x, y, x+detailW, y+detailH)
		c.glassCard(r, sectionR, surfaceSecondarySoft, withAlpha(colPink, 18), 0, 6, 12)
		drawLabelWithIcon(c, item.label, item.icon, x+18, y+14, detailW-36, 18, fsMini)
		vf := c.face(weightBold, fsFooterVal)
		c.textTop(truncateToWidth(vf, item.value, detailW-36), x+18, y+38, weightBold, fsFooterVal, colTextMain)
	}

	// 四张小卡：评论区 / 弹幕 / CC 字幕 / 本地媒体
	specY := y + detailH + statCardGap
	specW := (contentW - statCardGap*3) / 4
	specH := 72
	comments := strconv.Itoa(d.CommentsVis) + " 现存"
	if d.CommentsDel > 0 {
		comments += " · " + strconv.Itoa(d.CommentsDel) + " 消失"
	}
	subs := strconv.Itoa(d.SubtitleCnt) + " 条"
	if d.SubtitleCnt == 0 {
		subs = "无 CC"
	}
	media := "仅元数据"
	if v.Downloaded {
		media = "已下载"
	}
	specs := []infoCard{
		{"评论区", comments, "comment"},
		{"弹幕", strconv.Itoa(d.DanmakuVis) + " 现存", "bullet"},
		{"CC 字幕", subs, "info_audio"},
		{"本地媒体", media, "info_transfer"},
	}
	for i, item := range specs {
		x := paddingX + i*(specW+statCardGap)
		r := image.Rect(x, specY, x+specW, specY+specH)
		c.glassCard(r, sectionR, surfaceSecondarySoft, withAlpha(colPink, 12), 0, 4, 9)
		drawLabelWithIcon(c, item.label, item.icon, x+14, specY+12, specW-28, 16, fsMini)
		size, value := fitFontValue(c, item.value, []float64{fsProto, 18, 16}, specW-28)
		c.textTop(value, x+14, specY+38, weightBold, size, colTextMain)
	}
	return specY + specH + sectGap
}

// drawLabelWithIcon 粉色小标签 + 右侧图标（对应 _draw_card_label_with_icon）。
func drawLabelWithIcon(c *Canvas, label, icon string, x, y, maxW, iconSize int, size float64) {
	f := c.face(weightRegular, size)
	label = truncateToWidth(f, label, maxi(0, maxW-iconSize-8))
	c.textTop(label, x, y, weightRegular, size, colPink)
	if icon == "" {
		return
	}
	if im := c.icons.tintedIcon(icon, iconSize, colPink); im != nil {
		ix := x + textW(f, label) + 8
		if limit := x + maxW - iconSize; ix > limit {
			ix = limit
		}
		iy := y + (textH(f)-iconSize)/2
		if iy < y {
			iy = y
		}
		draw.Draw(c.RGBA, image.Rect(ix, iy, ix+iconSize, iy+iconSize), im, image.Point{}, draw.Over)
	}
}

// fitFontValue 从候选字号里选第一个能放下文本的，返回字号与截断后文本（_pick_font_that_fits）。
func fitFontValue(c *Canvas, s string, sizes []float64, maxW int) (float64, string) {
	for _, sz := range sizes {
		f := c.face(weightBold, sz)
		if textW(f, s) <= maxW {
			return sz, s
		}
	}
	sz := sizes[len(sizes)-1]
	f := c.face(weightBold, sz)
	return sz, truncateToWidth(f, s, maxW)
}

// ---- 落款：二维码（分享入口，视觉与整体一致） ----

func drawFooter(c *Canvas, d *cardData, y int) {
	v := d.Video
	const h = 190
	r := image.Rect(paddingX, y, paddingX+contentW, y+h)
	c.glassCard(r, sectionR, surfaceSecondarySoft, withAlpha(colPink, 14), 0, 5, 12)

	const qrSize = 150
	qrRect := image.Rect(r.Min.X+20, y+20, r.Min.X+20+qrSize, y+20+qrSize)
	c.fillRound(qrRect, 12, colorWhite)
	if qr, err := qrcode.New("https://www.bilibili.com/video/"+v.Bvid, qrcode.Medium); err == nil {
		qr.DisableBorder = true
		const quiet = 8
		inner := qrRect.Inset(quiet)
		bm := qr.Image(0)
		out := image.NewRGBA(image.Rect(0, 0, inner.Dx(), inner.Dy()))
		xdraw.NearestNeighbor.Scale(out, out.Bounds(), bm, bm.Bounds(), xdraw.Src, nil)
		draw.Draw(c.RGBA, inner, out, image.Point{}, draw.Over)
	}

	tx := r.Min.X + 20 + qrSize + 24
	line1 := "扫码回原视频 · bili-saver 补档存证"
	if v.Status == "gone" {
		line1 = "源视频已消失 · 扫码验证当前状态"
	}
	c.textTop(line1, tx, y+46, weightBold, 26, colTextMain)
	c.textTop("生成于 "+formatFullDate(d.GeneratedAt)+" · 数据为归档时点快照", tx, y+92, weightRegular, fsMini, colTextSub)
	c.textTop("www.bilibili.com/video/"+v.Bvid, tx, y+126, weightRegular, fsMini, colTextLight)
}
