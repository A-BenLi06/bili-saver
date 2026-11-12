package store

import (
	"fmt"
	"strings"
)

// migratePersonal preserves source IDs because queued jobs and origin records refer to them.
func (s *Store) migratePersonal() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ddl string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='sources'`).Scan(&ddl); err != nil {
		return err
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(strings.ToLower(ddl))
	if strings.Contains(compact, "unique(type,ext_id)") {
		var sequence int64
		if err = tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='sources'),0)`).Scan(&sequence); err != nil {
			return err
		}
		start := strings.Index(ddl, "(")
		body := ddl[start:]
		// Only replace the known old constraint; never guess at an unfamiliar schema.
		body = strings.ReplaceAll(body, "UNIQUE(type, ext_id)", "UNIQUE(user_id, type, ext_id)")
		body = strings.ReplaceAll(body, "UNIQUE(type,ext_id)", "UNIQUE(user_id, type, ext_id)")
		if body == ddl[start:] {
			return fmt.Errorf("无法识别监控源唯一约束，升级已回滚")
		}
		if _, err = tx.Exec(`CREATE TABLE sources_v14 ` + body); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO sources_v14 SELECT * FROM sources`); err != nil {
			return err
		}
		if _, err = tx.Exec(`DROP TABLE sources`); err != nil {
			return err
		}
		if _, err = tx.Exec(`ALTER TABLE sources_v14 RENAME TO sources`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='sources'`, sequence); err != nil {
			return err
		}
	}
	var migrated int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM schema_meta WHERE k='personal_v14'`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 0 {
		for _, stmt := range []string{
			`UPDATE user_tiers SET max_daily_downloads=max_daily_adds`,
			`INSERT OR IGNORE INTO user_videos(user_id,bvid,created_at) SELECT s.user_id,v.bvid,v.first_archived_at FROM videos v JOIN sources s ON s.id=v.source_id`,
			`INSERT OR IGNORE INTO user_videos(user_id,bvid,created_at) SELECT s.user_id,v.bvid,v.first_archived_at FROM sources s JOIN videos v ON s.type='video' AND s.ext_id=v.bvid`,
			`INSERT OR IGNORE INTO source_videos(source_id,bvid) SELECT s.id,v.bvid FROM videos v JOIN sources s ON s.id=v.source_id`,
			`INSERT OR IGNORE INTO source_videos(source_id,bvid) SELECT s.id,v.bvid FROM sources s JOIN videos v ON s.type='video' AND s.ext_id=v.bvid`,
			`INSERT INTO schema_meta(k,v) VALUES('personal_v14','1')`,
		} {
			if _, err = tx.Exec(stmt); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sources_user ON sources(user_id,created_at)`); err != nil {
		return err
	}
	return tx.Commit()
}
