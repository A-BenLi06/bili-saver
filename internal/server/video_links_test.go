package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

const linkBVID = "BV1LinksTest1"

func linkFixture(t *testing.T) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Admin.Token = "link-test-token"
	cfg.Teriteri.Enabled = false
	cfg.Teriteri.FrontendURL = "http://127.0.0.1:8081"
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v := &store.Video{Bvid: linkBVID, Title: "中文视频与空格", Status: "ok", PagesCount: 2, Downloaded: true, LocalPath: filepath.ToSlash(filepath.Join("media", linkBVID, "p1", linkBVID+"-p1.mp4"))}
	if err := st.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVideoDownloaded(v.Bvid, v.LocalPath); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := st.UpsertPage(&store.Page{Bvid: linkBVID, Page: i, Cid: int64(i), Part: fmt.Sprintf("分P %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(cfg.DataDir, v.LocalPath)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	return New(Deps{Cfg: cfg, St: st})
}

func linkRequest(s *Server, method, path string, adjust func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080/api/v1"+path, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer link-test-token")
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("X-Bili-Saver-Local", "1")
	if adjust != nil {
		adjust(r)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestVideoLinksAndLocalBoundary(t *testing.T) {
	s := linkFixture(t)
	var opened string
	s.revealPath = func(p string) error { opened = p; return nil }
	w := linkRequest(s, "GET", "/videos/"+linkBVID, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var d struct {
		Files []videoFileLink `json:"file_links"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 || !d.Files[0].Exists || !d.Files[0].CanOpen || !filepath.IsAbs(d.Files[0].Path) || d.Files[1].Exists || d.Files[1].CanOpen {
		t.Fatalf("bad file states: %+v", d.Files)
	}
	if !strings.HasSuffix(d.Files[1].Players[0].URL, linkBVID+"-p2") {
		t.Fatal("part lost", d.Files)
	}
	w = linkRequest(s, "POST", d.Files[0].OpenURL, nil)
	if w.Code != 200 || opened != d.Files[0].Path {
		t.Fatal(w.Code, opened, w.Body.String())
	}
	for _, adjust := range []func(*http.Request){func(r *http.Request) {
		r.RemoteAddr = "192.168.1.20:1234"
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
	}, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, func(r *http.Request) { r.Header.Del("X-Bili-Saver-Local") }, func(r *http.Request) { r.Host = "evil.example" }, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8080/path") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }} {
		opened = ""
		w = linkRequest(s, "POST", d.Files[0].OpenURL, adjust)
		if w.Code != 403 || opened != "" {
			t.Fatal("unsafe local open allowed", w.Code)
		}
	}
	for _, page := range []string{"2", "999"} {
		w = linkRequest(s, "POST", "/videos/"+linkBVID+"/local-open?page="+page, nil)
		if w.Code != 404 {
			t.Fatal("unknown/missing file opened", w.Code)
		}
	}
	w = linkRequest(s, "POST", d.Files[0].OpenURL, func(r *http.Request) { r.Header.Del("Authorization") })
	if w.Code != 401 {
		t.Fatal("anonymous open", w.Code)
	}
	s.revealPath = func(string) error { return errors.New("no desktop") }
	w = linkRequest(s, "POST", d.Files[0].OpenURL, nil)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "复制路径") {
		t.Fatal(w.Code, w.Body.String())
	}
	v, _ := s.Deps.St.GetVideo(linkBVID)
	v.LocalPath = filepath.Join("..", "outside.mp4")
	if p, ok := s.videoFile(v, 1); ok || p != "" {
		t.Fatal("path traversal", p)
	}
}

func TestLibraryRootLocalOpen(t *testing.T) {
	s := linkFixture(t)
	root := filepath.Join(t.TempDir(), "中文 目录 & 'quoted'")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	id, err := s.Deps.St.UpsertMediaLibrary(&store.MediaLibrary{Name: "测试库", RootPath: root, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	s.revealPath = func(p string) error { opened = p; return nil }
	w := linkRequest(s, "POST", fmt.Sprintf("/libraries/%d/local-open", id), nil)
	if w.Code != 200 || opened != root {
		t.Fatal(w.Code, opened)
	}
	w = linkRequest(s, "GET", "/libraries", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_open":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = linkRequest(s, "POST", "/libraries/99999/local-open", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestTeriteriURLAndRedactedMediaConfig(t *testing.T) {
	s := linkFixture(t)
	s.Deps.Cfg.Teriteri.FrontendURL = "https://watch.example/prefix"
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	if got := s.teriteriVideoURL(r, linkBVID+"-p2"); got != "https://watch.example/prefix/video/"+linkBVID+"-p2" {
		t.Fatal(got)
	}
	s.Deps.Cfg.Media.Servers = []config.MediaServer{{Type: "emby", URL: "https://emby.example/prefix?api_key=secret-fixture&foo=bar"}}
	b, _ := json.Marshal(configPayload(s.Deps.Cfg))
	if strings.Contains(string(b), "secret-fixture") || !strings.Contains(string(b), `"auth_configured":true`) {
		t.Fatal("credential disclosed", string(b))
	}
	if got := safeMediaURL("javascript:alert(1)"); got != "" {
		t.Fatal("unsafe scheme", got)
	}
}

func TestMissingTeriteriNeverLinksBuiltinPlayer(t *testing.T) {
	s := linkFixture(t)
	s.Deps.Cfg.Teriteri.FrontendURL = ""
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	if got := s.teriteriVideoURL(r, linkBVID); got != "" {
		t.Fatal("missing teriteri must not use builtin player", got)
	}
	v, _ := s.Deps.St.GetVideo(linkBVID)
	pages, _ := s.Deps.St.ListPages(linkBVID)
	for _, f := range s.videoLinks(r, v, pages) {
		if len(f.Players) != 0 {
			t.Fatal("builtin player mislabeled as teriteri", f.Players)
		}
	}
	s.Deps.Cfg.Media.Servers = []config.MediaServer{{Type: "teriteri", URL: "https://watch.example"}}
	if got := s.teriteriVideoURL(r, linkBVID); got != "https://watch.example/video/"+linkBVID {
		t.Fatal(got)
	}
}

func TestMediaPlaybackMatchingAndProtectedRange(t *testing.T) {
	s := linkFixture(t)
	var queries atomic.Int32
	emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "secret-fixture" || r.URL.Query().Get("api_key") != "" {
			t.Error("credential not isolated to header")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("bili-saver session forwarded")
		}
		switch r.URL.Path {
		case "/prefix/Items":
			queries.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Items":[{"Id":"exact123","ServerId":"srv1","Type":"Movie","Path":"/Movies/中文 [%s]/video.mp4","ProviderIds":{"bilibili":"%s"}}]}`, linkBVID, linkBVID)
		case "/prefix/Videos/exact123/stream.mp4":
			if r.URL.Query().Get("Static") != "true" {
				t.Error("missing static mode")
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 1-3/5")
			w.Header().Set("Set-Cookie", "upstream=private")
			w.WriteHeader(206)
			_, _ = w.Write([]byte("ide"))
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer emby.Close()
	s.Deps.Cfg.Media.Servers = []config.MediaServer{{Name: "家庭Emby", Type: "emby", URL: emby.URL + "/prefix?api_key=secret-fixture"}}
	for i := 0; i < 2; i++ {
		w := linkRequest(s, "GET", "/videos/"+linkBVID+"/play/0", func(r *http.Request) {
			r.Header.Set("Range", "bytes=1-3")
			r.Header.Set("Cookie", "bili-saver-private=1")
		})
		if w.Code != 206 || w.Body.String() != "ide" || w.Header().Get("Content-Range") != "bytes 1-3/5" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	if queries.Load() != 1 {
		t.Fatal("range requests repeated lookup", queries.Load())
	}
	w := linkRequest(s, "GET", "/videos/"+linkBVID+"/play/0?entry=1", nil)
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "id=exact123") || strings.Contains(w.Header().Get("Location"), "secret-fixture") {
		t.Fatal(w.Code, w.Header())
	}
	w = linkRequest(s, "GET", "/videos/"+linkBVID+"/play/0?page=2", nil)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "这个分P") {
		t.Fatal("incorrect P2 match", w.Code, w.Body.String())
	}
}

func TestMediaItemExactIdentityAndParts(t *testing.T) {
	base := mediaItem{ID: "one", Type: "Movie", Path: "/Title P2 [" + linkBVID + "]/video.mp4"}
	if !matchesMediaItem(base, linkBVID, 1) || matchesMediaItem(base, linkBVID, 2) {
		t.Fatal("title mistaken for part")
	}
	base.Path = "/Movies/[" + linkBVID + "]/extras/P02 - extra.mp4"
	if !matchesMediaItem(base, linkBVID, 2) || matchesMediaItem(base, linkBVID, 1) {
		t.Fatal("extras mismatch")
	}
	base.Path = "/media/" + linkBVID + "/p2/" + linkBVID + "-p2.mp4"
	if !matchesMediaItem(base, linkBVID, 2) {
		t.Fatal("canonical P2 mismatch")
	}
	base.Path = "/other/" + strings.ToLower(linkBVID) + ".mp4"
	if matchesMediaItem(base, linkBVID, 1) {
		t.Fatal("case-sensitive BV mismatch")
	}
	base.Path = "/other/" + linkBVID + "ABC.mp4"
	if matchesMediaItem(base, linkBVID, 1) {
		t.Fatal("BV prefix mismatch")
	}
}

func TestTeriteriLANHostAndDeduplication(t *testing.T) {
	s := linkFixture(t)
	s.Deps.Cfg.Teriteri.FrontendURL = "http://127.0.0.1:8081"
	s.Deps.Cfg.Media.Servers = []config.MediaServer{{Name: "teriteri 前端", Type: "teriteri", URL: "http://127.0.0.1:8081"}}
	r := httptest.NewRequest("GET", "http://192.168.0.23:8080/", nil)
	v, _ := s.Deps.St.GetVideo(linkBVID)
	pages, _ := s.Deps.St.ListPages(linkBVID)
	links := s.videoLinks(r, v, pages)
	if len(links[0].Players) != 1 || links[1].Players[0].URL != "http://192.168.0.23:8081/video/"+linkBVID+"-p2" {
		t.Fatalf("LAN host/port or duplicate players: %+v", links)
	}
	if links[0].CanOpen {
		t.Fatal("LAN request offered server desktop")
	}
}

func TestMediaLookupRejectsAuthRedirectAndAmbiguity(t *testing.T) {
	for _, mode := range []string{"auth", "redirect", "ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s := linkFixture(t)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "auth":
					w.WriteHeader(401)
				case "redirect":
					w.Header().Set("Location", "https://unrelated.invalid/?api_key=must-not-leak")
					w.WriteHeader(302)
				case "ambiguous":
					fmt.Fprintf(w, `{"Items":[{"Id":"a","Type":"Movie","ProviderIds":{"bilibili":"%s"}},{"Id":"b","Type":"Movie","ProviderIds":{"bilibili":"%s"}}]}`, linkBVID, linkBVID)
				default:
					_, _ = w.Write([]byte(`{"Items":[]}`))
				}
			}))
			defer remote.Close()
			s.Deps.Cfg.Media.Servers = []config.MediaServer{{Type: "emby", URL: remote.URL}}
			w := linkRequest(s, "GET", "/videos/"+linkBVID+"/play/0", nil)
			if w.Code != 502 || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "must-not-leak") {
				t.Fatal("unsafe failure", w.Code, w.Body.String())
			}
		})
	}
}
