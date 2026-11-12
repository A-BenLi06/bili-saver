package server

import (
	"archive/zip"
	"bili-saver/internal/store"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoreExportRetainsGoneAndExcludesPrivateState(t *testing.T) {
	s := linkFixture(t)
	if err := s.Deps.St.UpsertVideo(&store.Video{Bvid: "BVexport", Title: "gone original", Status: "gone", FirstArchivedAt: 1, LocalPath: "private-node-path"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Deps.St.AddTags("BVexport", []string{"original-tag"}, "bilibili"); err != nil {
		t.Fatal(err)
	}
	if err := s.Deps.St.AddTags("BVexport", []string{"derived-tag"}, "plugin:test"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/archive/export", nil)
	r.Header.Set("Authorization", "Bearer link-test-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("export %d: %s", w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	for _, file := range z.File {
		f, e := file.Open()
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		entries[file.Name] = string(body)
	}
	if !strings.Contains(entries["videos.ndjson"], `"status":"gone"`) || strings.Contains(entries["videos.ndjson"], "private-node-path") {
		t.Fatal("lost origin or exposed node path")
	}
	if !strings.Contains(entries["tags.ndjson"], "original-tag") || strings.Contains(entries["tags.ndjson"], "derived-tag") {
		t.Fatal("tag origin separation failed")
	}
	var manifest map[string]any
	if err = json.Unmarshal([]byte(entries["manifest.json"]), &manifest); err != nil || manifest["kind"] != "origin-record-export" {
		t.Fatal("invalid manifest")
	}
	for _, name := range []string{"users.ndjson", "sessions.ndjson", "runtime_settings.ndjson"} {
		if _, ok := entries[name]; ok {
			t.Fatal("private table exported")
		}
	}
}
func TestGuideAndExportRequireAdmin(t *testing.T) {
	s := linkFixture(t)
	for _, path := range []string{"/api/v1/guide", "/api/v1/archive/export"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
		s.Deps.PluginToken = "read-only-plugin"
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer read-only-plugin")
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
}
