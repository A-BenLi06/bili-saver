package images

// 本地化行为测试：落盘路径、幂等跳过、失败不中断、全量补档覆盖。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

// fakeJPEG 合法的最小占位内容（本包不做图片解码，只校验长度阈值）。
func fakeJPEG() []byte {
	b := make([]byte, 128)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func seedStore(t *testing.T, cdn string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := store.Now()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1ImgTest01", Aid: 1, Title: "图片测试", Pic: cdn + "/cover-01.jpg",
		OwnerMid: 42, OwnerName: "UP主", OwnerFace: cdn + "/face-42.jpg",
		Pubdate: now, PagesCount: 1, Status: "ok", FirstArchivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*store.Comment{
		{Rpid: 1, Bvid: "BV1ImgTest01", Content: "c1", MemberMid: 1001, MemberFace: cdn + "/face-1001.jpg", FirstSeenAt: now, LastSeenAt: now},
		{Rpid: 2, Bvid: "BV1ImgTest01", Content: "c2", MemberMid: 1002, MemberFace: cdn + "/face-1002.jpg", FirstSeenAt: now, LastSeenAt: now},
		{Rpid: 3, Bvid: "BV1ImgTest01", Content: "c3", MemberMid: 1001, MemberFace: cdn + "/face-1001.jpg", FirstSeenAt: now, LastSeenAt: now}, // 与 rpid1 同人，去重
	} {
		_ = i
		if err := st.UpsertComment(c); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestSyncVideoLocalizesAndSkipsExisting(t *testing.T) {
	img := fakeJPEG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证防盗链 Referer 与 UA 已带
		if r.Header.Get("Referer") != "https://www.bilibili.com/" {
			t.Errorf("缺少 Referer 头")
		}
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	st := seedStore(t, srv.URL)
	dataDir := t.TempDir()
	l := New(st, dataDir, "test-ua", nil)
	l.Pace = 0

	// 封面 + UP主 + 2 个去重后的评论者 = 4
	n, err := l.SyncVideo(context.Background(), "BV1ImgTest01")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("首次本地化应新增 4 个文件，实际 %d", n)
	}
	for _, rel := range []string{
		CoverRel("BV1ImgTest01"),
		FaceRel(42),
		FaceRel(1001),
		FaceRel(1002),
	} {
		if _, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s 未落盘: %v", rel, err)
		}
	}
	// URL 助手
	if u := CoverURL(dataDir, "BV1ImgTest01"); u != "/media/media/BV1ImgTest01/cover.jpg" {
		t.Errorf("CoverURL = %q", u)
	}
	if u := FaceURL(dataDir, 1001); u != "/media/media/faces/1001.jpg" {
		t.Errorf("FaceURL = %q", u)
	}
	if u := FaceURL(dataDir, 9999); u != "" {
		t.Errorf("未落盘的 mid 应返回空串，实际 %q", u)
	}

	// 幂等：第二次全部跳过
	fetches := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		_, _ = w.Write(img)
	}))
	defer srv2.Close()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1ImgTest01", Aid: 1, Title: "图片测试", Pic: srv2.URL + "/cover-01.jpg",
		OwnerMid: 42, OwnerName: "UP主", OwnerFace: srv2.URL + "/face-42.jpg",
		Pubdate: store.Now(), PagesCount: 1, Status: "ok", FirstArchivedAt: store.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	n, err = l.SyncVideo(context.Background(), "BV1ImgTest01")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || fetches != 0 {
		t.Fatalf("已存在的文件应跳过网络请求，新增=%d 请求数=%d", n, fetches)
	}
}

func TestBackfillCoversAllVideosAndFaces(t *testing.T) {
	img := fakeJPEG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	st := seedStore(t, srv.URL)
	// 第二支视频（无评论）
	now := store.Now()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1ImgTest02", Aid: 2, Title: "图片测试2", Pic: srv.URL + "/cover-02.jpg",
		OwnerMid: 42, OwnerName: "UP主", OwnerFace: srv.URL + "/face-42.jpg",
		Pubdate: now, PagesCount: 1, Status: "ok", FirstArchivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	l := New(st, dataDir, "test-ua", nil)
	l.Pace = 0

	var lastDone, lastTotal int
	n, err := l.Backfill(context.Background(), func(done, total int) { lastDone, lastTotal = done, total })
	if err != nil {
		t.Fatal(err)
	}
	// 2 封面 + 1 UP主 + 2 评论者 = 5
	if n != 5 {
		t.Fatalf("Backfill 应新增 5 个文件，实际 %d", n)
	}
	if lastDone != lastTotal {
		t.Errorf("进度未到终点: %d/%d", lastDone, lastTotal)
	}
	if _, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(CoverRel("BV1ImgTest02")))); err != nil {
		t.Errorf("第二支视频封面未落盘: %v", err)
	}
}

func TestSyncVideoFetchFailureIsNonFatal(t *testing.T) {
	// 404 的 CDN：全部失败但 SyncVideo 不报错、不落盘
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	st := seedStore(t, srv.URL)
	dataDir := t.TempDir()
	l := New(st, dataDir, "test-ua", nil)
	l.Pace = 0

	n, err := l.SyncVideo(context.Background(), "BV1ImgTest01")
	if err != nil {
		t.Fatalf("单图失败不应报错: %v", err)
	}
	if n != 0 {
		t.Fatalf("不应有落盘，实际 %d", n)
	}
	if u := CoverURL(dataDir, "BV1ImgTest01"); u != "" {
		t.Errorf("失败后 CoverURL 应为空，实际 %q", u)
	}
}

func TestListCommentFacesDedup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	st := seedStore(t, srv.URL)

	faces, err := st.ListCommentFaces("")
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) != 2 {
		t.Fatalf("mid 1001 出现两次应去重，实际 %d 个头像", len(faces))
	}
	perVideo, err := st.ListCommentFaces("BV1ImgTest01")
	if err != nil || len(perVideo) != 2 {
		t.Fatalf("按视频过滤失败: %v %d", err, len(perVideo))
	}
	if none, err := st.ListCommentFaces("BV1Nope"); err != nil || len(none) != 0 {
		t.Fatalf("无评论视频应返回空: %v %d", err, len(none))
	}
}
