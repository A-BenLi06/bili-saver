package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strconv"
)

const SchemaVersion = 18

type migration struct {
	version   int
	name, sql string
}

var migrations = []migration{{16, "archive observations and per-CID reconciliation", observationSchema}, {17, "portable content assets and page projections", assetSchema}, {18, "transactional runtime settings", settingsSchema}}

func (s *Store) schemaVersion() (int, error) {
	var exists bool
	if err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_meta')`).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var raw string
	err := s.DB.QueryRow(`SELECT v FROM schema_meta WHERE k='schema'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid archive schema version %q", raw)
	}
	return v, nil
}

func (s *Store) migrate() error {
	v, err := s.schemaVersion()
	if err != nil {
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("archive schema %d is newer than supported schema %d; use a newer application", v, SchemaVersion)
	}
	{
		if err := s.migrateLegacy(); err != nil {
			return err
		}
	}
	for _, m := range migrations {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		err = func() error {
			defer tx.Rollback()
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,checksum TEXT NOT NULL,applied_at INTEGER NOT NULL)`); err != nil {
				return err
			}
			var current int
			if err := tx.QueryRow(`SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&current); err != nil {
				return err
			}
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(m.sql)))
			if current >= m.version {
				var saved string
				err := tx.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=?`, m.version).Scan(&saved)
				if err != nil {
					return err
				}
				if saved != digest {
					return fmt.Errorf("migration %d checksum mismatch", m.version)
				}
				return tx.Commit()
			}
			if _, err := tx.Exec(m.sql); err != nil {
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
			if _, err := tx.Exec(`INSERT INTO schema_migrations VALUES(?,?,?,?) ON CONFLICT(version) DO NOTHING`, m.version, m.name, digest, Now()); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE schema_meta SET v=? WHERE k='schema'`, strconv.Itoa(m.version)); err != nil {
				return err
			}
			return tx.Commit()
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

const observationSchema = `
CREATE TABLE IF NOT EXISTS danmaku_sync_state (
 bvid TEXT NOT NULL,cid INTEGER NOT NULL,last_epoch INTEGER NOT NULL DEFAULT 0,
 anomalous INTEGER NOT NULL DEFAULT 0,missing_hash TEXT NOT NULL DEFAULT '',PRIMARY KEY(bvid,cid)
);
CREATE TABLE IF NOT EXISTS collection_status (
 bvid TEXT NOT NULL,cid INTEGER NOT NULL DEFAULT 0,component TEXT NOT NULL,
 attempted_at INTEGER NOT NULL,successful_at INTEGER NOT NULL DEFAULT 0,
 complete_at INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(bvid,cid,component)
);`
