package downloader

// 下载器测试：选流策略 + 断点/整流下载（不依赖 ffmpeg）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
)

func mkStreams() []biliapi.DashStream {
	return []biliapi.DashStream{
		{ID: 116, Codecid: 12, Codecs: "hev", BaseURL: "http://hevc"}, // HEVC 默认不允许
		{ID: 80, Codecid: 7, Codecs: "avc", BaseURL: "http://avc1080"},
		{ID: 64, Codecid: 7, Codecs: "avc", BaseURL: "http://avc720"},
		{ID: 120, Codecid: 13, Codecs: "av1", BaseURL: "http://av1"},
	}
}

func TestSelectVideoQualityCap(t *testing.T) {
	cfg := config.Defaults()
	cfg.Download.Quality = "1080p" // 80
	d := &Downloader{Cfg: cfg}
	s := d.selectVideo(mkStreams())
	if s == nil || s.ID != 80 || s.Codecid != 7 {
		t.Fatalf("应选 1080p AVC: %+v", s)
	}
	// 允许 AV1 后，<=want 仍是 80 avc（av1 是 4K 超档）
	cfg.Download.AllowAV1 = true
	s = d.selectVideo(mkStreams())
	if s.ID != 80 || s.Codecid != 7 {
		t.Fatalf("允许 AV1 也不应超档: %+v", s)
	}
	// 4K 档：120 av1/hevc 之上，需要允许新编码
	cfg.Download.Quality = "4k"
	cfg.Download.AllowAV1 = true
	s = d.selectVideo(mkStreams())
	if s.ID != 120 {
		t.Fatalf("4K 应选 120: %+v", s)
	}
	// 不允许 AV1 时 4K 只能回落 80
	cfg.Download.AllowAV1 = false
	s = d.selectVideo(mkStreams())
	if s.ID != 80 {
		t.Fatalf("不允许 AV1 时应回落 80: %+v", s)
	}
}

func TestSelectVideoQualitySet(t *testing.T) {
	// 全部画质（默认）：取允许编码里的最高档
	cfg := config.Defaults()
	cfg.Download.AllowAV1 = true
	d := &Downloader{Cfg: cfg}
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 120 {
		t.Fatalf("默认应选最高档 4K: %+v", s)
	}
	cfg.Download.AllowAV1 = false
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 80 {
		t.Fatalf("不允许新编码时应选 80: %+v", s)
	}

	// 指定若干档：取集合内可用的最高档
	cfg.Download.Qualities = []string{"720p", "1080p"}
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 80 {
		t.Fatalf("白名单 {1080p,720p} 应选 80: %+v", s)
	}
	// 未知档位名忽略
	cfg.Download.Qualities = []string{"1080p", "bogus"}
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 80 {
		t.Fatalf("未知档位应被忽略: %+v", s)
	}

	// 白名单全部不可用：回落到上限以下最高档
	cfg.Download.Qualities = []string{"1080p"}
	lowOnly := []biliapi.DashStream{
		{ID: 64, Codecid: 7, BaseURL: "http://720"},
		{ID: 32, Codecid: 7, BaseURL: "http://480"},
	}
	if s := d.selectVideo(lowOnly); s == nil || s.ID != 64 {
		t.Fatalf("1080p 不可用应回落 64: %+v", s)
	}

	// allow_4k=false 剔除 4K
	cfg.Download.Qualities = []string{"4k", "1080p"}
	cfg.Download.Allow4K = false
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 80 {
		t.Fatalf("allow_4k=false 应剔除 120 选 80: %+v", s)
	}
	cfg.Download.Allow4K = true
	cfg.Download.AllowAV1 = true // mkStreams 的 4K 流是 AV1 编码
	if s := d.selectVideo(mkStreams()); s == nil || s.ID != 120 {
		t.Fatalf("allow_4k=true 且含 4k 应选 120: %+v", s)
	}
}

