// site.go TeriTeri 站内互动层：点赞/投币/收藏/评论赞/发表评论/发送弹幕。
// 全部落 site_* 表（归档站社区数据），展示时与 B 站 origin 快照合并：
// 点赞数=站内+B站、投币数=站内+B站、收藏数=站内（B站收藏曲线仅存档展示）。
package teriteri

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bili-saver/internal/store"
)

// registerSite 互动端点（覆盖此前的 no-op 占位注册）。
func (a *Adapter) registerSite(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/video/love-or-not", a.wrap(a.videoLoveOrNot, true))
	mux.HandleFunc("POST /api/comment/love-or-not", a.wrap(a.commentLoveOrNot, true))
	mux.HandleFunc("POST /api/comment/add", a.wrap(a.commentAdd, true))
	mux.HandleFunc("POST /api/favorite/create", a.wrap(a.favoriteCreate, true))
	mux.HandleFunc("POST /api/video/collect", a.wrap(a.videoCollect, true))
	mux.HandleFunc("POST /api/video/cancel-collect", a.wrap(a.videoCancelCollect, true))
	mux.HandleFunc("GET /api/video/user-love", a.wrap(a.videoUserLove, true))
	mux.HandleFunc("GET /api/video/user-collect", a.wrap(a.videoUserCollect, true))
	mux.HandleFunc("GET /api/favorite/get-all/user", a.wrap(a.favoriteUserReal, true))
	mux.HandleFunc("GET /api/favorite/get-all/visitor", a.wrap(a.favoriteVisitorReal, false))
	mux.HandleFunc("GET /api/comment/get-like-and-dislike", a.wrap(a.commentGetLikeDislike, true))
	mux.HandleFunc("GET /api/video/collected-fids", a.wrap(a.collectedFids, true))
	mux.HandleFunc("POST /api/comment/delete", a.wrap(a.commentDelete, true))
	mux.HandleFunc("POST /api/danmu/delete", a.wrap(a.danmuDelete, true))
	mux.HandleFunc("POST /api/search/word/add", a.wrap(a.searchWordAdd, false))
	mux.HandleFunc("GET /api/search/word/get", a.wrap(a.searchWordGet, false))
	mux.HandleFunc("GET /api/search/user", a.wrap(a.searchUser, false))
	mux.HandleFunc("POST /api/user/avatar/update", a.wrap(a.userAvatarUpdate, true))
	mux.HandleFunc("POST /api/user/info/update", a.wrap(a.userInfoUpdate, true))
	mux.HandleFunc("POST /api/user/password/update", a.wrap(a.userPasswordUpdate, true))
}

