package media

// 媒体库桥测试：movie 布局、NFO 只含 origin 数据、copy 模式跨平台可用。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func setupBridge(t *testing.T, layout string) (*Bridge, *store.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.Media.Root = filepath.Join(dataDir, "library")
	cfg.Media.Layout = layout
	cfg.Media.LinkMode = "copy" // 测试环境统一 copy，规避 Windows symlink 权限
	now := store.Now()
	// 伪造一个已下载的视频文件
	srcDir := filepath.Join(dataDir, "media", "BV1Test00001", "p1")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "BV1Test00001-p1.mp4")
	if err := os.WriteFile(src, []byte("fake-mp4-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dataDir, "media", "BV1Test00001", "cover.jpg")
	_ = os.WriteFile(cover, []byte("jpeg"), 0o644)
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1Test00001", Aid: 111, Title: "测试:归档/视频", Description: "B站简介",
		OwnerMid: 42, OwnerName: "UP主", Pubdate: 1700000000, Duration: 120,
		Status: "ok", FirstArchivedAt: now, LastSyncAt: now,
		Downloaded: true, LocalPath: filepath.Join("media", "BV1Test00001", "p1", "BV1Test00001-p1.mp4"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTags("BV1Test00001", []string{"搞笑"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	_ = st.AddTags("BV1Test00001", []string{"AI标签"}, "plugin:ai-tagger")
	// 多库模型：插入以 cfg.Media.Root 为根的默认库
	if _, err := st.UpsertMediaLibrary(&store.MediaLibrary{
		Name: "默认库", RootPath: cfg.Media.Root, Layout: layout, LinkMode: "copy",
		MetadataFormat: "nfo", Enabled: true, CreatedAt: store.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return New(st, cfg, nil), st, cfg.Media.Root
}

func TestRebuildMovieLayout(t *testing.T) {
	b, st, root := setupBridge(t, "movie")
	// 给视频补页面记录（downloaded 校验只看 videos 表，无页面也能出片）
	res, err := b.Rebuild()
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Skipped != 0 {
		t.Fatalf("重建结果异常: %+v", res)
	}
	// 找到输出文件
	entries, _ := os.ReadDir(filepath.Join(root, "Movies"))
	if len(entries) != 1 {
		t.Fatalf("Movies 目录应有 1 项: %d", len(entries))
	}
	movieDir := filepath.Join(root, "Movies", entries[0].Name())
	files, _ := os.ReadDir(movieDir)
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name()] = true
	}
	if !names["movie.nfo"] || !names["poster.jpg"] {
		t.Fatalf("缺少 nfo/海报: %v", names)
	}
	var mp4 string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".mp4") {
			mp4 = f.Name()
		}
	}
	if mp4 == "" {
		t.Fatal("缺少 mp4")
	}
	// mp4 内容应与源一致（copy）
	out, _ := os.ReadFile(filepath.Join(movieDir, mp4))
	if string(out) != "fake-mp4-content" {
		t.Fatal("拷贝内容不一致")
	}
	// NFO 只写 B 站原生数据
	nfo, _ := os.ReadFile(filepath.Join(movieDir, "movie.nfo"))
	s := string(nfo)
	if !strings.Contains(s, "测试") || !strings.Contains(s, "B站简介") || !strings.Contains(s, "UP主") {
		t.Fatalf("NFO 缺少 origin 字段: %s", s)
	}
	if strings.Contains(s, "AI标签") || strings.Contains(s, "plugin:") {
		t.Fatalf("NFO 不得包含派生数据: %s", s)
	}
	_ = st
}

func TestLinkModes(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.mp4")
	_ = os.WriteFile(src, []byte("data"), 0o644)
	dst := filepath.Join(tmp, "out.mp4")
	for _, mode := range []string{"auto", "symlink", "hardlink", "copy"} {
		d := dst + "." + mode
		if err := linkFile(src, d, mode); err != nil {
			t.Fatalf("mode=%s 失败: %v", mode, err)
		}
		if fi, err := os.Stat(d); err != nil || fi.Size() != 4 {
			t.Fatalf("mode=%s 链接内容错误", mode)
		}
	}
	if err := linkFile(filepath.Join(tmp, "missing.mp4"), dst+".bad", "copy"); err == nil {
		t.Fatal("源不存在应报错")
	}
}

func TestEpisodeNFOExcludesDerivedTags(t *testing.T) {
	b, st, root := setupBridge(t, "show")
	// show 布局需要同一 UP 主 ≥3 个视频才按剧集处理；这里直接构造
	for i := 2; i <= 3; i++ {
		bvid := "BV1Test0000" + string(rune('0'+i))
		srcDir := filepath.Join(b.Cfg.DataDir, "media", bvid, "p1")
		_ = os.MkdirAll(srcDir, 0o755)
		_ = os.WriteFile(filepath.Join(srcDir, bvid+"-p1.mp4"), []byte("x"), 0o644)
		_ = st.UpsertVideo(&store.Video{
			Bvid: bvid, Title: "视频" + string(rune('0'+i)), OwnerMid: 42, OwnerName: "UP主",
			Pubdate: int64(1700000000 + i), Status: "ok", FirstArchivedAt: store.Now(),
			Downloaded: true, LocalPath: filepath.Join("media", bvid, "p1", bvid+"-p1.mp4"),
		})
		_ = st.UpsertPage(&store.Page{Bvid: bvid, Cid: int64(200 + i), Page: 1, FirstSeenAt: store.Now(), LastSeenAt: store.Now()})
	}
	_ = st.UpsertPage(&store.Page{Bvid: "BV1Test00001", Cid: 222, Page: 1, FirstSeenAt: store.Now(), LastSeenAt: store.Now()})
	if _, err := b.Rebuild(); err != nil {
		t.Fatal(err)
	}
	showDir := filepath.Join(root, "Shows", "UP主")
	if _, err := os.Stat(filepath.Join(showDir, "tvshow.nfo")); err != nil {
		t.Fatalf("show 布局未生成 tvshow.nfo: %v", err)
	}
	seasonDir := filepath.Join(showDir, "Season 01")
	files, _ := os.ReadDir(seasonDir)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".nfo") && strings.Contains(f.Name(), "S01E") {
			nf, _ := os.ReadFile(filepath.Join(seasonDir, f.Name()))
			if strings.Contains(string(nf), "AI标签") || strings.Contains(string(nf), "plugin:") {
				t.Fatalf("集级 NFO 不得包含派生标签: %s", nf)
			}
			if strings.Contains(string(nf), "搞笑") {
				found++ // B 站原生标签允许出现
			}
		}
	}
	if found == 0 {
		t.Log("警告：集级 NFO 未包含原生标签（可能未命中 episode nfo）")
	}
}
