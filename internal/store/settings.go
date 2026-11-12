package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const settingsSchema = `CREATE TABLE IF NOT EXISTS runtime_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings_revision(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL);
INSERT OR IGNORE INTO settings_revision VALUES(1,0);`

var ErrSettingsUnavailable = errors.New("archive settings could not be persisted")
var ErrSettingsConflict = errors.New("settings changed; reload before saving")

func (s *Store) RuntimeSettings() (map[string]json.RawMessage, int64, error) {
	rows, err := s.DB.Query(`SELECT key,value FROM runtime_settings`)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out[k] = json.RawMessage(v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	// The first library is the legacy default; per-library rows own output settings.
	var layout, mode, root string
	err = s.DB.QueryRow(`SELECT layout,link_mode,root_path FROM media_libraries ORDER BY id LIMIT 1`).Scan(&layout, &mode, &root)
	if err == nil {
		for k, v := range map[string]string{"media.layout": layout, "media.link_mode": mode, "media.root": root} {
			body, _ := json.Marshal(v)
			out[k] = body
		}
	} else if err != sql.ErrNoRows {
		return nil, 0, err
	}
	var revision int64
	err = s.DB.QueryRow(`SELECT revision FROM settings_revision WHERE id=1`).Scan(&revision)
	return out, revision, err
}

// SaveRuntimeSettings commits global and default-library settings together.
func (s *Store) SaveRuntimeSettings(values map[string]json.RawMessage, expected *int64) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE settings_revision SET revision=revision WHERE id=1`); err != nil {
		return 0, err
	}
	var revision int64
	if err = tx.QueryRow(`SELECT revision FROM settings_revision WHERE id=1`).Scan(&revision); err != nil {
		return 0, err
	}
	if expected != nil && revision != *expected {
		return 0, ErrSettingsConflict
	}
	for key, raw := range values {
		column := ""
		switch key {
		case "media.layout":
			column = "layout"
		case "media.link_mode":
			column = "link_mode"
		}
		if column != "" {
			var value string
			if err = json.Unmarshal(raw, &value); err != nil {
				return 0, err
			}
			if _, err = tx.Exec(`UPDATE media_libraries SET `+column+`=? WHERE id=(SELECT MIN(id) FROM media_libraries)`, value); err != nil {
				return 0, err
			}
		}
		if _, err = tx.Exec(`INSERT INTO runtime_settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(raw)); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(`UPDATE settings_revision SET revision=revision+1 WHERE id=1`); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("save settings: %w", err)
	}
	return revision + 1, nil
}
