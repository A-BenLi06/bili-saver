package store

// migrateCommentFTS 重建可再生索引，让 FTS rowid 与原始 rpid 对齐。
// 历史索引的 rpid UNINDEXED 无法快速查缺；迁移与标记在同一事务，失败可重试。
func (s *Store) migrateCommentFTS() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ready int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_meta WHERE k='comments_fts_rowid' AND v='1')`).Scan(&ready); err != nil {
		return err
	}
	if ready != 0 {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM comments_fts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO comments_fts(rowid,rpid,content) SELECT rpid,rpid,content FROM comments`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_meta(k,v) VALUES('comments_fts_rowid','1') ON CONFLICT(k) DO UPDATE SET v='1'`); err != nil {
		return err
	}
	return tx.Commit()
}

// HealVideoCommentsFTS 只补齐当前视频，用 FTS rowid 主键检查是否已索引。
// 失败后下轮仍检查整部视频，因此部分分页已提交的评论也能自愈。
func (s *Store) HealVideoCommentsFTS(bvid string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO comments_fts(rowid,rpid,content)
		SELECT c.rpid,c.rpid,c.content FROM comments c WHERE c.bvid=?
		AND NOT EXISTS(SELECT 1 FROM comments_fts f WHERE f.rowid=c.rpid)`, bvid)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
