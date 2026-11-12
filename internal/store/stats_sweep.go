package store

import "context"

// StatsCandidate 是全库统计冷却判断所需的窄投影，不装载描述、封面与其他归档列。
type StatsCandidate struct {
	RowID       int64
	Bvid        string
	Pubdate     int64
	LastStatsAt int64
}

func (s *Store) StatsSweepWindow(ctx context.Context) (count int, lastRowID int64, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(rowid),0) FROM videos`).Scan(&count, &lastRowID)
	return
}

// StatsSweepPage 用不可变 rowid 倒序游标，新归档优先，避免 OFFSET 随页数增长。
// 上界固定于本轮开始，期间新增视频留给下一轮；页间不持有长事务/连接。
func (s *Store) StatsSweepPage(ctx context.Context, before int64, limit int) ([]StatsCandidate, error) {
	if limit <= 0 {
		limit = syncQueryBatch
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT rowid,bvid,pubdate,last_stats_at FROM videos WHERE rowid<=? ORDER BY rowid DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StatsCandidate, 0, limit)
	for rows.Next() {
		var v StatsCandidate
		if err := rows.Scan(&v.RowID, &v.Bvid, &v.Pubdate, &v.LastStatsAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
