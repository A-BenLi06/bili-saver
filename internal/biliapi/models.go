package biliapi

import (
	"encoding/json"
	"strconv"
	"strings"
)

// B 站接口响应结构（只声明会消费的字段）。

// Dimension 视频分辨率元数据（dimension），width<height 即竖屏。
type Dimension struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Rotate int `json:"rotate"` // 90/270 时宽高语义互换
}

type VideoView struct {
	Bvid       string    `json:"bvid"`
	Aid        int64     `json:"aid"`
	Videos     int       `json:"videos"`
	TID        int       `json:"tid"`
	TName      string    `json:"tname"`
	TNameV2    string    `json:"tname_v2"` // 个别视频 tname 为空串时的兜底
	Dimension  Dimension `json:"dimension"`
	Pic        string    `json:"pic"`
	Title      string    `json:"title"`
	Pubdate    int64     `json:"pubdate"`
	Desc       string    `json:"desc"`
	Duration   int       `json:"duration"`
	OwnerOwner struct {
		Mid  int64  `json:"mid"`
		Name string `json:"name"`
		Face string `json:"face"`
	} `json:"owner"`
	StatStat struct {
		View     int `json:"view"`
		Danmaku  int `json:"danmaku"`
		Reply    int `json:"reply"`
		Favorite int `json:"favorite"`
		Coin     int `json:"coin"`
		Share    int `json:"share"`
		Like     int `json:"like"`
	} `json:"stat"`
	CID   int64      `json:"cid"`
	Pages []PageItem `json:"pages"`
}

// PageItem 单个分P。
type PageItem struct {
	Cid      int64  `json:"cid"`
	Page     int    `json:"page"`
	Part     string `json:"part"`
	Duration int    `json:"duration"`
}

type TagItem struct {
	ID    int64  `json:"id"`
	TagID int64  `json:"tag_id"` // 兼容旧字段
	Name  string `json:"name"`
	Name2 string `json:"tag_name"` // 兼容旧字段
}

func (t TagItem) IDOr() int64 {
	if t.ID != 0 {
		return t.ID
	}
	return t.TagID
}
func (t TagItem) NameOr() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Name2
}