func TestResolveQuality(t *testing.T) {
	// 默认：全部画质 → 请求最高位；Allow4K 默认关 → fnval 不带 4K 位
	cfg := config.Defaults()
	d := &Downloader{Cfg: cfg}
	qn, fnval := d.resolveQuality()
	if qn != 120 || fnval != 16 {
		t.Fatalf("默认应 qn=120 fnval=16，实际 %d %d", qn, fnval)
	}
	// 开 4K
	cfg.Download.Allow4K = true
	if qn, fnval = d.resolveQuality(); qn != 120 || fnval != 144 {
		t.Fatalf("全部画质+4K 应 qn=120 fnval=144，实际 %d %d", qn, fnval)
	}
	// 指定档位取最高为 qn；Hi-Res 音频带 256 位
	cfg.Download.Qualities = []string{"1080p", "720p"}
	cfg.Download.AudioQualities = []string{"hires", "192k"}
	if qn, fnval = d.resolveQuality(); qn != 80 || fnval != 16|256 {
		t.Fatalf("白名单+Hi-Res 应 qn=80 fnval=272，实际 %d %d", qn, fnval)
	}
	// 旧版单档位：上限语义，不带 4K 位（即使 allow_4k=true）
	cfg2 := config.Defaults()
	cfg2.Download.Quality = "1080p"
	cfg2.Download.Allow4K = true
	if qn, fnval = (&Downloader{Cfg: cfg2}).resolveQuality(); qn != 80 || fnval != 16 {
		t.Fatalf("旧单档 1080p 应 qn=80 fnval=16，实际 %d %d", qn, fnval)
	}
}

func TestSelectAudio(t *testing.T) {
	d := &Downloader{Cfg: config.Defaults()}
	streams := []biliapi.DashStream{{ID: 30216}, {ID: 30280}, {ID: 30232}}
	if s := d.selectAudio(streams); s.ID != 30280 {
		t.Fatalf("默认应选 192k: %+v", s)
	}
	// 指定 64k：集合内存在则精确命中
	d.Cfg.Download.AudioQualities = []string{"64k"}
	if s := d.selectAudio(streams); s.ID != 30216 {
		t.Fatalf("64k 存在应精确命中: %+v", s)
	}
	// 64k 缺失：标准链兜底到 192k
	no64 := []biliapi.DashStream{{ID: 30280}, {ID: 30232}}
	if s := d.selectAudio(no64); s.ID != 30280 {
		t.Fatalf("64k 缺失应兜底 192k: %+v", s)
	}
	// 杜比/Hi-Res 在合并后的候选里（dolby/flac 字段展开传入）
	d.Cfg.Download.AudioQualities = []string{"hires", "dolby"}
	spatial := []biliapi.DashStream{{ID: 30280}, {ID: 30251}, {ID: 30250}}
	if s := d.selectAudio(spatial); s.ID != 30251 {
		t.Fatalf("hires 优先于 dolby，应选 30251: %+v", s)
	}
}

func TestDownloadFileFullAndResume(t *testing.T) {
	payload := make([]byte, 300*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var hitRange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); rg != "" {
			hitRange = true
			var start int64
			fmt.Sscanf(rg, "bytes=%d-", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[start:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	api, err := biliapi.New("", 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	d := &Downloader{Cfg: cfg, API: api}
	stream := &biliapi.DashStream{ID: 80, BaseURL: srv.URL, Bandwidth: 1}

	// 全量下载
	path := filepath.Join(t.TempDir(), "video.m4s.part")
	if err := d.downloadFile(context.Background(), stream, path, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || len(got) != len(payload) {
		t.Fatalf("全量下载不完整: %d %v", len(got), err)
	}
	// 模拟断点：保留前半部分，续传
	part := path + ".resume"
	_ = os.WriteFile(part, payload[:100*1024], 0o644)
	if err := d.downloadFile(context.Background(), stream, part, nil); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(part)
	if len(got2) != len(payload) {
		t.Fatalf("断点续传不完整: %d", len(got2))
	}
	if string(got2) != string(payload) {
		t.Fatal("续传内容不一致")
	}
	if !hitRange {
		t.Fatal("未发起 Range 请求")
	}
}
