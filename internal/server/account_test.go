package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func TestAccountSelfServiceAndAccessBoundaries(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "account.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Admin.Token = "account-admin-token"
	u, err := st.CreateUser("self-alice", "old-password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateUser("self-bob", "bob-password", "user", 1); err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := st.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Cfg: cfg, St: st}).Handler()
	call := func(method, path, body, origin, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "bs_sess", Value: token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/api/v1/account", "", "", sess)
	if w.Code != 200 || strings.Contains(w.Body.String(), "pass_hash") || strings.Contains(w.Body.String(), "old-password") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("PATCH", "/api/v1/account", `{"username":"新用户名"}`, "", sess)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		UID      int64  `json:"uid"`
		Username string `json:"username"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.UID != u.ID || got.Username != "新用户名" {
		t.Fatal(got, err)
	}
	for _, tc := range []struct {
		body, origin string
		status       int
	}{
		{`{"username":"self-bob"}`, "", 409},
		{`{"username":"safe","uid":1,"role":"admin"}`, "", 400},
		{`{"username":"safe"}`, "http://evil.example", 403},
		{`{"username":"safe"}`, "http://example.com:9999", 403},
		{`{"username":"safe"} {}`, "", 400},
	} {
		w = call("PATCH", "/api/v1/account", tc.body, tc.origin, sess)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if w = call("GET", "/api/v1/account", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/api/v1/account", nil)
	r.Header.Set("Authorization", "Bearer account-admin-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("token has no personal identity", w.Code)
	}
	w = call("POST", "/api/v1/account/password", `{"current_password":"wrong","new_password":"new-password"}`, "", sess)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	if _, err = st.UserBySession(sess); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/v1/account/password", `{"current_password":"old-password","new_password":"new-password"}`, "", sess)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, token := range []string{sess, otherSession} {
		if w = call("GET", "/api/v1/account", "", "", token); w.Code != 401 {
			t.Fatal("session not revoked", w.Code)
		}
	}
	w = call("POST", "/api/v1/auth/login", `{"username":"新用户名","password":"new-password"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", "/api/v1/auth/login", `{"username":"self-alice","password":"old-password"}`, "", "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestAccountAvatarDecodedAndNormalized(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "avatar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	u, err := st.CreateUser("avatar-user", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Cfg: cfg, St: st}).Handler()
	img := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	img.Set(10, 5, color.NRGBA{R: 240, G: 110, B: 150, A: 255})
	var valid bytes.Buffer
	if err = png.Encode(&valid, img); err != nil {
		t.Fatal(err)
	}
	upload := func(data []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("file", "../../payload.html")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(data)
		mw.Close()
		r := httptest.NewRequest("POST", "/api/v1/account/avatar", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.AddCookie(&http.Cookie{Name: "bs_sess", Value: sess})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), bytes.Repeat([]byte("x"), (2<<20)+1)} {
		if w := upload(data); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := upload(valid.Bytes())
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, err := st.GetUser(u.ID)
	if err != nil || got.ID != u.ID || !strings.HasSuffix(got.AvatarPath, ".png") {
		t.Fatal(got, err)
	}
	f, err := os.Open(filepath.Join(cfg.DataDir, filepath.FromSlash(got.AvatarPath)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := png.DecodeConfig(f)
	f.Close()
	if err != nil || c.Width != 256 || c.Height != 256 {
		t.Fatal(c, err)
	}
	r := httptest.NewRequest("GET", got.AvatarURL, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal(w.Code, w.Header())
	}
	r = httptest.NewRequest("DELETE", "/api/v1/account/avatar", nil)
	r.AddCookie(&http.Cookie{Name: "bs_sess", Value: sess})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	got, err = st.GetUser(u.ID)
	if err != nil || got.AvatarURL != "" {
		t.Fatal(got, err)
	}
}
