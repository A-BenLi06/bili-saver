package levels

// 等级回填测试：raw 快照离线回填 + 等级→经验映射（teriteri 前端 VLevel 契约）。

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func writeSnapshot(t *testing.T, dataDir, endpoint, name string, replies []snapReply) {
	t.Helper()
	dir := filepath.Join(dataDir, "raw", endpoint, "deadbeef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := struct {
		Data struct {
			Replies []snapReply `json:"replies"`
		} `json:"data"`
	}{}
	body.Data.Replies = replies
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := gzip.NewWriter(f).Close(); err == nil {
		w := gzip.NewWriter(f)
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommentLevelsFromSnapshots(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := store.Now()
	if err := st.UpsertVideo(&store.Video{
		Bvid: "BV1LvTest001", Aid: 1, Title: "等级测试", OwnerMid: 42,
		Pubdate: now, PagesCount: 1, Status: "ok", FirstArchivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*store.Comment{
		{Rpid: 111, Bvid: "BV1LvTest001", Content: "a", MemberMid: 1, FirstSeenAt: now, LastSeenAt: now},
		{Rpid: 112, Bvid: "BV1LvTest001", Content: "b", MemberMid: 2, FirstSeenAt: now, LastSeenAt: now},
		{Rpid: 113, Bvid: "BV1LvTest001", Content: "c", MemberMid: 3, FirstSeenAt: now, LastSeenAt: now},
	} {
		if err := st.UpsertComment(c); err != nil {
			t.Fatal(err)
		}
	}

	// 快照：111→Lv5、嵌套楼中楼 112→Lv2；113 不在任何快照里（保持 0）
	writeSnapshot(t, dataDir, "x_v2_reply_reply", "111-aaa", []snapReply{
		{Rpid: 111, Member: mkMember(5), Replies: []snapReply{
			{Rpid: 112, Member: mkMember(2)},
		}},
	})
	// 损坏文件：跳过不中断
	bad := filepath.Join(dataDir, "raw", "x_v2_reply_wbi_main", "beef", "222-bad.json.gz")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(bad, []byte("not gzip"), 0o644)

	b := New(st, dataDir, nil, nil)
	n, err := b.CommentLevelsFromSnapshots(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应回填 2 行，实际 %d", n)
	}
	cs, err := st.ListComments("BV1LvTest001", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int{}
	for _, c := range cs {
		got[c.Rpid] = c.MemberLevel
	}
	if got[111] != 5 || got[112] != 2 || got[113] != 0 {
		t.Fatalf("等级回填结果错误: %v", got)
	}

	// 幂等：再跑一遍不改结果
	n, err = b.CommentLevelsFromSnapshots(context.Background(), nil)
	if err != nil || n != 0 {
		t.Fatalf("二次回填应为 0: %d %v", n, err)
	}
}

func mkMember(level int) (m struct {
	LevelInfo struct {
		CurrentLevel int `json:"current_level"`
	} `json:"level_info"`
}) {
	m.LevelInfo.CurrentLevel = level
	return m
}

func TestLevelExp(t *testing.T) {
	// teriteri-client handleLevel 的边界：50/200/1500/4500/10800/28800
	cases := map[int]int{
		-1: 0, 0: 0, 1: 50, 2: 200, 3: 1500, 4: 4500, 5: 10800, 6: 28800, 7: 28800,
	}
	for level, want := range cases {
		if got := LevelExp(level); got != want {
			t.Errorf("LevelExp(%d) = %d, want %d", level, got, want)
		}
	}
}

func TestOwnerLevelsSkipsWhenNotLoggedIn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := New(st, t.TempDir(), nil, nil)
	updated, skipped, err := b.OwnerLevels(context.Background())
	if err != nil || !skipped || updated != 0 {
		t.Fatalf("无 API 时应 skipped: %d %v %v", updated, skipped, err)
	}
}
