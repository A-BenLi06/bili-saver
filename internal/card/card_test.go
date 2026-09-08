package card

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func newTestGen(t *testing.T) (*Generator, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults()
	cfg.DataDir = dir
	g := New(st, cfg, nil)
	return g, st, dir
}

func seedVideo(t *testing.T, st *store.Store, dir string) {
	t.Helper()
	v := &store.Video{
		Bvid: "BV1TestCard01", Aid: 42,
		Title:       "骑台铃苍穹带女朋友送外卖是种什么体验？这是一段很长的标题用来测试折行与省略号逻辑是否正常工作",
		Description: "周末没事干？在家打游戏？不如骑车带女朋友赚点外快。这是一段用来测试多行简介排版与自动折行的说明文字，长度故意超过三行。第二行内容。第三行内容。第四行内容。第五行应该被截断。",
		Pic:         "",
		OwnerMid:    946974, OwnerName: "示例UP主",
		OwnerFace: "",
		Pubdate:   store.Now() - 7*86400,
		Duration:  309, PagesCount: 2,
		Status: "ok", FirstArchivedAt: store.Now() - 86400,
	}
	if err := st.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendStats(&store.StatsSnapshot{
		Bvid: v.Bvid, Ts: store.Now(),
		View: 105_000_000, Danmaku: 1200, Reply: 34,
		Favorite: 8, Coin: 10, Share: 1, Like: 1222,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTags(v.Bvid, []string{"骑行", "搞笑", "短片", "原创", "生活记录", "测评"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	// 评论/弹幕/字幕计数（前两条带昵称，作为热门评论渲染）
	for i := int64(1); i <= 3; i++ {
		name := ""
		if i <= 2 {
			name = "热评用户"
		}
		if err := st.UpsertComment(&store.Comment{
			Rpid: i, Bvid: v.Bvid, Content: "这条评论是热门评论，用来验证评论区排版是否与参考实现一致。",
			MemberName: name, LikeCount: int(10 - i), Ctime: 1, FirstSeenAt: 1, LastSeenAt: 2,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertComment(&store.Comment{Rpid: 99, Bvid: v.Bvid, Content: "消失的评论", FirstSeenAt: 1, LastSeenAt: 1}); err == nil {
		_, _ = st.DB.Exec(`UPDATE comments SET status='deleted', deleted_at=3 WHERE rpid=99`)
	}
	for i := int64(1); i <= 5; i++ {
		if err := st.UpsertDanmaku(&store.Danmaku{Dmid: i, Bvid: v.Bvid, Cid: 7, Content: "弹幕", FirstSeenAt: 1, LastSeenAt: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertSubtitle(&store.Subtitle{Bvid: v.Bvid, Cid: 7, Lang: "zh-Hans", Origin: "bilibili", Format: "bcc", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// 本地封面（ 32x32 纯色 JPEG -> 用 PNG 也可以，解码器都注册了）
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 120, B: 160, A: 255})
		}
	}
	coverDir := filepath.Join(dir, "media", v.Bvid)
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(coverDir, "cover.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestRenderCardToFile(t *testing.T) {
	g, st, dir := newTestGen(t)
	seedVideo(t, st, dir)

	path, err := g.RenderToFile(context.Background(), "BV1TestCard01")
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, fmtName, err := image.Decode(f)
	if err != nil {
		t.Fatalf("输出不是合法图片: %v", err)
	}
	if fmtName != "png" {
		t.Fatalf("应为 PNG，实际 %s", fmtName)
	}
	b := im.Bounds()
	if b.Dx() != posterW {
		t.Fatalf("宽度应为 %d，实际 %d", posterW, b.Dx())
	}
	if b.Dy() < 1200 || b.Dy() > 3200 {
		t.Fatalf("高度异常: %d", b.Dy())
	}
	// 背景应是渐变而非纯色：采样顶部与底部像素
	top := im.At(b.Dx()/2, 2)
	bottom := im.At(b.Dx()/2, b.Dy()-3)
	if top == bottom {
		t.Fatal("背景应存在竖向渐变")
	}
	t.Logf("卡片尺寸: %dx%d, 路径: %s", b.Dx(), b.Dy(), path)

	// 二次调用应幂等（Generate 命中缓存）
	p2, err := g.Generate(context.Background(), "BV1TestCard01")
	if err != nil {
		t.Fatal(err)
	}
	if p2 != path {
		t.Fatal("Generate 应复用已生成的卡片")
	}
}

func TestFormatNumber(t *testing.T) {
	cases := map[int]string{
		0: "0", 999: "999", 662: "662",
		10000: "1万", 105_000_000: "1.05亿", 12345: "1.2万",
		1200: "1200",
	}
	for in, want := range cases {
		if got := formatNumber(in); got != want {
			t.Errorf("formatNumber(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(309); got != "05:09" {
		t.Errorf("formatDuration(309) = %q", got)
	}
	if got := formatDuration(3725); got != "1:02:05" {
		t.Errorf("formatDuration(3725) = %q", got)
	}
}

func TestRelativeTime(t *testing.T) {
	now := int64(10_000_000)
	if got := relativeTime(now-30, now); got != "刚刚" {
		t.Errorf("got %q", got)
	}
	if got := relativeTime(now-3600, now); got != "1小时前" {
		t.Errorf("got %q", got)
	}
	if got := relativeTime(now-7*86400, now); got != "7天前" {
		t.Errorf("got %q", got)
	}
}

func TestWrapTextEllipsis(t *testing.T) {
	g, _, _ := newTestGen(t)
	_ = g
	faces := newFaceCache()
	c := newCanvas(8, 8, faces, newIconCache())
	f := c.face(weightRegular, fsBody)
	long := "这是一段非常长的中文文本" +
		"用于验证折行逻辑在超长时可以正确截断并且能够在末行正确添加省略号" +
		"另外补一段内容确保超过三行的容量限制"
	lines := wrapText(f, long, 300, 3)
	if len(lines) != 3 {
		t.Fatalf("应折成 3 行，实际 %d 行: %q", len(lines), lines)
	}
	last := lines[2]
	if len(last) < 3 || last[len(last)-3:] != "..." {
		t.Fatalf("末行应以省略号结尾: %q", last)
	}
	for _, l := range lines {
		if measureString(f, l) > pxToFixed(300) {
			t.Fatalf("行超宽: %q", l)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("BV1xx411c7mD"); got != "BV1xx411c7mD" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeName(`a/b\c:d`); got != "abcd" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeName(""); got != "unknown" {
		t.Errorf("got %q", got)
	}
}
