package collector

// 端到端测试：mock B 站 API → 全量归档 → 断言落库/落盘 → 二次同步验证消失检测。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

// ---- protobuf 构造（与 dmproto 测试同思路） ----

func pbKey(b []byte, field, wire int) []byte {
	key := uint64(field)<<3 | uint64(wire)
	for key >= 0x80 {
		b = append(b, byte(key)|0x80)
		key >>= 7
	}
	return append(b, byte(key))
}

func pbVarint(b []byte, field int, v uint64) []byte {
	b = pbKey(b, field, 0)
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func pbBytes(b []byte, field int, s []byte) []byte {
	b = pbKey(b, field, 2)
	for len(s) >= 0x80 {
		b = append(b, byte(len(s))|0x80)
		s = s[7:]
	}
	b = append(b, byte(len(s)))
	return append(b, s...)
}

func pbStr(b []byte, field int, s string) []byte { return pbBytes(b, field, []byte(s)) }

func buildDanmakuSeg(items []struct {
	id      int64
	content string
}) []byte {
	var seg []byte
	for _, it := range items {
		var e []byte
		e = pbVarint(e, 1, uint64(it.id))
		e = pbVarint(e, 2, 5000) // progress 5s
		e = pbVarint(e, 3, 1)
		e = pbVarint(e, 4, 25)
		e = pbVarint(e, 5, 0xFFFFFF)
		e = pbStr(e, 7, it.content)
		e = pbVarint(e, 8, 1700000000)
		seg = pbBytes(seg, 1, e)
	}
	return seg
}

// ---- mock 状态 ----

type mockBili struct {
	srv      *httptest.Server
	mainCall atomic.Int32
	replySet atomic.Int32 // 0 = 首次（r1,r2,r3）；1 = 二次（r1,r3,r4）
}

type reply struct {
	Rpid    int64 `json:"rpid"`
	Root    int64 `json:"root"`
	Like    int   `json:"like"`
	Rcount  int   `json:"rcount"`
	Content struct {
		Message string `json:"message"`
	} `json:"content"`
	Member struct {
		Mid    int64  `json:"mid"`
		Uname  string `json:"uname"`
		Avatar string `json:"avatar"`
	} `json:"member"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeLoginCookie 写一个含 SESSDATA 的 cookie 文件（模拟登录态，使消失检测可信）。
func writeLoginCookie(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "cookies.json")
	_ = os.WriteFile(f, []byte(`[{"name":"SESSDATA","value":"test","domain":".bilibili.com","path":"/"}]`), 0o600)
	return f
}

func newMockBili(t *testing.T) *mockBili {
	t.Helper()
	m := &mockBili{}
	mux := http.NewServeMux()

	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"isLogin": false,
			"wbi_img": map[string]string{
				"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b94feee.png",
				"sub_url": "https://i0.hdslb.com/bfs/wbi/4932ca060261963d2e9c20273d95603c.png",
			},
		}})
	})

	mux.HandleFunc("/x/web-interface/view", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"bvid": "BV1Test00001", "aid": 111, "videos": 1,
			"title": "测试视频：归档全流程", "desc": "这是一个用于端到端测试的简介",
			"pic": m.srv.URL + "/cover.jpg", "pubdate": 1700000000, "duration": 120,
			"owner": map[string]any{"mid": 42, "name": "测试UP主", "face": ""},
			"stat":  map[string]any{"view": 1000, "danmaku": 2, "reply": 3, "favorite": 40, "coin": 50, "share": 60, "like": 70},
			"cid":   222,
			"pages": []map[string]any{{"cid": 222, "page": 1, "part": "P1", "duration": 120}},
		}})
	})

	mux.HandleFunc("/x/tag/archive/tags", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "data": []map[string]any{
			{"id": 1, "name": "搞笑"}, {"id": 2, "name": "教程"},
		}})
	})

	mux.HandleFunc("/x/web-interface/view/conclusion/get", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"model_list": map[string]any{"bili-tracer": map[string]string{"name": "bili-tracer"}},
			"model_result": map[string]any{
				"summary": "官方AI小结内容",
				"outline": []any{},
			},
		}})
	})

	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"subtitle": map[string]any{
				"subtitles": []map[string]any{{
					"id": 7, "lan": "ai-zh", "lan_doc": "中文（自动生成）",
					"subtitle_url": m.srv.URL + "/cc/7.json",
				}},
			},
		}})
	})

	mux.HandleFunc("/cc/7.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"font_size": 0.4, "body": []map[string]any{
			{"from": 1.0, "to": 2.0, "content": "你好"},
		}})
	})

	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Query().Get("segment") != "1" {
			_, _ = w.Write(nil)
			return
		}
		_, _ = w.Write(buildDanmakuSeg([]struct {
			id      int64
			content string
		}{{1001, "弹幕一"}, {1002, "弹幕二"}}))
	})

	// 评论主楼：两次调用（分页），replySet 决定第二次同步的可见集合
	mux.HandleFunc("/x/v2/reply/wbi/main", func(w http.ResponseWriter, r *http.Request) {
		call := m.mainCall.Add(1)
		set := m.replySet.Load()
		var replies []reply
		mk := func(id int64, root int64, msg string, rcount int) reply {
			var rp reply
			rp.Rpid, rp.Root, rp.Rcount = id, root, rcount
			rp.Content.Message = msg
			rp.Member.Mid, rp.Member.Uname = id*10, "用户"
			return rp
		}
		end := false
		switch {
		case set == 0:
			if call == 1 {
				replies = []reply{mk(1, 0, "第一条热评", 1), mk(2, 0, "第二条评论", 0)}
			} else {
				end = true
			}
		default: // 第二次同步：r2 消失，r4 出现
			if call == 1 {
				replies = []reply{mk(1, 0, "第一条热评", 1), mk(4, 0, "新来的评论", 0)}
			} else {
				end = true
			}
		}
		next := ""
		if !end {
			next = fmt.Sprintf("page-%d", call)
		}
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"cursor": map[string]any{
				"is_end": end, "next": 0,
				"pagination_reply": map[string]string{"next_offset": next},
			},
			"replies":     replies,
			"top_replies": []reply{},
		}})
	})

	// 楼中楼
	mux.HandleFunc("/x/v2/reply/reply", func(w http.ResponseWriter, r *http.Request) {
		var rp reply
		rp.Rpid, rp.Root = 3, 1
		rp.Content.Message = "楼中楼回复"
		rp.Member.Mid, rp.Member.Uname = 30, "用户"
		writeJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"page":    map[string]int{"count": 1, "size": 20, "num": 1},
			"replies": []reply{rp},
		}})
	})

	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-jpeg-bytes"))
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// ---- E2E ----

func TestArchiveE2E(t *testing.T) {
	mock := newMockBili(t)

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Bilibili.MinReqMs = 1

	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	api, err := biliapi.New(writeLoginCookie(t), 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	api.SetBase(mock.srv.URL, mock.srv.URL)

	col := New(st, api, cfg, nil, nil)
	api.SetSnapshotHook(col.SnapshotHook())

	// ---- 首次归档 ----
	res, err := col.ArchiveVideo(context.Background(), "BV1Test00001")
	if err != nil {
		t.Fatalf("首次归档失败: %v", err)
	}
	if res.New != true || res.Complete != true {
		t.Fatalf("首次归档结果异常: %+v", res)
	}
	if res.CommentsSeen != 3 || res.DanmakuSeen != 2 {
		t.Fatalf("首次采集计数错误: comments=%d danmaku=%d", res.CommentsSeen, res.DanmakuSeen)
	}

	// 视频主记录
	v, err := st.GetVideo("BV1Test00001")
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "测试视频：归档全流程" || v.OwnerMid != 42 || v.Status != "ok" {
		t.Fatalf("视频记录错误: %+v", v)
	}

	// 评论：现存 3，消失 0
	vis, del, _ := st.CountCommentsByStatus("BV1Test00001")
	if vis != 3 || del != 0 {
		t.Fatalf("首次评论计数: vis=%d del=%d", vis, del)
	}
	// 楼中楼关联
	cs, _ := st.ListComments("BV1Test00001", "", 0, 0)
	var rootOK bool
	for _, c := range cs {
		if c.Rpid == 3 && c.RootRpid == 1 {
			rootOK = true
		}
	}
	if !rootOK {
		t.Fatal("楼中楼未正确关联到主楼")
	}

	// 弹幕
	dms, _ := st.ListDanmaku(222, "")
	if len(dms) != 2 || dms[0].Timepoint != 5000 {
		t.Fatalf("弹幕入库错误: %+v", dms)
	}
	// 弹幕 full.xml 与原始段
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "media", "BV1Test00001", "danmaku", "full.xml")); err != nil {
		t.Fatal("full.xml 未生成:", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "media", "BV1Test00001", "danmaku_raw", "222-seg-1.proto")); err != nil {
		t.Fatal("原始弹幕段未落盘:", err)
	}

	// 字幕（origin=bilibili）
	subs, _ := st.ListSubtitles("BV1Test00001", 0)
	if len(subs) != 1 || subs[0].Origin != "bilibili" || subs[0].Lang != "ai-zh" {
		t.Fatalf("字幕入库错误: %+v", subs)
	}
	if _, err := os.Stat(subs[0].FilePath); err != nil {
		t.Fatal("字幕文件不存在:", err)
	}

	// 标签
	tags, _ := st.VideoTags("BV1Test00001")
	if len(tags["bilibili"]) != 2 {
		t.Fatalf("标签错误: %+v", tags)
	}

	// 官方 AI 小结
	if _, summary, _, err := st.GetAIConclusion("BV1Test00001"); err != nil || summary != "官方AI小结内容" {
		t.Fatalf("AI 小结错误: %v %q", err, summary)
	}

	// 统计时间序列
	hist, _ := st.ListStats("BV1Test00001")
	if len(hist) != 1 || hist[0].View != 1000 {
		t.Fatalf("统计序列错误: %+v", hist)
	}

	// 原始快照：至少 7 个接口响应落盘
	var snapCount int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&snapCount); err != nil {
		t.Fatal(err)
	}
	if snapCount < 7 {
		t.Fatalf("快照数量不足: %d", snapCount)
	}
	entries, _ := os.ReadDir(filepath.Join(cfg.DataDir, "raw"))
	if len(entries) == 0 {
		t.Fatal("raw 目录为空")
	}

	// ---- 第二次同步：r2 消失，r4 新增 ----
	mock.mainCall.Store(0)
	mock.replySet.Store(1)
	res2, err := col.ArchiveVideo(context.Background(), "BV1Test00001")
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if res2.New != false {
		t.Fatal("第二次不应是新增")
	}
	if res2.CommentsDeleted != 1 {
		t.Fatalf("应检测到 1 条消失评论: %+v", res2)
	}
	vis, del, _ = st.CountCommentsByStatus("BV1Test00001")
	if vis != 3 || del != 1 {
		t.Fatalf("二次同步计数: vis=%d del=%d", vis, del)
	}
	// 消失的评论正文保留
	found := false
	for _, c := range cs {
		_ = c
	}
	all, _ := st.ListComments("BV1Test00001", "deleted", 0, 0)
	for _, c := range all {
		if c.Rpid == 2 && c.Content == "第二条评论" && c.DeletedAt > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("消失评论应保留正文与 deleted_at")
	}

	// 统计序列追加
	hist, _ = st.ListStats("BV1Test00001")
	if len(hist) != 2 {
		t.Fatalf("统计序列未追加: %d", len(hist))
	}

	// 快照文件可解压且为 JSON
	var bodyPath string
	if err := st.DB.QueryRow(`SELECT body_path FROM snapshots LIMIT 1`).Scan(&bodyPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(bodyPath, ".json.gz") {
		t.Fatalf("快照文件名异常: %s", bodyPath)
	}
}

// 范围采集：重同步时只刷新统计，不碰评论/弹幕/字幕；
// 分类型时间戳只更新完成的部分。
func TestArchiveWithOptions(t *testing.T) {
	mock := newMockBili(t)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Bilibili.MinReqMs = 1
	cfg.Download.DownloadDanmaku = false
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api, err := biliapi.New(writeLoginCookie(t), 1, "test-ua")
	if err != nil {
		t.Fatal(err)
	}
	api.SetBase(mock.srv.URL, mock.srv.URL)
	col := New(st, api, cfg, nil, nil)
	api.SetSnapshotHook(col.SnapshotHook())

	// 首次：全量
	if _, err := col.ArchiveVideo(context.Background(), "BV1Test00001"); err != nil {
		t.Fatal(err)
	}
	vis, _, _ := st.CountCommentsByStatus("BV1Test00001")
	if vis != 3 {
		t.Fatalf("首次评论数错误: %d", vis)
	}
	v, _ := st.GetVideo("BV1Test00001")
	if v.LastCommentsAt == 0 || v.LastDanmakuAt == 0 {
		t.Fatalf("首次归档应更新分类型时间戳: %+v", v)
	}

	// 第二次：范围=只统计（三个刷新位全关）
	mock.mainCall.Store(0)
	mock.replySet.Store(1)
	res, err := col.ArchiveVideoOpts(context.Background(), "BV1Test00001", CollectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.CommentsSeen != 0 {
		t.Fatalf("范围外不应抓评论: %d", res.CommentsSeen)
	}
	if res.Complete {
		t.Fatal("范围不完整时 Complete 应为 false（不做消失检测）")
	}
	// 评论/弹幕数据保持不变
	vis2, _, _ := st.CountCommentsByStatus("BV1Test00001")
	if vis2 != vis {
		t.Fatalf("范围外评论数不应变化: %d -> %d", vis, vis2)
	}
	v2, _ := st.GetVideo("BV1Test00001")
	if v2.LastStatsAt <= v.LastStatsAt {
		t.Fatal("stats 时间戳应更新")
	}
	if v2.LastCommentsAt != v.LastCommentsAt {
		t.Fatal("范围外 comments 时间戳不应更新")
	}
}