type DashStream struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	BackupURL []string `json:"backupUrl"`
	Bandwidth int64    `json:"bandwidth"`
	MimeType  string   `json:"mimeType"`
	Codecs    string   `json:"codecs"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate string   `json:"frameRate"`
	Codecid   int      `json:"codecid"`
}

type PlayURL struct {
	Quality       int   `json:"quality"`
	AcceptQuality []int `json:"accept_quality"`
	DashDash      *Dash `json:"dash"`
	DurlDurl      []struct {
		URL  string `json:"url"`
		Size int64  `json:"size"`
	} `json:"durl"`
}

type Dash struct {
	Duration int          `json:"duration"`
	Video    []DashStream `json:"video"`
	Audio    []DashStream `json:"audio"`
	// 杜比全景声与 Hi-Res 无损音轨不在 audio 数组里，是独立字段（需 fnval|256 才返回）。
	Dolby *DolbyAudio `json:"dolby"`
	FLAC  *DolbyAudio `json:"flac"`
}

// DolbyAudio 复用于 dolby/flac 两种包装：{"audio":[...]}。
type DolbyAudio struct {
	Audio []DashStream `json:"audio"`
}

// SpaceAccInfo 用户空间资料（等级用于 teriteri 前端徽章）。
// LevelInfo 用指针：登录态失效/软风控时 B 站返回 code=0 但剥离该字段，须与真 Lv0 区分。
type SpaceAccInfo struct {
	Mid       int    `json:"mid"`
	Name      string `json:"name"`
	LevelInfo *struct {
		CurrentLevel int `json:"current_level"`
	} `json:"level_info"`
}

type SubtitleItem struct {
	ID          int64  `json:"id"`
	Lan         string `json:"lan"`
	LanDoc      string `json:"lan_doc"`
	SubtitleURL string `json:"subtitle_url"`
}

type PlayerInfo struct {
	Subtitle struct {
		Subtitles []SubtitleItem `json:"subtitles"`
	} `json:"subtitle"`
}

type Conclusion struct {
	ModelList map[string]struct {
		Name string `json:"name"`
	} `json:"model_list"`
	ModelResult struct {
		Summary string `json:"summary"`
		Outline []struct {
			Title       string `json:"title"`
			PartOutline []struct {
				Timestamp int64  `json:"timestamp"`
				Content   string `json:"content"`
			} `json:"part_outline"`
		} `json:"outline"`
	} `json:"model_result"`
}

// FlexInt 兼容 B 站接口里数字字段有时返回字符串的情况。
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	b = trimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.Trim(s, " \t")
		if s == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// 可能是浮点字符串
			fv, ferr := strconv.ParseFloat(s, 64)
			if ferr != nil {
				return err
			}
			*f = FlexInt(fv)
			return nil
		}
		*f = FlexInt(v)
		return nil
	}
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexInt(v)
	return nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

type Reply struct {
	Rpid    FlexInt `json:"rpid"`
	Root    FlexInt `json:"root"`
	Oid     FlexInt `json:"oid"`
	Mid     FlexInt `json:"mid"`
	Like    int     `json:"like"`
	Rcount  int     `json:"rcount"`
	Ctime   int64   `json:"ctime"`
	Content struct {
		Message string `json:"message"`
	} `json:"content"`
	Member struct {
		Mid    FlexInt `json:"mid"`
		Uname  string  `json:"uname"`
		Avatar string  `json:"avatar"`
		// LevelInfo 当前等级（0-6），teriteri 前端按经验值映射渲染等级徽章
		LevelInfo struct {
			CurrentLevel int `json:"current_level"`
		} `json:"level_info"`
	} `json:"member"`
	ReplyControl *struct {
		Location string `json:"location"`
	} `json:"reply_control"`
	// Replies 是接口内嵌的楼中楼预览（前几条）。
	Replies []Reply `json:"replies"`
}

type ReplyMainData struct {
	Cursor struct {
		IsBegin         bool  `json:"is_begin"`
		IsEnd           bool  `json:"is_end"`
		Next            int64 `json:"next"`
		PaginationReply struct {
			NextOffset string `json:"next_offset"`
		} `json:"pagination_reply"`
	} `json:"cursor"`
	Replies    []Reply `json:"replies"`
	TopReplies []Reply `json:"top_replies"`
}

type ReplySubData struct {
	Page struct {
		Count int `json:"count"`
		Size  int `json:"size"`
		Num   int `json:"num"`
	} `json:"page"`
	Replies []Reply `json:"replies"`
}

type FavMedia struct {
	ID       int64  `json:"id"`
	Bvid     string `json:"bvid"`
	Title    string `json:"title"`
	Cover    string `json:"cover"`
	Duration int    `json:"duration"`
	Type     int    `json:"type"` // 2 = 视频
}

type FavListData struct {
	Info struct {
		Title string `json:"title"`
		ID    int64  `json:"id"`
		Mid   int64  `json:"mid"`
	} `json:"info"`
	Medias  []FavMedia `json:"medias"`
	HasMore bool       `json:"has_more"`
}

type SpaceVideo struct {
	Aid     int64  `json:"aid"`
	Bvid    string `json:"bvid"`
	Title   string `json:"title"`
	Created int64  `json:"created"`
	Length  string `json:"length"`
	Pic     string `json:"pic"`
}

type SpaceArcData struct {
	List struct {
		Vlist []SpaceVideo `json:"vlist"`
	} `json:"list"`
	Page struct {
		Count int `json:"count"`
		PN    int `json:"pn"`
		PS    int `json:"ps"`
	} `json:"page"`
}

type SeasonArchive struct {
	Aid      int64  `json:"aid"`
	Bvid     string `json:"bvid"`
	Title    string `json:"title"`
	Pic      string `json:"pic"`
	Duration int    `json:"duration"`
}

type SeasonArcData struct {
	Meta struct {
		Name  string `json:"name"`
		Total int    `json:"total"`
	} `json:"meta"`
	Archives []SeasonArchive `json:"archives"`
}

type QRGenerate struct {
	URL       string `json:"url"`
	QRCodeKey string `json:"qrcode_key"`
}

type QRPoll struct {
	URL          string `json:"url"`
	RefreshToken string `json:"refresh_token"`
	Timestamp    int64  `json:"timestamp"`
	Code         int    `json:"code"`
	Message      string `json:"message"`
}

// DynFeedItem 动态 feed 条目（只声明消费的字段；完整原始 JSON 单独保留，
// 未识别的新字段不会丢——raw 快照 + SpacePost.Raw 双保险）。
type DynFeedItem struct {
	ID      string `json:"id_str"`
	Raw     string `json:"-"`
	Type    string `json:"type"` // DYNAMIC_TYPE_AV / WORD / DRAW / FORWARD / ARTICLE ...
	Modules struct {
		ModuleAuthor struct {
			Mid   int64 `json:"mid"`
			PubTs int64 `json:"pub_ts"`
		} `json:"module_author"`
		ModuleDynamic struct {
			Content struct {
				Text        string `json:"text"`
				Description string `json:"description"`
			} `json:"content"`
			Major struct {
				Type    string `json:"type"` // MAJOR_TYPE_ARCHIVE / ARTICLE / DRAW / OPUS / NONE
				Archive *struct {
					Bvid  string `json:"bvid"`
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"archive"`
				Article *struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"article"`
				Draw *struct {
					Title string `json:"title"`
					Items []struct {
						Src string `json:"src"`
					} `json:"items"`
				} `json:"draw"`
				Opus *struct {
					Title   string `json:"title"`
					Content json.RawMessage `json:"content"`
					Images  []struct {
						Url string `json:"url"`
					} `json:"images"`
				} `json:"opus"`
			} `json:"major"`
		} `json:"module_dynamic"`
	} `json:"modules"`
}

// DynFeed 动态 feed 分页数据。
type DynFeed struct {
	Items      []DynFeedItem `json:"items"`
	HasMore    bool          `json:"has_more"`
	NextOffset string        `json:"offset"`
}
