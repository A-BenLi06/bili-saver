// Package config 加载并合并默认值 / YAML / 环境变量三层配置。
// 环境变量规则：BILI_SAVER_ + 大写字段路径（下划线分隔），如 BILI_SAVER_DOWNLOAD_QUALITY。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DataDir string `yaml:"data_dir"`
	Listen  string `yaml:"listen"`

	Log struct {
		Level string `yaml:"level"` // debug|info|warn|error
		File  string `yaml:"file"`  // 可选：同时写文件
	} `yaml:"log"`

	FFmpeg struct {
		Path string `yaml:"path"` // 留空则从 PATH 查找
	} `yaml:"ffmpeg"`

	Bilibili struct {
		CookieFile string `yaml:"cookie_file"` // 默认 <data_dir>/cookies.json
		MinReqMs   int    `yaml:"min_req_ms"`  // 全局请求最小间隔（限流），默认 350
		UserAgent  string `yaml:"user_agent"`
	} `yaml:"bilibili"`

	Download struct {
		// Qualities 清晰度白名单（4k/1080p60/1080p-high/1080p/720p60/720p/480p/360p）：
		// 空=全部画质（默认，取当前账号可用的最高档）；指定若干档时取其中可用的最高档，
		// 全部不可用则回落到更低档。非空时忽略 Quality。
		Qualities []string `yaml:"qualities"`
		// Quality 旧版单档位（上限语义：不超过该档）。留空=跟随 Qualities。
		Quality     string `yaml:"quality"`
		Concurrency int    `yaml:"concurrency"`
		Segments    int    `yaml:"segments"` // 单流分段并行连接数（Range 分块），1=单线程
		AllowAV1    bool   `yaml:"allow_av1"`
		Allow4K     bool   `yaml:"allow_4k"` // false 时即使 Qualities 含 4k 也剔除
		// AudioQualities 音频质量偏好（hires/dolby/192k/132k/64k）：
		// 空=标准三档最高可用（192k>132k>64k，不取空间音频）；指定时集合优先、标准链兜底。
		AudioQualities  []string `yaml:"audio_qualities"`
		DownloadDanmaku bool     `yaml:"download_danmaku"`
		AutoDownload    bool     `yaml:"auto_download"` // 归档后自动下载视频文件
	} `yaml:"download"`

	Collector struct {
		CommentsMax        int  `yaml:"comments_max"`         // 单视频主楼评论抓取上限（含翻页），0=不限
		SubRepliesMax      int  `yaml:"sub_replies_max"`      // 单条主楼的楼中楼上限，0=不限
		FullResyncComments bool `yaml:"full_resync_comments"` // 重同步时全量翻页以检测删除
		SyncDefaultMinutes int  `yaml:"sync_default_minutes"` // 监控源默认重同步间隔
		StatsResyncHours   int  `yaml:"stats_resync_hours"`   // 全库统计重同步间隔（小时），0=禁用
		// AdaptiveSync 自适应补档频率：按视频新发程度（<24h 加速、≥30d 冷却）与
		// 评论/播放活跃度（统计快照增速）伸缩检测间隔；false=固定间隔旧行为。
		AdaptiveSync bool `yaml:"adaptive_sync"`
		// 热门视频二次确认阈值（仅作用于管理端手动补档路径，自动化不打扰）。
		HotVideoViews   int `yaml:"hot_video_views"`   // 播放量阈值，默认 1000000；0=禁用提示
		HotVideoReplies int `yaml:"hot_video_replies"` // 评论数阈值，默认 10000；0=禁用提示
	} `yaml:"collector"`

	Media struct {
		Enabled   bool   `yaml:"enabled"`
		Root      string `yaml:"root"`       // 媒体库输出根，默认 <data_dir>/library
		Layout    string `yaml:"layout"`     // movie | show | collection
		LinkMode  string `yaml:"link_mode"`  // auto | symlink | hardlink | copy | strm
		PublicURL string `yaml:"public_url"` // strm 模式的核心访问地址，默认 http://127.0.0.1<listen>
	} `yaml:"media"`

	Teriteri struct {
		Enabled     bool   `yaml:"enabled"`
		AdminUser   string `yaml:"admin_user"`
		AdminPass   string `yaml:"admin_pass"`
		JWTSecret   string `yaml:"jwt_secret"`
		FrontendURL string `yaml:"frontend_url"` // teriteri-client 部署地址（观看入口），留空显示部署提示
		// 多端站点托管：web_root 下 client/dist（用户端）与 admin/dist（管理员端）
		// 由核心直接托管（进程内反代 API/媒体/WS），可用管理页一键安装构建。
		WebRoot    string `yaml:"web_root"`    // 默认 <data_dir>/teriteri
		ClientAddr string `yaml:"client_addr"` // 用户端站点监听地址，默认 :8081；留空禁用
		AdminAddr  string `yaml:"admin_addr"`  // 管理员端站点监听地址，默认 :8082；留空禁用
	} `yaml:"teriteri"`

	Admin struct {
		Token string `yaml:"token"` // 非空则管理 API 要求 Authorization: Bearer <token>
	} `yaml:"admin"`

	Plugins struct {
		Dir     string `yaml:"dir"` // 默认 <data_dir>/plugins
		Enabled bool   `yaml:"enabled"`
	} `yaml:"plugins"`
}

