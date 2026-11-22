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

func writeSnapshot(t *testing.T, dataDir, endpoint, name string, replies []snapReply, topReplies ...snapReply) {
	t.Helper()
	dir := filepath.Join(dataDir, "raw", endpoint, "deadbeef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body snapBody
	body.Data.Replies = replies
	body.Data.TopReplies = topReplies
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
		{Rpid: 114, Bvid: "BV1LvTest001", Content: "d", MemberMid: 4, FirstSeenAt: now, LastSeenAt: now},
		{Rpid: 115, Bvid: "BV1LvTest001", Content: "e", MemberMid: 5, MemberLevel: 6, FirstSeenAt: now, LastSeenAt: now},
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
	if err := os.WriteFile(bad, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 仅索引可发现的快照在损坏文件之后，旧 Windows 路径与可移植路径指向同一文件。
	writeSnapshot(t, dataDir, "indexed", "333", nil,
		snapReply{Rpid: 111, Member: mkMember(4)},
		snapReply{Rpid: 114, Member: mkMember(3)},
		snapReply{Rpid: 115, Member: mkMember(2)},
	)
	for _, key := range []string{`C:\previous\data\raw\indexed\deadbeef\333.json.gz`, "raw/indexed/deadbeef/333.json.gz"} {
		if err := st.AddSnapshot(&store.Snapshot{Endpoint: "/x/v2/reply/wbi/main", BodyPath: key}); err != nil {
			t.Fatal(err)
		}
	}

	b := New(st, dataDir, nil, nil)
	calls := 0
	n, err := b.CommentLevelsFromSnapshots(context.Background(), func(done, total int) {
		calls++
		if done != calls || total != 3 {
			t.Fatalf("快照进度 %d/%d，预期 %d/3", done, total, calls)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || calls != 3 {
		t.Fatalf("应回填 3 行并报告 3 次进度，实际 %d 行、%d 次", n, calls)
	}
	cs, err := st.ListComments("BV1LvTest001", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int{}
	for _, c := range cs {
		got[c.Rpid] = c.MemberLevel
	}
	if got[111] != 4 || got[112] != 2 || got[113] != 0 || got[114] != 3 || got[115] != 6 {
		t.Fatalf("等级回填结果错误: %v", got)
	}

	// 幂等：再跑一遍不改结果
	n, err = b.CommentLevelsFromSnapshots(context.Background(), nil)
	if err != nil || n != 0 {
		t.Fatalf("二次回填应为 0: %d %v", n, err)
	}
}

func TestCommentLevelsFromSnapshotsSkipsCompletedArchive(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB.Exec(`INSERT INTO comments(rpid,bvid,member_level,first_seen_at,last_seen_at)
		VALUES(1, 'BVLEVELS', 6, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, dir, "x_v2_reply_reply", "cached", []snapReply{
		{Rpid: 1, Member: mkMember(4)},
		{Rpid: 2, Member: mkMember(5)},
	})
	backfill := New(st, dir, nil, nil)
	n, err := backfill.CommentLevelsFromSnapshots(t.Context(), func(done, total int) {
		t.Fatalf("completed archive scanned snapshot %d/%d", done, total)
	})
	if err != nil || n != 0 {
		t.Fatalf("completed archive returned %d, %v", n, err)
	}

	if _, err := st.DB.Exec(`INSERT INTO comments(rpid,bvid,first_seen_at,last_seen_at) VALUES(2, 'BVLEVELS', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if n, err := backfill.CommentLevelsFromSnapshots(t.Context(), nil); err != nil || n != 1 {
		t.Fatalf("backfill after import updated %d comments: %v", n, err)
	}
	var level int
	if err := st.DB.QueryRow(`SELECT member_level FROM comments WHERE rpid=2`).Scan(&level); err != nil || level != 5 {
		t.Fatalf("imported comment level = %d, want 5: %v", level, err)
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
