package levels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
)

func TestIncrementalOwnerOnlyQueriesRequestedMidAndCachesZero(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for mid := int64(1); mid <= 3; mid++ {
		if err := st.UpsertVideo(&store.Video{Bvid: "BV" + strconv.FormatInt(mid, 10), OwnerMid: mid, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/x/web-interface/nav":
			data = map[string]any{"wbi_img": map[string]string{"img_url": "https://example.com/7cd084941338484aae1ad9425b94feee.png", "sub_url": "https://example.com/4932ca060261963d2e9c20273d95603c.png"}}
		case "/x/frontend/finger/spi":
			data = map[string]string{"b_3": "test", "b_4": "test"}
		case "/x/space/wbi/acc/info":
			mid := r.URL.Query().Get("mid")
			mu.Lock()
			calls[mid]++
			mu.Unlock()
			data = map[string]any{}
			if mid == "2" {
				data = map[string]any{"level_info": map[string]int{"current_level": 5}}
			}
			if mid == "3" {
				data = map[string]any{"level_info": map[string]int{"current_level": 0}}
			}
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	t.Cleanup(srv.Close)
	cookieFile := filepath.Join(dir, "cookies.json")
	if err := os.WriteFile(cookieFile, []byte(`[{"name":"SESSDATA","value":"test","domain":".bilibili.com","path":"/"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	api, err := biliapi.New(cookieFile, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	api.SetBase(srv.URL, srv.URL)
	b := New(st, dir, api, nil)
	for _, vid := range []string{"BV2", "BV1", "BV1"} {
		if err := b.EnsureOwnerLevel(context.Background(), vid); err != nil {
			t.Fatal(err)
		}
	}
	v, err := st.GetVideo("BV2")
	if err != nil || v.OwnerLevel != 5 {
		t.Fatalf("owner level: %v %v", v, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.EnsureOwnerLevel(context.Background(), "BV3"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := b.EnsureOwnerLevel(context.Background(), "BV3"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, mid := range []string{"1", "2", "3"} {
		if calls[mid] != 1 {
			t.Errorf("mid %s requested %d times", mid, calls[mid])
		}
	}
}
