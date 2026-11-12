package server

import (
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRenameLibraryRetainsIdentity(t *testing.T) {
	st, e := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	id, e := st.UpsertMediaLibrary(&store.MediaLibrary{Name: "original", RootPath: t.TempDir(), Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Deps: Deps{St: st, Cfg: config.Defaults()}}
	r := httptest.NewRequest("PATCH", "/api/v1/libraries/"+fmt.Sprint(id), strings.NewReader(`{"name":"renamed"}`))
	r.SetPathValue("id", fmt.Sprint(id))
	w := httptest.NewRecorder()
	s.handlePatchLibrary(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	libs, e := st.ListMediaLibraries()
	if e != nil {
		t.Fatal(e)
	}
	if len(libs) != 1 || libs[0].ID != id || libs[0].Name != "renamed" {
		t.Fatalf("rename duplicated/reidentified library: %+v", libs)
	}
}

func TestAuditConcurrentConfigReadWrite(t *testing.T) {
	cfg := config.Defaults()
	s := &Server{Deps: Deps{Cfg: cfg}}
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for i := 0; i < 100; i++ {
			r := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(`{"auto_download":false,"new_add_first":false}`))
			s.handlePatchConfig(httptest.NewRecorder(), r)
		}
	}()
	close(start)
	for i := 0; i < 100; i++ {
		_ = configPayload(cfg)
	}
	<-done
}

func TestAuditConfigMediaChangeUpdatesEffectiveLibrary(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Normalize()
	st, e := store.Open(filepath.Join(cfg.DataDir, "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = st.EnsureDefaultLibrary(cfg.Media.Root, "movie", "copy"); e != nil {
		t.Fatal(e)
	}
	s := &Server{Deps: Deps{Cfg: cfg, St: st}}
	r := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(`{"media_layout":"show","media_link_mode":"strm"}`))
	w := httptest.NewRecorder()
	s.handlePatchConfig(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	libs, e := st.ListMediaLibraries()
	if e != nil {
		t.Fatal(e)
	}
	if libs[0].Layout != "show" || libs[0].LinkMode != "strm" {
		t.Fatalf("saved layout=%s mode=%s; effective layout=%s mode=%s", cfg.Media.Layout, cfg.Media.LinkMode, libs[0].Layout, libs[0].LinkMode)
	}
}

func TestFailedSettingsPersistenceKeepsSnapshot(t *testing.T) {
	st, e := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	cfg := config.Defaults()
	s := &Server{Deps: Deps{St: st, Cfg: cfg}}
	if _, e = st.DB.Exec(`DROP TABLE runtime_settings`); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(`{"auto_download":false}`))
	w := httptest.NewRecorder()
	s.handlePatchConfig(w, r)
	if w.Code < 400 || !cfg.View().Download.AutoDownload {
		t.Fatal("failed persistence changed runtime state", w.Code)
	}
}
func TestSettingsRevisionAndEnvironmentBoundaries(t *testing.T) {
	st, e := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	cfg := config.Defaults()
	s := &Server{Deps: Deps{St: st, Cfg: cfg}}
	save := func(rev string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/config", strings.NewReader(`{"auto_download":false}`))
		r.Header.Set("If-Match", rev)
		w := httptest.NewRecorder()
		s.handlePatchConfig(w, r)
		return w
	}
	if w := save("0"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := save("0"); w.Code != 409 {
		t.Fatal("stale revision accepted", w.Code)
	}
	t.Setenv("BILI_SAVER_DOWNLOAD_AUTO_DOWNLOAD", "true")
	if w := save("1"); w.Code != 400 {
		t.Fatal("overrode environment", w.Code)
	}
}
