package store

import (
	"fmt"
	"sort"
	"strings"
)

type CommentSyncAdvice struct {
	Days              int     `json:"days"`
	Minutes           int     `json:"minutes"`
	Comments          int     `json:"comments"`
	ObservedDays      float64 `json:"observed_days"`
	NewCommentsPerDay float64 `json:"new_comments_per_day"`
	Reason            string  `json:"reason"`
}

// SuggestCommentSync uses observed growth, not publication age alone. It is
// advisory only: no source setting or reconciliation policy is changed.
func SuggestCommentSync(v *Video, stats []*StatsSnapshot, now int64) *CommentSyncAdvice {
	if v == nil || v.Status != "ok" || v.Pubdate <= 0 || now-v.Pubdate < 30*86400 || len(stats) < 2 {
		return nil
	}
	samples := append([]*StatsSnapshot(nil), stats...)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].Ts < samples[j].Ts })
	last := samples[len(samples)-1]
	if last.Ts > now || now-last.Ts > 3*86400 || last.Reply < 10000 {
		return nil
	}
	var first *StatsSnapshot
	for _, s := range samples {
		if s.Ts >= last.Ts-31*86400 && s.Ts < last.Ts {
			first = s
			break
		}
	}
	if first == nil || last.Ts-first.Ts < 3*86400 || last.Reply < first.Reply || last.View < first.View {
		return nil
	}
	days := float64(last.Ts-first.Ts) / 86400
	replies := float64(last.Reply-first.Reply) / days
	views := float64(last.View-first.View) / (days * 24)
	if replies >= 2 || views >= 5 {
		return nil
	}
	prev := samples[len(samples)-2]
	if gap := last.Ts - prev.Ts; gap >= 60 {
		if last.Reply < prev.Reply || last.View < prev.View || float64(last.Reply-prev.Reply)*86400/float64(gap) >= 2 || float64(last.View-prev.View)*3600/float64(gap) >= 5 {
			return nil
		}
	}
	interval := 7
	if last.Reply >= 50000 && days >= 7 {
		interval = 14
	}
	if last.Reply >= 100000 && days >= 14 && replies <= 0.2 && views < 1 {
		interval = 30
	}
	return &CommentSyncAdvice{Days: interval, Minutes: interval * 1440, Comments: last.Reply, ObservedDays: days, NewCommentsPerDay: replies,
		Reason: fmt.Sprintf("评论 %d 条，近 %.0f 天日均新增 %.1f 条；建议将评论同步间隔延长至 %d 天", last.Reply, days, replies, interval)}
}

// CommentAdviceStats selects latest two observations and 3/7/14/30-day anchors
// with indexed probes. At most six rows per ID, one SQL per <=400 IDs; neither
// per-video queries nor a full history/window sort is introduced into lists.
func (s *Store) CommentAdviceStats(ids []string, now int64) (map[string][]*StatsSnapshot, error) {
	out := make(map[string][]*StatsSnapshot, len(ids))
	err := eachSyncBatch(ids, func(_ string, args []any) error {
		values := strings.TrimSuffix(strings.Repeat("(?),", len(args)), ",")
		branches := []string{
			`SELECT w.bvid,(SELECT id FROM video_stats_history WHERE bvid=w.bvid ORDER BY ts DESC,id DESC LIMIT 1) id FROM wanted w`,
			`SELECT w.bvid,(SELECT id FROM video_stats_history WHERE bvid=w.bvid ORDER BY ts DESC,id DESC LIMIT 1 OFFSET 1) id FROM wanted w`,
		}
		for _, days := range []int64{3, 7, 14, 30} {
			branches = append(branches, `SELECT w.bvid,(SELECT id FROM video_stats_history WHERE bvid=w.bvid AND ts<=? ORDER BY ts DESC,id DESC LIMIT 1) id FROM wanted w`)
			args = append(args, now-days*86400)
		}
		rows, err := s.DB.Query(`WITH wanted(bvid) AS (VALUES `+values+`), chosen AS (`+strings.Join(branches, " UNION ALL ")+`) SELECT DISTINCT h.bvid,h.ts,h.view,h.reply,h.id FROM chosen c JOIN video_stats_history h ON h.id=c.id ORDER BY h.bvid,h.ts,h.id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h StatsSnapshot
			var id int64
			if err := rows.Scan(&h.Bvid, &h.Ts, &h.View, &h.Reply, &id); err != nil {
				return err
			}
			out[h.Bvid] = append(out[h.Bvid], &h)
		}
		return rows.Err()
	})
	return out, err
}
