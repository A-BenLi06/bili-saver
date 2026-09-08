package store

// 核心数据模型。origin 层数据来自 B 站；derived 层每条必带 origin/generator/model。
// json tag 统一小写下划线，供 admin API 前端消费。

type Source struct {
	ID                  int64  `json:"id"`
	Type                string `json:"type"`   // video|space|favlist|season|series
	ExtID               string `json:"ext_id"` // bvid / mid / media_id / season_id / series_id
	Extra               string `json:"extra"`  // json: mid, fid, sid 等
	Title               string `json:"title"`
	Watch               bool   `json:"watch"`
	SyncIntervalMinutes int    `json:"sync_interval_minutes"`     // 完整重同步间隔，0=全局默认
	SyncMode            string `json:"sync_mode"`                 // once | interval
	IntervalCommentsMin int    `json:"interval_comments_minutes"` // 0=跟随主间隔，-1=从不
	IntervalDanmakuMin  int    `json:"interval_danmaku_minutes"`
	IntervalSubsMin     int    `json:"interval_subtitles_minutes"`
	Layout              string `json:"layout"` // 媒体库布局覆盖：movie|show|collection|空
	// AutoDownload 新视频触发器（v7）：1=发现新视频自动下载并计入跟踪列表，
	// -1=强制关，0=跟随全局 download.auto_download。
	AutoDownload int   `json:"auto_download"`
	CreatedAt    int64 `json:"created_at"`
	LastSyncAt   int64 `json:"last_sync_at"`
}

type Video struct {
	Bvid            string `json:"bvid"`
	Aid             int64  `json:"aid"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Pic             string `json:"pic"`
	OwnerMid        int64  `json:"owner_mid"`
	OwnerName       string `json:"owner_name"`
	OwnerFace       string `json:"owner_face"`
	OwnerLevel      int    `json:"owner_level"` // UP主B站等级 0-6（v6，space 接口拉取）
	Pubdate         int64  `json:"pubdate"`
	Duration        int    `json:"duration"`
	PagesCount      int    `json:"pages_count"`
	Status          string `json:"status"` // ok|gone|private
	FirstArchivedAt int64  `json:"first_archived_at"`
	LastSyncAt      int64  `json:"last_sync_at"`
	Downloaded      bool   `json:"downloaded"`
	LocalPath       string `json:"local_path"`
	LastStatsAt     int64  `json:"last_stats_at"`
	LastCommentsAt  int64  `json:"last_comments_at"`
	LastDanmakuAt   int64  `json:"last_danmaku_at"`
	LastSubsAt      int64  `json:"last_subtitles_at"`
	SourceID        int64  `json:"source_id"`
}

type Page struct {
	Bvid        string `json:"bvid"`
	Cid         int64  `json:"cid"`
	Page        int    `json:"page"`
	Part        string `json:"part"`
	Duration    int    `json:"duration"`
	FirstSeenAt int64  `json:"first_seen_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
}

type StatsSnapshot struct {
	Bvid     string `json:"bvid"`
	Ts       int64  `json:"ts"`
	View     int    `json:"view"`
	Danmaku  int    `json:"danmaku"`
	Reply    int    `json:"reply"`
	Favorite int    `json:"favorite"`
	Coin     int    `json:"coin"`
	Share    int    `json:"share"`
	Like     int    `json:"like"`
}