// videoLoveOrNot FormData{vid, isLove, isSet}。
func (a *Adapter) videoLoveOrNot(w http.ResponseWriter, r *http.Request, uid int64) {
	vid := r.FormValue("vid")
	isLove := r.FormValue("isLove") == "true"
	isSet := r.FormValue("isSet") == "true"
	bvid, _ := splitVid(vid)
	if bvid == "" || strings.EqualFold(bvid, "NaN") {
		a.fail(w, 400, "缺少 vid")
		return
	}
	love, unlove := false, false
	if isSet {
		if isLove {
			love = true
		} else {
			unlove = true
		}
	}
	if err := a.St.SetVideoAttitude(uid, bvid, love, unlove); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// videoUserLove ?uid= 用户点过赞的视频卡片（空间“最近点赞”）。
func (a *Adapter) videoUserLove(w http.ResponseWriter, r *http.Request, _ int64) {
	uid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	if uid == 0 {
		uid = 1
	}
	bvids, err := a.St.ListLovedBvids(uid)
	if err != nil {
		a.ok(w, []any{})
		return
	}
	out := make([]map[string]any, 0, len(bvids))
	for _, b := range bvids {
		if v, err := a.St.GetVideo(b); err == nil {
			out = append(out, a.cardMap(v))
		}
	}
	a.ok(w, out)
}

// videoUserCollect ?uid= 用户收藏的全部视频卡片（跨夹合并）。
func (a *Adapter) videoUserCollect(w http.ResponseWriter, r *http.Request, _ int64) {
	uid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	if uid == 0 {
		uid = 1
	}
	folders, err := a.St.ListFavFolders(uid)
	if err != nil {
		a.ok(w, []any{})
		return
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, 8)
	for _, f := range folders {
		bvids, err := a.St.ListFavBvids(f.Fid)
		if err != nil {
			continue
		}
		for _, b := range bvids {
			if seen[b] {
				continue
			}
			seen[b] = true
			if v, err := a.St.GetVideo(b); err == nil {
				out = append(out, a.cardMap(v))
			}
		}
	}
	a.ok(w, out)
}

// commentLoveOrNot FormData{id(rpid), isLike, isSet}。
func (a *Adapter) commentLoveOrNot(w http.ResponseWriter, r *http.Request, uid int64) {
	rpid, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	isLike := r.FormValue("isLike") == "true"
	isSet := r.FormValue("isSet") == "true"
	if rpid == 0 {
		a.fail(w, 400, "缺少评论 id")
		return
	}
	if err := a.St.SetCommentLove(uid, rpid, isLike && isSet); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// commentAdd FormData{vid, root_id, parent_id, to_user_id, content} → 返回新评论对象。
func (a *Adapter) commentAdd(w http.ResponseWriter, r *http.Request, uid int64) {
	vid := r.FormValue("vid")
	content := strings.TrimSpace(r.FormValue("content"))
	rootID, _ := strconv.ParseInt(r.FormValue("root_id"), 10, 64)
	parentID, _ := strconv.ParseInt(r.FormValue("parent_id"), 10, 64)
	if vid == "" || content == "" {
		a.fail(w, 400, "评论内容不能为空")
		return
	}
	bvid, _ := splitVid(vid)
	if _, err := a.St.GetVideo(bvid); err != nil {
		a.fail(w, 404, "视频不存在或未归档")
		return
	}
	if rootID == parentID {
		parentID = 0 // 直接回复主楼时前端会传相同值，归一化为 0
	}
	if rootID != 0 {
		if _, err := a.St.GetSiteComment(rootID); err != nil {
			// 回复 B 站评论：root 是 B 站 rpid，parent 归一为 root
			parentID = 0
		}
	}
	_ = parentID
	id, err := a.St.AddSiteComment(uid, bvid, rootID, parentID, 0, content)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	sc, _ := a.St.GetSiteComment(id)
	a.ok(w, a.siteCommentToT(sc))
}

// siteCommentToT 站内评论 → teriteri 评论对象（昵称即站长）。
func (a *Adapter) siteCommentToT(c *store.SiteComment) TComment {
	user := TUser{UID: c.UID, Nickname: a.AdminUser, Gender: 2}
	if c.UID != 1 {
		user.Nickname = fmt.Sprintf("站内用户%d", c.UID)
	}
	return TComment{
		ID: c.ID, Vid: c.Bvid, RootID: c.RootID, ParentID: c.ParentID,
		Content: c.Content, CreateTime: isoDate(c.Ctime),
		Replies: []TComment{}, ToUser: TUser{}, User: user,
	}
}

// favoriteCreate FormData{title, desc, visible} → 新收藏夹对象。
func (a *Adapter) favoriteCreate(w http.ResponseWriter, r *http.Request, uid int64) {
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		a.fail(w, 400, "收藏夹名字不能为空")
		return
	}
	fid, err := a.St.CreateFavFolder(uid, title)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, map[string]any{"fid": fid, "mid": uid, "title": title, "name": title,
		"type": 0, "visible": 1, "count": 0, "ctime": store.Now()})
}

// videoCollect FormData{vid, adds:"fid,..", removes:"fid,.."}。
func (a *Adapter) videoCollect(w http.ResponseWriter, r *http.Request, uid int64) {
	vid := r.FormValue("vid")
	bvid, _ := splitVid(vid)
	if bvid == "" || strings.EqualFold(bvid, "NaN") {
		a.fail(w, 400, "缺少 vid")
		return
	}
	apply := func(list string, add bool) error {
		for _, s := range strings.Split(list, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			fid, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				continue
			}
			if add {
				if err := a.St.AddFavItem(fid, bvid, uid); err != nil {
					return err
				}
			} else if err := a.St.RemoveFavItem(fid, bvid); err != nil {
				return err
			}
		}
		return nil
	}
	if err := apply(r.FormValue("adds"), true); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	if err := apply(r.FormValue("removes"), false); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// videoCancelCollect FormData{vid, fid}。
