package app

import (
	"bili-saver/internal/jobs"
	"bili-saver/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

func (a *App) jobUserDownload(ctx context.Context, j *store.Job, progress jobs.ProgressFn) (retErr error) {
	var p struct {
		Bvid      string `json:"bvid"`
		RequestID int64  `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(j.PayloadJSON), &p); err != nil {
		return err
	}
	if p.RequestID <= 0 {
		return fmt.Errorf("下载请求无效")
	}
	active, err := a.St.BeginUserDownload(p.RequestID, j.ID)
	if err != nil {
		return err
	}
	if !active {
		return a.St.SkipUserDownload(p.RequestID, j.ID)
	}
	complete := a.St.VideoFilesPresent(a.Cfg.DataDir, p.Bvid)
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("下载异常: %v", r)
		}
		if err := a.St.FinishUserDownload(p.RequestID, retErr == nil, !complete, retErr); err != nil {
			retErr = fmt.Errorf("保存下载额度: %w", err)
		}
	}()
	if !complete {
		if err := a.Dl.DownloadVideo(ctx, p.Bvid, func(f float64, detail string) { progress(f, detail) }); err != nil {
			return err
		}
		if !a.St.VideoFilesPresent(a.Cfg.DataDir, p.Bvid) {
			return fmt.Errorf("下载后分 P 文件不完整")
		}
		// Refresh is best effort; quota must reflect the file download, not derived rendering.
		a.refreshCard(ctx, p.Bvid)
		if a.Cfg.Media.Enabled && a.Med != nil {
			go func() { _ = a.Med.RebuildAllDebounced() }()
		}
	} else {
		v, err := a.St.GetVideo(p.Bvid)
		if err != nil {
			return err
		}
		local := v.LocalPath
		if local == "" {
			pages, err := a.St.ListPages(p.Bvid)
			if err != nil {
				return err
			}
			local = filepath.ToSlash(filepath.Join("media", p.Bvid, fmt.Sprintf("p%d", pages[0].Page), fmt.Sprintf("%s-p%d.mp4", p.Bvid, pages[0].Page)))
		}
		if err := a.St.SetVideoDownloaded(p.Bvid, local); err != nil {
			return err
		}
	}
	progress(1, "完成")
	return nil
}

// attachSourceVideo records all memberships, including already archived videos.
func (a *App) attachSourceVideo(src *store.Source, bvid string, auto bool, priority int) error {
	if _, err := a.St.DB.Exec(`INSERT OR IGNORE INTO source_videos(source_id,bvid) VALUES(?,?)`, src.ID, bvid); err != nil {
		return err
	}
	had, err := a.St.UserHasVideo(src.UserID, bvid)
	if err != nil {
		return err
	}
	if err = a.St.AddUserVideo(src.UserID, bvid); err != nil {
		return err
	}
	if auto && (!had || src.LastSyncAt == 0) {
		u, err := a.St.GetUser(src.UserID)
		if err == nil && u.Role == "user" {
			_, err = a.St.RequestUserDownload(u.ID, bvid, priority, false)
			return err
		}
	}
	return nil
}
