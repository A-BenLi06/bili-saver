package store

import "strings"

// UpdateStatsMetadata 轻量刷新只更新实际变化的列，不读取/重写完整视频元数据参数。
// 时间戳单调，空分区/尺寸结果不抹掉可信旧值；不存在时保留 ErrNotFound 契约。
func (s *Store) UpdateStatsMetadata(bvid string, ts, tid int64, tname, orientation string) error {
	res, err := s.DB.Exec(`UPDATE videos SET status='ok',last_sync_at=MAX(last_sync_at,?),last_stats_at=MAX(last_stats_at,?),
		tid=CASE WHEN ?<>0 THEN ? ELSE tid END,
		tname=CASE WHEN ?<>'' THEN ? ELSE tname END,
		orientation=CASE WHEN ?<>'' THEN ? ELSE orientation END WHERE bvid=?`,
		ts, ts, tid, tid, tname, tname, orientation, orientation, bvid)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// 每块限制参数数量和短时内存；查询仅覆盖当前源，不加载整个归档库。
const syncQueryBatch = 400

func eachSyncBatch(ids []string, query func(string, []any) error) error {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	ids = unique
	for start := 0; start < len(ids); start += syncQueryBatch {
		end := start + syncQueryBatch
		if end > len(ids) {
			end = len(ids)
		}
		args := make([]any, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
		}
		if err := query(strings.TrimSuffix(strings.Repeat("?,", len(args)), ","), args); err != nil {
			return err
		}
	}
	return nil
}

// VideosByBvids 批量读取源视频；不存在的 ID 不返回，数据库错误正常传播。
func (s *Store) VideosByBvids(ids []string) (map[string]*Video, error) {
	out := make(map[string]*Video, len(ids))
	err := eachSyncBatch(ids, func(ph string, args []any) error {
		rows, err := s.DB.Query(`SELECT `+videoCols+` FROM videos WHERE bvid IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVideo(rows)
			if err != nil {
				return err
			}
			out[v.Bvid] = v
		}
		return rows.Err()
	})
	return out, err
}

// RecentStatsBatch 每个 ID 只通过 idx_stats 定位最近两行，不扫描/排序全部历史。
// 结果与 RecentStats(id,2) 一致，按时间升序排列，供一轮源调度复用。
func (s *Store) RecentStatsBatch(ids []string) (map[string][]*StatsSnapshot, error) {
	out := make(map[string][]*StatsSnapshot, len(ids))
	err := eachSyncBatch(ids, func(_ string, args []any) error {
		values := strings.TrimSuffix(strings.Repeat("(?),", len(args)), ",")
		rows, err := s.DB.Query(`WITH wanted(bvid) AS (VALUES `+values+`)
			SELECT h.bvid,h.ts,h.view,h.danmaku,h.reply,h.favorite,h.coin,h.share,h.like
			FROM wanted w JOIN video_stats_history h ON h.id IN
			(SELECT id FROM video_stats_history WHERE bvid=w.bvid ORDER BY ts DESC LIMIT 2)
			ORDER BY h.bvid,h.ts ASC,h.id ASC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t StatsSnapshot
			if err := rows.Scan(&t.Bvid, &t.Ts, &t.View, &t.Danmaku, &t.Reply, &t.Favorite, &t.Coin, &t.Share, &t.Like); err != nil {
				return err
			}
			out[t.Bvid] = append(out[t.Bvid], &t)
		}
		return rows.Err()
	})
	return out, err
}