func (a *Adapter) videoCancelCollect(w http.ResponseWriter, r *http.Request, _ int64) {
	bvid, _ := splitVid(r.FormValue("vid"))
	fid, _ := strconv.ParseInt(r.FormValue("fid"), 10, 64)
	if bvid == "" {
		a.fail(w, 400, "缺少 vid")
		return
	}
	if err := a.St.RemoveFavItem(fid, bvid); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// cardMap 视频卡片（map 形态，user 含 avatar_url 契约）。
func (a *Adapter) cardMap(v *store.Video) map[string]any {
	play, danmu := 0, 0
	if hist, err := a.St.ListStats(v.Bvid); err == nil && len(hist) > 0 {
		play, danmu = hist[len(hist)-1].View, hist[len(hist)-1].Danmaku
	}
	return map[string]any{
		"video": map[string]any{
			"vid": v.Bvid, "title": v.Title, "coverUrl": a.coverURL(v),
			"duration": v.Duration, "uploadDate": isoDate(v.Pubdate),
		},
		"user":  map[string]any{"uid": v.OwnerMid, "nickname": v.OwnerName, "avatar_url": v.OwnerFace},
		"stats": map[string]any{"play": play, "danmu": danmu},
		"_play": play,
	}
}

// ---- 收藏夹列表（真实数据，替换 extras 的占位实现） ----

func (a *Adapter) favoriteUserReal(w http.ResponseWriter, r *http.Request, uid int64) {
	folders, err := a.St.ListFavFolders(uid)
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, a.foldersJSON(uid, folders))
}

func (a *Adapter) favoriteVisitorReal(w http.ResponseWriter, r *http.Request, _ int64) {
	uid, _ := strconv.ParseInt(r.FormValue("uid"), 10, 64)
	if uid <= 0 {
		uid = 1
	}
	folders, err := a.St.ListFavFolders(uid)
	if err != nil {
		a.ok(w, []any{})
		return
	}
	a.ok(w, a.foldersJSON(uid, folders))
}

// foldersJSON 客户端收藏夹对象：title/name 双字段、visible、count。
func (a *Adapter) foldersJSON(uid int64, folders []*store.FavFolder) []map[string]any {
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, map[string]any{
			"fid": f.Fid, "mid": uid, "title": f.Name, "name": f.Name,
			"type": f.Type, "visible": 1, "count": f.Count, "cover": "",
		})
	}
	return out
}

// ---- WS 弹幕：解析客户端帧 → 入库 → 回显（前端 push 进弹幕列表） ----

// wsDanmuFrame 客户端发送格式 {token, data:{content,mode,color,fontsize,timePoint}}。
type wsDanmuFrame struct {
	Token string `json:"token"`
	Data  struct {
		Content   string `json:"content"`
		Mode      int    `json:"mode"`
		Color     int64  `json:"color"`
		Fontsize  int    `json:"fontsize"`
		TimePoint int    `json:"timePoint"`
	} `json:"data"`
}

// parseWSFrame 解析一条客户端→服务端文本帧（RFC6455，客户端帧必须掩码）。
// 返回 payload 与消耗的总字节数；数据不足返回 ok=false。
func parseWSFrame(buf []byte) (payload []byte, total int, ok bool) {
	if len(buf) < 2 {
		return nil, 0, false
	}
	opcode := buf[0] & 0x0F
	masked := buf[1]&0x80 != 0
	length := int(buf[1] & 0x7F)
	off := 2
	switch length {
	case 126:
		if len(buf) < 4 {
			return nil, 0, false
		}
		length = int(buf[2])<<8 | int(buf[3])
		off = 4
	case 127:
		if len(buf) < 10 {
			return nil, 0, false
		}
		length = 0
		for i := 2; i < 10; i++ {
			length = length<<8 | int(buf[i])
		}
		off = 10
	}
	maskLen := 0
	if masked {
		maskLen = 4
	}
	total = off + maskLen + length
	if len(buf) < total {
		return nil, 0, false
	}
	payload = buf[off : off+length]
	if masked {
		mask := buf[off : off+4]
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode == 8 { // close
		return nil, total, true
	}
	return payload, total, true
}

// writeWSTextFrame 服务端→客户端文本帧（服务端帧不掩码）。
func writeWSTextFrame(payload []byte) []byte {
	var header []byte
	header = append(header, 0x81) // FIN + text
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n < 65536:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		for shift := 56; shift >= 0; shift -= 8 {
			header = append(header, byte(n>>shift))
		}
	}
	return append(header, payload...)
}

