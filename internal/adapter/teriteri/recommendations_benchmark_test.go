package teriteri

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkVideoRecommendations(b *testing.B) {
	for _, tc := range []struct {
		name, endpoint string
		videos         int
		excluded       int
	}{
		{"Small", "/api/video/random/visitor", 64, 0},
		{"Archive", "/api/video/random/visitor", 10000, 0},
		{"Cumulative", "/api/video/cumulative/visitor", 10000, 1500},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			st, err := store.Open(filepath.Join(dir, "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { st.Close() })
			_, err = st.DB.Exec(`WITH RECURSIVE ids(n) AS (
				SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n < ?
			) INSERT INTO videos(bvid,title,description,pubdate,first_archived_at)
			SELECT printf('BV%010d',n), 'Synthetic video', ?, n, 1 FROM ids`, tc.videos, strings.Repeat("Archive description. ", 32))
			if err != nil {
				b.Fatal(err)
			}
			for _, query := range []string{
				`INSERT INTO video_policies(bvid,hidden,updated_at) SELECT bvid,1,1 FROM videos WHERE rowid%5=0`,
				`INSERT INTO video_stats_history(bvid,ts,view,danmaku) SELECT bvid,1,rowid,2 FROM videos`,
			} {
				if _, err := st.DB.Exec(query); err != nil {
					b.Fatal(err)
				}
			}
			endpoint := tc.endpoint
			if tc.excluded > 0 {
				exclude := make([]string, tc.excluded)
				for i := range exclude {
					exclude[i] = fmt.Sprintf("BV%010d", i+1)
				}
				endpoint += "?vids=" + url.QueryEscape(strings.Join(exclude, ","))
			}
			mux := http.NewServeMux()
			(&Adapter{St: st, DataDir: dir}).Register(mux)
			b.ReportAllocs()
			for b.Loop() {
				r := httptest.NewRequestWithContext(b.Context(), http.MethodGet, endpoint, nil)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"vid":`) {
					b.Fatalf("unexpected recommendation response: %d %s", w.Code, w.Body)
				}
			}
		})
	}
}
