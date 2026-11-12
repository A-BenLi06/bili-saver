package store

import "context"

// StreamDanmakuExport 在同一读快照中计数和顺序遍历；只取 XML 需要的字段。
// 包含消失的弹幕，避免丢失历史。visit 的指针只在本次回调中有效。
func (s *Store) StreamDanmakuExport(ctx context.Context, cid int64, begin func(int) error, visit func(*Danmaku) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM danmaku WHERE cid=?`, cid).Scan(&count); err != nil {
		return err
	}
	if err := begin(count); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT dmid,timepoint,mode,fontsize,color,content,ctime FROM danmaku WHERE cid=? ORDER BY timepoint,dmid`, cid)
	if err != nil {
		return err
	}
	defer rows.Close()
	var d Danmaku
	for rows.Next() {
		if err := rows.Scan(&d.Dmid, &d.Timepoint, &d.Mode, &d.Fontsize, &d.Color, &d.Content, &d.Ctime); err != nil {
			return err
		}
		if err := visit(&d); err != nil {
			return err
		}
	}
	return rows.Err()
}