// wsDanmu 保持握手 + 解析弹幕帧入库并回显（替换原 discard 占位）。
func (a *Adapter) wsDanmu(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected websocket", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	accept := wsAccept(key)
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", accept)
	_ = buf.Flush()

	vid := r.PathValue("vid")
	// 观看人数提示（前端解析 “当前观看人数N”）
	_, _ = conn.Write(writeWSTextFrame([]byte("当前观看人数 1")))

	readBuf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := conn.Read(chunk)
		if err != nil {
			return
		}
		readBuf = append(readBuf, chunk[:n]...)
		for len(readBuf) > 0 {
			payload, total, ok := parseWSFrame(readBuf)
			if !ok {
				break
			}
			readBuf = readBuf[total:]
			if len(payload) == 0 {
				continue
			}
			a.handleDanmuFrame(vid, payload, conn)
		}
	}
}

// handleDanmuFrame 校验 token → 入库 → 回显弹幕对象给发送者。
func (a *Adapter) handleDanmuFrame(vid string, payload []byte, conn interface{ Write([]byte) (int, error) }) {
	var f wsDanmuFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return
	}
	uid := int64(0)
	if tok := strings.TrimSpace(f.Token); tok != "" {
		if u, err := a.parseJWT(strings.TrimPrefix(tok, "Bearer ")); err == nil {
			uid = u
		}
	}
	if uid == 0 {
		_, _ = conn.Write(writeWSTextFrame([]byte("登录已过期")))
		return
	}
	bvid, page := splitVid(vid)
	if f.Data.Content == "" {
		return
	}
	d := &store.SiteDanmaku{
		Bvid: bvid, UID: uid,
		TimePoint: f.Data.TimePoint, Mode: f.Data.Mode,
		Fontsize: f.Data.Fontsize, Color: f.Data.Color,
		Content: f.Data.Content, Ctime: store.Now(),
	}
	id, err := a.St.AddSiteDanmaku(d)
	if err != nil {
		return
	}
	d.ID = id
	item, _ := json.Marshal(map[string]any{
		"id": id, "uid": uid, "timePoint": d.TimePoint, "mode": d.Mode,
		"fontsize": d.Fontsize, "color": d.Color, "content": d.Content,
		"createDate": isoDate(d.Ctime), "state": 0, "active": true,
	})
	_, _ = conn.Write(writeWSTextFrame(item))
	_ = page
}

// ---- 账号自助 / 搜索联想 / 删除（此前为占位） ----

// commentDelete FormData{id}：仅站内评论可删（B站 rpid 不在 site_comments，幂等成功）。
func (a *Adapter) commentDelete(w http.ResponseWriter, r *http.Request, uid int64) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id == 0 {
		a.fail(w, 400, "缺少评论 id")
		return
	}
	if _, err := a.St.DeleteSiteComment(id, uid); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// danmuDelete FormData{id}：仅站内弹幕可删。
func (a *Adapter) danmuDelete(w http.ResponseWriter, r *http.Request, uid int64) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id == 0 {
		a.fail(w, 400, "缺少弹幕 id")
		return
	}
	if _, err := a.St.DeleteSiteDanmaku(id, uid); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, true)
}

// searchWordAdd FormData{keyword}：入站内词库（联想排序按新→旧）。
func (a *Adapter) searchWordAdd(w http.ResponseWriter, r *http.Request, uid int64) {
	kw := strings.TrimSpace(clientKeyword(r.FormValue("keyword")))
	if kw == "" {
		a.ok(w, true)
		return
	}
	_ = a.St.AddSearchWord(uid, kw)
	a.ok(w, true)
}

