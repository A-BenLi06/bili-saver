package collector

// 评论抓取：主楼翻页 + 楼中楼，逐条 upsert（含原始 JSON），配合消失检测。

import (
	"context"
	"encoding/json"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
)

// collectComments 抓取全部评论。返回 (本轮见到条数, 是否完整抓完, 错误)。
// 完整性决定上层是否执行消失检测——限量截断时不能标删。
func (c *Collector) collectComments(ctx context.Context, aid int64, bvid string, syncStart int64, isNew bool) (int, bool, error) {
	seen := 0
	max := c.Cfg.Collector.CommentsMax
	offset := ""
	complete := false

	save := func(r biliapi.Reply) error {
		location := ""
		if r.ReplyControl != nil {
			location = r.ReplyControl.Location
		}
		location = stringsTrimPrefix(location, "IP属地：")
		raw, _ := json.Marshal(r)
		return c.St.UpsertComment(&store.Comment{
			Rpid: int64(r.Rpid), Bvid: bvid,
			ParentRpid: 0, // main 接口里 root==0 为主楼；root>0 时 parent=root
			RootRpid:   int64(r.Root),
			MemberMid:  int64(r.Member.Mid), MemberName: r.Member.Uname, MemberFace: r.Member.Avatar,
			MemberLevel: r.Member.LevelInfo.CurrentLevel,
			Content:     r.Content.Message, LikeCount: r.Like, ReplyCount: r.Rcount,
			Ctime: r.Ctime, Location: location,
			FirstSeenAt: syncStart, LastSeenAt: syncStart, RawJSON: string(raw),
		})
	}

	for page := 0; page < 1000; page++ {
		main, err := c.API.ReplyMain(ctx, aid, offset)
		if err != nil {
			return seen, false, err
		}
		replies := main.Replies
		if page == 0 {
			replies = append(append([]biliapi.Reply{}, main.TopReplies...), replies...)
		}
		for _, r := range replies {
			if err := save(r); err != nil {
				return seen, false, err
			}
			seen++
			if r.Rcount > 0 {
				subSeen := 0
				for _, sr := range r.Replies {
					if err := save(sr); err != nil {
						return seen, false, err
					}
					seen++
					subSeen++
				}
				if remaining := r.Rcount - subSeen; remaining > 0 {
					if c.Cfg.Collector.SubRepliesMax > 0 && subSeen >= c.Cfg.Collector.SubRepliesMax {
						complete = false
					} else if ok := c.fetchSubReplies(ctx, aid, int64(r.Rpid), int64(r.Root), bvid, remaining, syncStart, syncStart, &seen); !ok {
						complete = false
					}
				}
			}
			if max > 0 && seen >= max {
				return seen, false, nil
			}
		}
		nextOffset := main.Cursor.PaginationReply.NextOffset
		if main.Cursor.IsEnd || nextOffset == "" {
			complete = true
			break
		}
		offset = nextOffset
	}
	// 未登录时 B 站评论接口只返回部分数据且伪报 is_end=true，
	// 缺失不代表消失：本轮不可作为消失对账依据（抓到的仍无损入库）。
	if complete && !c.API.HasCookie("SESSDATA") {
		c.Log.Warn("未登录：B站评论接口不完整，本轮跳过消失对账（登录后恢复检测）")
		complete = false
	}
	return seen, complete, nil
}

// fetchSubReplies 翻页抓取一条主楼的全部楼中楼，返回是否抓完整。
func (c *Collector) fetchSubReplies(ctx context.Context, aid, rpid, root int64, bvid string, remaining int, syncStart, now int64, seen *int) bool {
	const ps = 20
	maxSub := c.Cfg.Collector.SubRepliesMax
	fetched := 0
	for pn := 1; pn <= 1000; pn++ {
		d, err := c.API.ReplySub(ctx, aid, rpid, pn, ps)
		if err != nil {
			return false
		}
		for _, r := range d.Replies {
			location := ""
			if r.ReplyControl != nil {
				location = stringsTrimPrefix(r.ReplyControl.Location, "IP属地：")
			}
			raw, _ := json.Marshal(r)
			if err := c.St.UpsertComment(&store.Comment{
				Rpid: int64(r.Rpid), Bvid: bvid,
				ParentRpid: rpid, RootRpid: orDefault(int64(r.Root), rpid),
				MemberMid: int64(r.Member.Mid), MemberName: r.Member.Uname, MemberFace: r.Member.Avatar,
				MemberLevel: r.Member.LevelInfo.CurrentLevel,
				Content:     r.Content.Message, LikeCount: r.Like, ReplyCount: r.Rcount,
				Ctime: r.Ctime, Location: location,
				FirstSeenAt: syncStart, LastSeenAt: syncStart, RawJSON: string(raw),
			}); err != nil {
				return false
			}
			*seen = *seen + 1
			fetched++
			if maxSub > 0 && fetched >= maxSub {
				return false
			}
		}
		if len(d.Replies) < ps || fetched >= remaining {
			return true
		}
	}
	return true
}

func orDefault(v, def int64) int64 {
	if v != 0 {
		return v
	}
	return def
}

func stringsTrimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
