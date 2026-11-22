package teriteri

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func TestVideoRecommendations(t *testing.T) {
	st := seed(t)
	const hidden = "BV1Test00001"
	if err := st.SetVideoHidden(hidden, true); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("BVFeed%06d", i)
		ids = append(ids, id)
		if err := st.UpsertVideo(&store.Video{
			Bvid: id, Title: id, Pic: "cover.jpg", Duration: 120, Pubdate: 1700000000,
			OwnerMid: 42, OwnerName: "作者", OwnerFace: "face.jpg", OwnerLevel: 3,
			Status: "gone", FirstArchivedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendStats(&store.StatsSnapshot{Bvid: id, Ts: 1, View: 1, Danmaku: 1}); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendStats(&store.StatsSnapshot{Bvid: id, Ts: 2, View: 100, Danmaku: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetVideoHidden(ids[0], false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO video_policies(bvid,hidden,updated_at) VALUES(?,'invalid',1)`, ids[24]); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Adapter{St: st, DataDir: t.TempDir()}).Register(mux)
	getData := func(path string, into any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		var response envelope
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || response.Code != 200 {
			t.Fatalf("%s: HTTP %d, payload %s: %v", path, w.Code, w.Body, err)
		}
		if err := json.Unmarshal(response.Data, into); err != nil {
			t.Fatal(err)
		}
	}
	checkCards := func(cards []tFeedCard, want int, excluded []string) {
		t.Helper()
		if len(cards) != want {
			t.Fatalf("received %d cards, want %d", len(cards), want)
		}
		seen := map[string]bool{hidden: true, ids[24]: true}
		for _, id := range excluded {
			seen[id] = true
		}
		for _, c := range cards {
			id := c.Video.Vid
			if id == "" || seen[id] {
				t.Fatalf("hidden, excluded, or duplicate ID %q", id)
			}
			seen[id] = true
			if c.Video.Title != id || c.Video.CoverUrl != "cover.jpg" || c.Video.Duration != 120 ||
				c.User.UID != 42 || c.User.Nickname != "作者" || c.User.AvatarURL != "face.jpg" ||
				c.User.Exp != 1500 || c.Stats.Play != 100 || c.Stats.Danmu != 5 {
				t.Fatalf("unexpected card: %+v", c)
			}
		}
	}
	var random []tFeedCard
	getData("/api/video/random/visitor", &random)
	checkCards(random, 11, nil)

	path := "/api/video/cumulative/visitor?vids="
	var cumulative struct {
		Videos []tFeedCard `json:"videos"`
		IDs    []string    `json:"vids"`
		More   bool        `json:"more"`
	}
	excluded := append([]string(nil), ids[20:24]...)
	for i := range 1200 {
		excluded = append(excluded, fmt.Sprintf("absent'%d", i))
	}
	getData(path+url.QueryEscape(strings.Join(excluded, ",")), &cumulative)
	checkCards(cumulative.Videos, 20, excluded)
	if !cumulative.More || len(cumulative.IDs) != len(cumulative.Videos) {
		t.Fatalf("unexpected cumulative response: %+v", cumulative)
	}
	for i, id := range cumulative.IDs {
		if id != cumulative.Videos[i].Video.Vid {
			t.Fatalf("ID %q does not match card %d", id, i)
		}
	}

	getData(path+url.QueryEscape(strings.Join(ids, ",")), &cumulative)
	if cumulative.Videos == nil || cumulative.IDs == nil || len(cumulative.Videos) != 0 || len(cumulative.IDs) != 0 || !cumulative.More {
		t.Fatalf("unexpected empty response: %+v", cumulative)
	}
}
