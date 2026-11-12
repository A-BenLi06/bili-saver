package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bili-saver/internal/store"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func accountPayload(u *store.User) map[string]any {
	return map[string]any{"uid": u.ID, "user_id": u.ID, "username": u.Username, "avatar_url": u.AvatarURL, "role": u.Role, "created_at": u.CreatedAt}
}

func (s *Server) ownAccount(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	au := s.currentUser(r)
	if au.ID <= 0 {
		errJSON(w, 403, errStr("请使用用户名和密码登录后管理个人账号"))
		return nil, false
	}
	u, err := s.Deps.St.GetUser(au.ID)
	if err != nil || !u.Enabled {
		errJSON(w, 401, errStr("登录已失效，请重新登录"))
		return nil, false
	}
	if r.Method != "GET" {
		origin := r.Header.Get("Origin")
		if origin != "" {
			o, err := url.Parse(origin)
			if err != nil || (o.Scheme != "http" && o.Scheme != "https") || !strings.EqualFold(o.Host, r.Host) || o.User != nil || o.Path != "" || o.RawQuery != "" || o.Fragment != "" {
				errJSON(w, 403, errStr("请从当前站点的账号设置操作"))
				return nil, false
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			errJSON(w, 403, errStr("请从当前站点的账号设置操作"))
			return nil, false
		}
	}
	return u, true
}

func decodeAccountJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		errJSON(w, 400, errStr("账号设置参数无效"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		errJSON(w, 400, errStr("账号设置参数无效"))
		return false
	}
	return true
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	u, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, accountPayload(u))
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if !decodeAccountJSON(w, r, &req) {
		return
	}
	name, err := store.ValidateUsername(req.Username)
	if err != nil {
		errJSON(w, 400, err)
		return
	}
	u, err = s.Deps.St.UpdateOwnAccount(u.ID, &name, nil)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			errJSON(w, 409, err)
		} else {
			errJSON(w, 500, errStr("保存账号资料失败"))
		}
		return
	}
	writeJSON(w, 200, accountPayload(u))
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeAccountJSON(w, r, &req) {
		return
	}
	if req.CurrentPassword == "" {
		errJSON(w, 400, errStr("请输入当前密码"))
		return
	}
	if err := s.Deps.St.ChangeOwnPassword(u.ID, req.CurrentPassword, req.NewPassword); err != nil {
		errJSON(w, 400, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "bs_sess", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"ok": true, "reauthenticate": true})
}

func (s *Server) handleAccountAvatar(w http.ResponseWriter, r *http.Request) {
	u, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	if r.Method == "DELETE" {
		empty := ""
		u, err := s.Deps.St.UpdateOwnAccount(u.ID, nil, &empty)
		if err != nil {
			errJSON(w, 500, errStr("移除头像失败"))
			return
		}
		writeJSON(w, 200, accountPayload(u))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	if err := r.ParseMultipartForm(3 << 20); err != nil {
		errJSON(w, 400, errStr("请选择不超过 2 MB 的图片"))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		errJSON(w, 400, errStr("请选择头像图片"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		errJSON(w, 400, errStr("头像图片不能超过 2 MB"))
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif" && format != "webp") || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
		errJSON(w, 400, errStr("请选择有效的 PNG、JPEG、GIF 或 WebP 图片，尺寸不超过 4096×4096"))
		return
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		errJSON(w, 400, errStr("无法读取头像图片"))
		return
	}
	bounds := src.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	left := bounds.Min.X + (bounds.Dx()-side)/2
	top := bounds.Min.Y + (bounds.Dy()-side)/2
	avatar := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	draw.CatmullRom.Scale(avatar, avatar.Bounds(), src, image.Rect(left, top, left+side, top+side), draw.Src, nil)
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, avatar); err != nil {
		errJSON(w, 500, errStr("处理头像失败"))
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		errJSON(w, 500, errStr("保存头像失败"))
		return
	}
	dir := filepath.Join(s.Deps.Cfg.DataDir, "media", "user-avatars")
	if err = os.MkdirAll(dir, 0755); err != nil {
		errJSON(w, 500, errStr("无法创建头像目录"))
		return
	}
	name := hex.EncodeToString(random[:]) + ".png"
	full := filepath.Join(dir, name)
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		errJSON(w, 500, errStr("保存头像失败"))
		return
	}
	_, writeErr := f.Write(encoded.Bytes())
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(full)
		errJSON(w, 500, errStr("保存头像失败"))
		return
	}
	rel := "media/user-avatars/" + name
	u, err = s.Deps.St.UpdateOwnAccount(u.ID, nil, &rel)
	if err != nil {
		_ = os.Remove(full)
		errJSON(w, 500, errStr("保存头像资料失败"))
		return
	}
	writeJSON(w, 200, accountPayload(u))
}