// Defaults 返回内置默认值。
func Defaults() *Config {
	var c Config
	c.DataDir = "./data"
	c.Listen = ":8080"
	c.Log.Level = "info"
	c.Bilibili.CookieFile = ""
	c.Bilibili.MinReqMs = 350
	c.Bilibili.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36"
	c.Download.Quality = "" // 默认全部画质=最高可用（旧值为 "1080p"）
	c.Download.Concurrency = 2
	c.Download.Segments = 4
	c.Download.DownloadDanmaku = true
	c.Download.AutoDownload = true
	c.Collector.CommentsMax = 0
	c.Collector.SubRepliesMax = 0
	c.Collector.FullResyncComments = true
	c.Collector.SyncDefaultMinutes = 360
	c.Collector.HotVideoViews = 1000000
	c.Collector.HotVideoReplies = 10000
	c.Collector.StatsResyncHours = 24
	c.Collector.AdaptiveSync = true
	c.Media.Enabled = true
	c.Media.Layout = "movie"
	c.Media.LinkMode = "auto"
	c.Teriteri.Enabled = true
	c.Teriteri.AdminUser = "admin"
	c.Teriteri.AdminPass = "bili-saver"
	c.Teriteri.ClientAddr = ":8081"
	c.Teriteri.AdminAddr = ":8082"
	c.Plugins.Enabled = true
	return &c
}

// Load 依次应用默认值 → YAML 文件（可不存在）→ 环境变量，然后派生路径并校验。
func Load(path string) (*Config, error) {
	c := Defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	applyEnv(c, "BILI_SAVER", reflect.ValueOf(c).Elem(), "")
	c.Normalize()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// SaveConfigFile 把配置写回 YAML 文件；原文件备份为 <path>.bak（存在时）。
// 注意：Marshal 会丢失原文件注释，属已知取舍（备份里仍有原文）。
// 权限 0600：配置可能含 admin token / JWT secret 等敏感项。
func SaveConfigFile(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	if old, rerr := os.ReadFile(path); rerr == nil {
		if werr := os.WriteFile(path+".bak", old, 0o600); werr != nil {
			return fmt.Errorf("备份原配置失败: %w", werr)
		}
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("写入配置 %s: %w", path, err)
	}
	return nil
}

// Normalize 派生默认路径（Load 与测试共用）。
func (c *Config) Normalize() {
	c.DataDir = filepath.Clean(c.DataDir)
	if c.Bilibili.CookieFile == "" {
		c.Bilibili.CookieFile = filepath.Join(c.DataDir, "cookies.json")
	}
	if c.Media.Root == "" {
		c.Media.Root = filepath.Join(c.DataDir, "library")
	}
	if c.Plugins.Dir == "" {
		c.Plugins.Dir = filepath.Join(c.DataDir, "plugins")
	}
	if c.Teriteri.WebRoot == "" {
		c.Teriteri.WebRoot = filepath.Join(c.DataDir, "teriteri")
	}
	if c.Collector.SyncDefaultMinutes <= 0 {
		c.Collector.SyncDefaultMinutes = 360
	}
}

// Validate 做跨字段校验与派生。
func (c *Config) Validate() error {
	if c.Download.Concurrency < 1 {
		c.Download.Concurrency = 1
	}
	if c.Download.Segments < 1 {
		c.Download.Segments = 1
	}
	switch c.Media.Layout {
	case "movie", "show", "collection":
	default:
		return fmt.Errorf("media.layout 必须是 movie/show/collection，当前 %q", c.Media.Layout)
	}
	switch c.Media.LinkMode {
	case "auto", "symlink", "hardlink", "copy", "strm":
	default:
		return fmt.Errorf("media.link_mode 必须是 auto/symlink/hardlink/copy/strm，当前 %q", c.Media.LinkMode)
	}
	return nil
}

// FFmpegPath 返回 ffmpeg 可执行路径（配置优先，其次 PATH 常见名）。
func (c *Config) FFmpegPath() string {
	if c.FFmpeg.Path != "" {
		return c.FFmpeg.Path
	}
	return "ffmpeg"
}

// CookiePath 返回 cookie 文件绝对位置。
func (c *Config) CookiePath() string { return c.Bilibili.CookieFile }

// applyEnv 递归地把 BILI_SAVER_<PATH> 环境变量覆盖到结构体字段上。
func applyEnv(c *Config, prefix string, v reflect.Value, path string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		key := f.Tag.Get("yaml")
		if key == "" {
			key = strings.ToLower(f.Name)
		}
		key = strings.SplitN(key, ",", 2)[0]
		sub := key
		if path != "" {
			sub = path + "_" + key
		}
		switch fv.Kind() {
		case reflect.Struct:
			applyEnv(c, prefix, fv, sub)
		case reflect.String, reflect.Bool, reflect.Int:
			env := prefix + "_" + strings.ToUpper(sub)
			if val, ok := os.LookupEnv(env); ok {
				switch fv.Kind() {
				case reflect.String:
					fv.SetString(val)
				case reflect.Bool:
					b, err := strconv.ParseBool(val)
					if err == nil {
						fv.SetBool(b)
					}
				case reflect.Int:
					n, err := strconv.Atoi(val)
					if err == nil {
						fv.SetInt(int64(n))
					}
				}
			}
		}
	}
}
