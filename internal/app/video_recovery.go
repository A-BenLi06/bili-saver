package app

import (
	"context"
	"errors"

	"bili-saver/internal/store"
)

// 轻量存在性检查确认恢复后立即排发到期部分，无需等下一轮源调度。
// 仍遵守源的“从不刷新”开关与下载策略，不对仍 gone 的视频请求子接口。
func (a *App) queueRestoredParts(ctx context.Context, bvid string, sourceID int64, download *bool, priority int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v, err := a.St.GetVideo(bvid)
	if err != nil {
		return err
	}
	if v.Status != "ok" {
		return nil
	}
	src, err := a.St.GetSource(sourceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} // 源可能已被用户移除。
	if err != nil {
		return err
	}
	_, comments, dm, subs := a.dueParts(src, v, store.Now())
	if !comments && !dm && !subs {
		return nil
	}
	dl := a.Cfg.View().Download.AutoDownload
	if download != nil {
		dl = *download
	}
	_, err = a.Mgr.SubmitPrio("archive", map[string]any{
		"bvid": bvid, "download": dl, "source_id": sourceID,
		"refresh_comments": comments, "refresh_danmaku": dm, "refresh_subtitles": subs,
	}, priority)
	return err
}
