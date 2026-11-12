package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestStatsSweepFixedWindowAndStableCursor(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 805; i++ {
		if err := s.UpsertVideo(&Video{Bvid: fmt.Sprintf("BV%04d", i), Pubdate: int64(i), LastStatsAt: 77, FirstArchivedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	total, before, err := s.StatsSweepWindow(t.Context())
	if err != nil || total != 805 {
		t.Fatalf("window %d %v", total, err)
	}
	if err := s.UpsertVideo(&Video{Bvid: "BVlater", FirstArchivedAt: 1}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for before > 0 {
		page, err := s.StatsSweepPage(t.Context(), before, 400)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 400 {
			t.Fatal("unbounded page")
		}
		for _, v := range page {
			if v.Bvid == "BVlater" || seen[v.Bvid] || v.LastStatsAt != 77 {
				t.Fatal("wrong sweep", v)
			}
			seen[v.Bvid] = true
		}
		// 修改排序/时间字段不会改变 rowid，也不会漏项或重复。
		if _, err := s.DB.Exec(`UPDATE videos SET pubdate=pubdate+90000 WHERE bvid='BV0000'`); err != nil {
			t.Fatal(err)
		}
		before = page[len(page)-1].RowID - 1
	}
	if len(seen) != 805 {
		t.Fatal(len(seen))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.StatsSweepPage(ctx, 99999, 400); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestDueSourcesMatchesLegacyFiltering(t *testing.T) {
	s := openTest(t)
	now := int64(100000)
	for i := 0; i < 40; i++ {
		v := &Source{Type: "video", ExtID: fmt.Sprint(i), CreatedAt: 1, Watch: i%2 == 0, SyncIntervalMinutes: []int{0, 10, -1, 60}[i%4], SyncMode: []string{"once", "interval"}[(i/2)%2], LastSyncAt: []int64{0, now, now - 600, now - 3600, -1}[(i/8)%5]}
		if _, err := s.UpsertSource(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, mins := range []int{0, 20, 60} {
		all, err := s.ListSources()
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		for _, src := range all {
			if !src.Watch || (src.SyncMode == "once" && src.LastSyncAt > 0) {
				continue
			}
			interval := src.SyncIntervalMinutes
			if interval <= 0 {
				interval = mins
			}
			if src.LastSyncAt == 0 || src.LastSyncAt <= now-int64(interval)*60 {
				want = append(want, src.ID)
			}
		}
		due, err := s.DueSources(mins, now)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, src := range due {
			got = append(got, src.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("minutes=%d got %v want %v", mins, got, want)
		}
	}
}

func TestTagsBatchOriginOrderAndChunkBoundary(t *testing.T) {
	s := openTest(t)
	ids := make([]string, 805)
	for i := range ids {
		ids[i] = fmt.Sprintf("BV%04d", i)
		if err := s.AddTags(ids[i], []string{"后加入ID", "先加入视频"}, "bilibili"); err != nil {
			t.Fatal(err)
		}
		if err := s.AddTags(ids[i], []string{"不应路由"}, "plugin:test"); err != nil {
			t.Fatal(err)
		}
	}
	ids = append(ids, ids[0], "missing")
	got, err := s.BilibiliTagsByBvids(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 805 {
		t.Fatal(len(got))
	}
	for _, id := range ids[:805] {
		original, err := s.VideoTags(id)
		if err != nil || !reflect.DeepEqual(got[id], original["bilibili"]) {
			t.Fatalf("tags %s: %v %v", id, got[id], err)
		}
	}
}
