package store

import (
	"fmt"
	"testing"
)

func TestCommentAdviceEvidenceAndActivity(t *testing.T) {
	now := int64(1800000000)
	v := &Video{Bvid: "BVAdvice", Status: "ok", Pubdate: now - 100*86400}
	for _, tc := range []struct {
		name               string
		reply, days, delta int
		want               int
	}{
		{"medium", 10000, 3, 0, 7}, {"large", 50000, 7, 1, 14}, {"huge", 100000, 14, 0, 30},
		{"small", 9999, 14, 0, 0}, {"short observation", 100000, 2, 0, 0}, {"active", 100000, 14, 60, 0},
		{"count dropped", 100000, 14, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := []*StatsSnapshot{{Ts: now - int64(tc.days)*86400, Reply: tc.reply, View: 1000}, {Ts: now, Reply: tc.reply + tc.delta, View: 1000}}
			got := SuggestCommentSync(v, h, now)
			days := 0
			if got != nil {
				days = got.Days
				if got.Minutes != days*1440 {
					t.Fatal(got)
				}
			}
			if days != tc.want {
				t.Fatalf("days=%d want %d", days, tc.want)
			}
		})
	}
	h := []*StatsSnapshot{{Ts: now - 14*86400, Reply: 100000, View: 1000}, {Ts: now - 3600, Reply: 100000, View: 1000}, {Ts: now, Reply: 100001, View: 1000}}
	if SuggestCommentSync(v, h, now) != nil {
		t.Fatal("recent reply growth must override quiet long history")
	}
	stale := []*StatsSnapshot{{Ts: now - 18*86400, Reply: 100000}, {Ts: now - 4*86400, Reply: 100000}}
	if SuggestCommentSync(v, stale, now) != nil {
		t.Fatal("stale data must not advise")
	}
}

func TestCommentAdviceStatsBatchAnchorsAndBoundary(t *testing.T) {
	s := openTest(t)
	now := int64(1800000000)
	ids := make([]string, 405)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		ids[i] = fmt.Sprintf("BVAdvice%d", i)
		for day := 0; day <= 40; day++ {
			if _, err := tx.Exec(`INSERT INTO video_stats_history(bvid,ts,view,reply) VALUES(?,?,1000,100000)`, ids[i], now-int64(day)*86400); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := s.CommentAdviceStats(append(ids, ids[0], "missing"), now)
	if err != nil || len(got) != 405 {
		t.Fatalf("%d %v", len(got), err)
	}
	for _, id := range ids {
		if len(got[id]) != 6 {
			t.Fatalf("%s samples=%d", id, len(got[id]))
		}
		a := SuggestCommentSync(&Video{Bvid: id, Status: "ok", Pubdate: now - 100*86400}, got[id], now)
		if a == nil || a.Days != 30 || a.ObservedDays != 30 {
			t.Fatalf("%s %+v", id, a)
		}
	}
}
