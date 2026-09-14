package teriteri

// 评论区 SQL 分页回归：主楼页码推进（offset>0）与 B站/站内合并序列跨段边界。
// 背景：2026-09-11 分页重写曾以段内全局下标索引页内切片，第 2 页起越界 panic——
// 分页类重写必须带 offset>0 与跨边界用例（PITFALLS 同款教训）。

import (
	"encoding/json"
	"fmt"
	"testing"

	"bili-saver/internal/store"
)

// seedMainComments 造 n 条主楼（rpid=base+i，点赞=i，i∈[1,n]），hot 序=点赞降序。
func seedMainComments(t *testing.T, st *store.Store, bvid string, base, n int64) {
	t.Helper()
	now := store.Now()
	for i := int64(1); i <= n; i++ {
		if err := st.UpsertComment(&store.Comment{
			Rpid: base + i, Bvid: bvid, Content: fmt.Sprintf("m%d", i),
			LikeCount: int(i), Ctime: 1700000000 + i,
			FirstSeenAt: now, LastSeenAt: now, Status: "visible",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeCommentPage(t *testing.T, raw json.RawMessage) (more bool, likes []int, contents []string) {
	t.Helper()
	var data struct {
		More     bool `json:"more"`
		Comments []struct {
			Content string `json:"content"`
			Love    int    `json:"love"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	more = data.More
	for _, c := range data.Comments {
		likes = append(likes, c.Love)
		contents = append(contents, c.Content)
	}
	return
}

func TestCommentGetSecondPage(t *testing.T) {
	st, srv := newAdapterServer(t)
	seedMainComments(t, st, "BV1Page000001", 100, 25)

	// 第 1 页：20 条，还有更多
	resp, e := get(t, srv.URL+"/api/comment/get?vid=BV1Page000001&offset=0&type=1", nil)
	if e.Code != 200 {
		t.Fatalf("code=%d msg=%s", e.Code, e.Message)
	}
	_ = resp
	more, likes, _ := decodeCommentPage(t, e.Data)
	if !more || len(likes) != 20 || likes[0] != 25 || likes[19] != 6 {
		t.Fatalf("第 1 页应为赞 25..6 且 more=true: more=%v likes=%v", more, likes)
	}

	// 第 2 页（修复前此处越界 panic）
	_, e = get(t, srv.URL+"/api/comment/get?vid=BV1Page000001&offset=20&type=1", nil)
	if e.Code != 200 {
		t.Fatalf("第 2 页不应失败: code=%d msg=%s", e.Code, e.Message)
	}
	more, likes, _ = decodeCommentPage(t, e.Data)
	if more || len(likes) != 5 || likes[0] != 5 || likes[4] != 1 {
		t.Fatalf("第 2 页应为赞 5..1 且 more=false: more=%v likes=%v", more, likes)
	}
}

func TestCommentGetCrossBoundary(t *testing.T) {
	st, srv := newAdapterServer(t)
	// 22 条 B 站主楼 + 2 条站内主楼：type=1 合并序列 = [B站22] ++ [站内2]
	seedMainComments(t, st, "BV1Page000002", 200, 22)
	if _, err := st.AddSiteComment(1, "BV1Page000002", 0, 0, 0, "站内一"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSiteComment(1, "BV1Page000002", 0, 0, 0, "站内二"); err != nil {
		t.Fatal(err)
	}

	// offset=15：页窗口 [15,35) 跨 B站/站内段边界 → 7 条 B站（赞 7..1）+ 2 条站内
	_, e := get(t, srv.URL+"/api/comment/get?vid=BV1Page000002&offset=15&type=1", nil)
	if e.Code != 200 {
		t.Fatalf("code=%d msg=%s", e.Code, e.Message)
	}
	more, likes, contents := decodeCommentPage(t, e.Data)
	if more || len(likes) != 9 {
		t.Fatalf("跨段页应 9 条且 more=false: more=%v n=%d", more, len(likes))
	}
	for i, want := range []int{7, 6, 5, 4, 3, 2, 1} {
		if likes[i] != want {
			t.Fatalf("B站段第 %d 项赞应 %d: %v", i, want, likes)
		}
	}
	if contents[7] != "站内一" || contents[8] != "站内二" {
		t.Fatalf("站内段应在 B 站段之后: %v", contents)
	}

	// type=2：站内段在前。offset=0 → 2 站内 + 18 条 B站（赞 22..5），more=true
	_, e = get(t, srv.URL+"/api/comment/get?vid=BV1Page000002&offset=0&type=2", nil)
	if e.Code != 200 {
		t.Fatalf("code=%d msg=%s", e.Code, e.Message)
	}
	more, likes, contents = decodeCommentPage(t, e.Data)
	if !more || len(likes) != 20 || contents[0] != "站内一" || contents[1] != "站内二" {
		t.Fatalf("type=2 首页应站内在前 20 条 more=true: more=%v contents=%v", more, contents)
	}
	if likes[2] != 22 || likes[19] != 5 {
		t.Fatalf("type=2 B站段应赞 22..5: %v", likes)
	}
}