// searchWordGet ?keyword=：词库 + 归档标题/标签联想。
func (a *Adapter) searchWordGet(w http.ResponseWriter, r *http.Request, _ int64) {
	kw := clientKeyword(r.FormValue("keyword"))
	a.ok(w, a.St.SearchWordSuggestions(kw, 10))
}

// searchUser ?keyword=：归档站单用户，昵称命中即返回站长卡片。
func (a *Adapter) searchUser(w http.ResponseWriter, r *http.Request, _ int64) {
	kw := strings.TrimSpace(clientKeyword(r.FormValue("keyword")))
	u := a.adminUser()
	if kw != "" && strings.Contains(u.Nickname, kw) {
		a.ok(w, []TUser{u})
		return
	}
	a.ok(w, []any{})
}

// commentGetLikeDislike 用户赞过的评论 id 集合（重载后回显高亮）。
func (a *Adapter) commentGetLikeDislike(w http.ResponseWriter, _ *http.Request, uid int64) {
	liked, err := a.St.ListLikedRpids(uid)
	if err != nil {
		liked = nil
	}
	a.ok(w, map[string]any{"userLike": liked, "userDislike": []any{}})
}

// collectedFids ?vid=：该视频已被收藏进哪些夹（收藏弹窗预勾选）。
func (a *Adapter) collectedFids(w http.ResponseWriter, r *http.Request, uid int64) {
	bvid, _ := splitVid(r.FormValue("vid"))
	if bvid == "" || strings.EqualFold(bvid, "NaN") {
		a.ok(w, []any{})
		return
	}
	fids, err := a.St.ListFavItemFids(uid, bvid)
	if err != nil {
		a.ok(w, []any{})
		return
	}
	a.ok(w, fids)
}

// userAvatarUpdate FormData{file}：头像存 dataDir/media/avatars，返回可访问 URL。
func (a *Adapter) userAvatarUpdate(w http.ResponseWriter, r *http.Request, uid int64) {
	_ = r.ParseMultipartForm(8 << 20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		a.fail(w, 400, "缺少头像文件")
		return
	}
	defer file.Close()
	ext := ".png"
	if e := filepath.Ext(hdr.Filename); e != "" && len(e) <= 5 {
		ext = strings.ToLower(e)
	}
	dir := filepath.Join(a.DataDir, "media", "avatars")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	rel := fmt.Sprintf("media/avatars/avatar_%d_%d%s", uid, time.Now().UnixMilli(), ext)
	dst, err := os.Create(filepath.Join(a.DataDir, filepath.FromSlash(rel)))
	if err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	url := "/media/" + rel
	_ = a.St.SetProfileAvatar(uid, rel)
	a.ok(w, url)
}

// userInfoUpdate FormData{nickname, description, gender} → 更新后的用户对象。
func (a *Adapter) userInfoUpdate(w http.ResponseWriter, r *http.Request, uid int64) {
	_ = r.ParseMultipartForm(1 << 20)
	nickname := strings.TrimSpace(r.FormValue("nickname"))
	descr := r.FormValue("description")
	gender, _ := strconv.Atoi(r.FormValue("gender"))
	if nickname == "" {
		a.fail(w, 400, "昵称不能为空")
		return
	}
	if gender < 0 || gender > 2 {
		gender = 2
	}
	if err := a.St.UpsertProfile(uid, nickname, descr, gender); err != nil {
		a.fail(w, 500, err.Error())
		return
	}
	a.ok(w, a.adminUser())
}

// userPasswordUpdate FormData{pw, npw}：校验旧密码后经 Hook 持久化到配置。
func (a *Adapter) userPasswordUpdate(w http.ResponseWriter, r *http.Request, uid int64) {
	_ = r.ParseMultipartForm(1 << 20)
	pw := r.FormValue("pw")
	npw := r.FormValue("npw")
	if pw != a.AdminPass {
		a.fail(w, 400, "原密码错误")
		return
	}
	if len(npw) < 6 {
		a.fail(w, 400, "新密码至少 6 位")
		return
	}
	if a.PasswordHook != nil {
		if err := a.PasswordHook(npw); err != nil {
			a.fail(w, 500, err.Error())
			return
		}
	}
	a.AdminPass = npw
	a.ok(w, true)
}
