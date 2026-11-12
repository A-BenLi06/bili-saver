package server

import (
	"bili-saver/internal/config"
	"reflect"
)

type settingsRequest struct {
	SyncDefaultMinutes *int      `json:"sync_default_minutes" setting:"collector.sync_default_minutes"`
	StatsResyncHours   *int      `json:"stats_resync_hours" setting:"collector.stats_resync_hours"`
	AdaptiveSync       *bool     `json:"adaptive_sync" setting:"collector.adaptive_sync"`
	NewAddFirst        *bool     `json:"new_add_first" setting:"queue.new_add_first"`
	CommentsMax        *int      `json:"comments_max" setting:"collector.comments_max"`
	SubRepliesMax      *int      `json:"sub_replies_max" setting:"collector.sub_replies_max"`
	FullResyncComments *bool     `json:"full_resync_comments" setting:"collector.full_resync_comments"`
	CommentConcurrency *int      `json:"comment_concurrency" setting:"collector.comment_concurrency"`
	CommentFullHours   *int      `json:"comment_full_hours" setting:"collector.comment_full_hours"`
	HotVideoViews      *int      `json:"hot_video_views" setting:"collector.hot_video_views"`
	HotVideoReplies    *int      `json:"hot_video_replies" setting:"collector.hot_video_replies"`
	AutoDownload       *bool     `json:"auto_download" setting:"download.auto_download"`
	DownloadDanmaku    *bool     `json:"download_danmaku" setting:"download.download_danmaku"`
	Concurrency        *int      `json:"concurrency" setting:"download.concurrency"`
	Segments           *int      `json:"segments" setting:"download.segments"`
	AllowAV1           *bool     `json:"allow_av1" setting:"download.allow_av1"`
	Allow4K            *bool     `json:"allow_4k" setting:"download.allow_4k"`
	Qualities          *[]string `json:"qualities" setting:"download.qualities"`
	AudioQualities     *[]string `json:"audio_qualities" setting:"download.audio_qualities"`
	MinReqMs           *int      `json:"min_req_ms" setting:"bilibili.min_req_ms"`
	MediaEnabled       *bool     `json:"media_enabled" setting:"media.enabled"`
	MediaLayout        *string   `json:"media_layout" setting:"media.layout"`
	MediaLinkMode      *string   `json:"media_link_mode" setting:"media.link_mode"`
	MediaPublicURL     *string   `json:"media_public_url" setting:"media.public_url"`
	TeriteriEnabled    *bool     `json:"teriteri_enabled" setting:"teriteri.enabled"`
	FrontendURL        *string   `json:"frontend_url" setting:"teriteri.frontend_url"`
}

func settingSources() map[string]string {
	out := map[string]string{}
	t := reflect.TypeOf(settingsRequest{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("setting")
		if config.EnvControlled(key) {
			out[f.Tag.Get("json")] = config.EnvName(key)
		}
	}
	return out
}
