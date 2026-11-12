package store

// deletePersonalUser merges duplicate admin sources before transferring ownership.
// Raw videos, comments and danmaku are never removed.
func (s *Store) deletePersonalUser(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE users SET enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT s.id,a.id FROM sources s JOIN sources a ON a.user_id=0 AND a.type=s.type AND a.ext_id=s.ext_id WHERE s.user_id=?`, id)
	if err != nil {
		return err
	}
	var pairs [][2]int64
	for rows.Next() {
		var pair [2]int64
		if err = rows.Scan(&pair[0], &pair[1]); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, pair)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		for _, stmt := range []string{
			`UPDATE videos SET source_id=? WHERE source_id=?`,
			`UPDATE video_tracks SET source_id=? WHERE source_id=?`,
			`UPDATE jobs SET payload_json=json_set(payload_json,'$.source_id',?) WHERE json_valid(payload_json) AND json_extract(payload_json,'$.source_id')=?`,
			`INSERT OR IGNORE INTO source_videos(source_id,bvid) SELECT ?,bvid FROM source_videos WHERE source_id=?`,
		} {
			if _, err = tx.Exec(stmt, p[1], p[0]); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`DELETE FROM sources WHERE id=?`, p[0]); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`UPDATE sources SET user_id=0 WHERE user_id=?`,
		`INSERT OR IGNORE INTO user_videos(user_id,bvid,created_at) SELECT 0,bvid,created_at FROM user_videos WHERE user_id=?`,
		`UPDATE user_download_usage SET state='released' WHERE state='reserved' AND user_id=?`,
		`UPDATE user_download_requests SET state='cancelled',updated_at=unixepoch() WHERE user_id=? AND state NOT IN ('done','cancelled')`,
		`DELETE FROM sessions WHERE user_id=?`,
		`DELETE FROM user_videos WHERE user_id=?`,
		`DELETE FROM users WHERE id=?`,
	} {
		if _, err = tx.Exec(stmt, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
