package biliapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAuditSubtitlePreservesRawSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"body":[{"from":0,"to":1,"content":"sample"}]}`))
	}))
	defer srv.Close()
	c, e := New("", 1, "test")
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	c.SetSnapshotHook(func(string, url.Values, int, []byte) error { count++; return nil })
	if _, e = c.SubtitleCC(context.Background(), srv.URL); e != nil {
		t.Fatal(e)
	}
	if count == 0 {
		t.Fatal("CC subtitle body bypasses raw snapshot hook")
	}
}

func TestSnapshotFailurePreventsParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"code":0,"data":{"bvid":"BVtest"}}`)) }))
	defer srv.Close()
	c, _ := New("", 1, "test")
	c.SetBase(srv.URL, srv.URL)
	c.SetSnapshotHook(func(string, url.Values, int, []byte) error { return errors.New("disk full") })
	if _, err := c.VideoView(context.Background(), "BVtest"); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("snapshot failure was ignored: %v", err)
	}
}