type Comment struct {
	Rpid        int64  `json:"rpid"`
	Bvid        string `json:"bvid"`
	ParentRpid  int64  `json:"parent_rpid"`
	RootRpid    int64  `json:"root_rpid"`
	MemberMid   int64  `json:"member_mid"`
	MemberName  string `json:"member_name"`
	MemberFace  string `json:"member_face"`
	MemberLevel int    `json:"member_level"` // B站等级 0-6（v6）
	Content     string `json:"content"`
	LikeCount   int    `json:"like_count"`
	ReplyCount  int    `json:"reply_count"`
	Ctime       int64  `json:"ctime"`
	Location    string `json:"location"`
	Status      string `json:"status"` // visible|deleted
	FirstSeenAt int64  `json:"first_seen_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
	DeletedAt   int64  `json:"deleted_at"`
	RawJSON     string `json:"raw_json"`
}

// Track 跟踪列表：源触发器发现新视频并自动下载的记录（v7）。
type Track struct {
	ID        int64  `json:"id"`
	Bvid      string `json:"bvid"`
	SourceID  int64  `json:"source_id"`
	State     string `json:"state"` // queued|done|failed
	Error     string `json:"error"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	// 以下为 LEFT JOIN videos 的展示字段
	Title       string `json:"title"`
	OwnerName   string `json:"owner_name"`
	OwnerMid    int64  `json:"owner_mid"`
	Downloaded  int    `json:"downloaded"`   // 视频文件是否已落库（0/1）
	SourceTitle string `json:"source_title"` // LEFT JOIN sources（ListTracksPage 填充）
}

// Face 去重后的评论者头像（图片本地化用，见 internal/images）。
type Face struct {
	Mid int64  `json:"mid"`
	URL string `json:"url"`
}

type Danmaku struct {
	Dmid        int64  `json:"dmid"`
	Bvid        string `json:"bvid"`
	Cid         int64  `json:"cid"`
	Timepoint   int    `json:"timepoint"` // ms
	Mode        int    `json:"mode"`
	Fontsize    int    `json:"fontsize"`
	Color       int64  `json:"color"`
	Content     string `json:"content"`
	Ctime       int64  `json:"ctime"`
	Status      string `json:"status"`
	FirstSeenAt int64  `json:"first_seen_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
	DeletedAt   int64  `json:"deleted_at"`
}

type Subtitle struct {
	ID        int64  `json:"id"`
	Bvid      string `json:"bvid"`
	Cid       int64  `json:"cid"`
	Lang      string `json:"lang"`
	LangDoc   string `json:"lang_doc"`
	Origin    string `json:"origin"` // bilibili | plugin:{id}
	Format    string `json:"format"` // bcc|srt|json
	FilePath  string `json:"file_path"`
	Generator string `json:"generator"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
}

type Summary struct {
	ID        int64  `json:"id"`
	Bvid      string `json:"bvid"`
	Origin    string `json:"origin"`
	Generator string `json:"generator"`
	Model     string `json:"model"`
	PromptVer string `json:"prompt_ver"`
	Lang      string `json:"lang"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

type Relation struct {
	ID        int64   `json:"id"`
	SrcType   string  `json:"src_type"`
	SrcID     string  `json:"src_id"`
	DstType   string  `json:"dst_type"`
	DstID     string  `json:"dst_id"`
	Relation  string  `json:"relation"`
	Weight    float64 `json:"weight"`
	Origin    string  `json:"origin"`
	CreatedAt int64   `json:"created_at"`
}

type Snapshot struct {
	ID         int64  `json:"id"`
	Endpoint   string `json:"endpoint"`
	ParamsJSON string `json:"params_json"`
	RespHash   string `json:"resp_hash"`
	BodyPath   string `json:"body_path"`
	StatusCode int    `json:"status_code"`
	FetchedAt  int64  `json:"fetched_at"`
}

type Job struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	PayloadJSON string  `json:"payload_json"`
	State       string  `json:"state"` // queued|running|done|failed
	Progress    float64 `json:"progress"`
	Detail      string  `json:"detail"`
	Error       string  `json:"error"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

type PluginRow struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	Enabled      bool   `json:"enabled"`
	ManifestJSON string `json:"manifest_json"`
	ConfigJSON   string `json:"config_json"`
}

type PluginRun struct {
	ID         int64  `json:"id"`
	PluginID   string `json:"plugin_id"`
	Event      string `json:"event"`
	Ok         bool   `json:"ok"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
	CreatedAt  int64  `json:"created_at"`
}
