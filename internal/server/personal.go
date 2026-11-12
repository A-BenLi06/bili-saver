package server

import (
	"bili-saver/internal/store"
	"net/http"
)

func (s *Server) personalVideo(w http.ResponseWriter, r *http.Request) (*store.Video, bool) {
	au := s.currentUser(r)
	bvid := r.PathValue("bvid")
	has, err := s.Deps.St.UserHasVideo(au.ID, bvid)
	if err != nil {
		errJSON(w, 500, err)
		return nil, false
	}
	if !has {
		errJSON(w, 404, errStr("视频不存在"))
		return nil, false
	}
	v, err := s.Deps.St.GetVideo(bvid)
	if err != nil {
		errJSON(w, 404, errStr("视频尚未完成归档"))
		return nil, false
	}
	v.LocalPath = ""
	v.SourceID = 0
	return v, true
}

func (s *Server) handlePersonalVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.personalVideo(w, r)
	if !ok {
		return
	}
	pages, err := s.Deps.St.ListPages(v.Bvid)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	req, _ := s.Deps.St.UserDownload(s.currentUser(r).ID, v.Bvid)
	// Own source metadata only; a shared video's first source may belong to somebody else.
	srcs, err := s.Deps.St.ListUserVideoSources(s.currentUser(r).ID, v.Bvid)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"video": v, "pages": pages, "sources": srcs, "download_request": req, "watch_url": s.teriteriVideoURL(r, v.Bvid)})
}

func (s *Server) handlePersonalDownload(w http.ResponseWriter, r *http.Request) {
	v, ok := s.personalVideo(w, r)
	if !ok {
		return
	}
	if v.Status != "ok" {
		errJSON(w, 400, errStr("视频当前无法下载，已保留归档数据"))
		return
	}
	req, err := s.Deps.St.RequestUserDownload(s.currentUser(r).ID, v.Bvid, 1, true)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	if s.Deps.Mgr != nil {
		if err = s.Deps.Mgr.PumpUserDownloads(s.Deps.Cfg.DataDir); err != nil {
			errJSON(w, 500, err)
			return
		}
	}
	req, err = s.Deps.St.UserDownload(s.currentUser(r).ID, v.Bvid)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	state := req.State
	if state == "done" {
		state = "downloaded"
	} else if state == "waiting" {
		state = "waiting_quota"
	} else if state == "running" {
		state = "queued"
	}
	writeJSON(w, 200, map[string]any{"state": state, "download_request": req})
}
