package archiveops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"bili-saver/internal/archivepath"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func relocatePaths(ctx context.Context, stage, oldRoot, destination string) error {
	uri, err := store.DatabaseURL(filepath.Join(stage, "bili-saver.db"))
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var version int
	if err = db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&version); err != nil {
		return err
	}
	if version > store.SchemaVersion {
		return fmt.Errorf("backup requires newer schema support")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range []struct{ table, id, column string }{{"videos", "bvid", "local_path"}, {"subtitles", "id", "file_path"}} {
		rows, e := tx.QueryContext(ctx, `SELECT `+item.id+`,`+item.column+` FROM `+item.table+` WHERE `+item.column+`<>''`)
		if e != nil {
			return e
		}
		type change struct {
			id  any
			key string
		}
		var changes []change
		var id any
		var raw string
		for rows.Next() {
			if e = rows.Scan(&id, &raw); e != nil {
				rows.Close()
				return e
			}
			key, e := archivepath.Key(oldRoot, raw)
			if e != nil {
				rows.Close()
				return e
			}
			if key != raw {
				changes = append(changes, change{id, key})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		update := `UPDATE ` + item.table + ` SET ` + item.column + `=? WHERE ` + item.id + `=?`
		for _, c := range changes {
			if _, e = tx.ExecContext(ctx, update, c.key, c.id); e != nil {
				return e
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE snapshots SET body_path=replace(body_path,char(92),'/') WHERE instr(body_path,char(92))>0`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM media_libraries ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		root := filepath.Join(destination, "library", strconv.FormatInt(id, 10))
		if _, err = tx.ExecContext(ctx, `UPDATE media_libraries SET root_path=? WHERE id=?`, root, id); err != nil {
			return err
		}
	}
	if hasTable(db, "storage_roots") {
		if _, err = tx.ExecContext(ctx, `INSERT INTO storage_roots VALUES('archive',?) ON CONFLICT(id) DO UPDATE SET path=excluded.path`, destination); err != nil {
			return err
		}
	}
	if hasTable(db, "runtime_settings") {
		for _, key := range []string{"media.root", "plugins.dir"} {
			if _, err = tx.ExecContext(ctx, `DELETE FROM runtime_settings WHERE key=?`, key); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Node paths are intentionally regenerated; persisted runtime preferences remain in SQLite.
	cfg := config.Defaults()
	if _, e := os.Stat(filepath.Join(stage, "startup-config.yaml")); e == nil {
		cfg, err = config.LoadSnapshot(filepath.Join(stage, "startup-config.yaml"))
		if err != nil {
			return err
		}
	} else if os.IsNotExist(e) {
		cfg.Download.AutoDownload = false
		cfg.Media.Enabled = false
	} else {
		return e
	}
	cfg.DataDir = destination
	cfg.Media.Root = filepath.Join(destination, "library")
	cfg.Plugins.Dir = filepath.Join(destination, "plugins")
	cfg.Bilibili.CookieFile = filepath.Join(destination, "cookies.json")
	cfg.Teriteri.WebRoot = filepath.Join(destination, "teriteri")
	cfg.Log.File = ""
	cfg.FFmpeg.Path = ""
	cfg.Listen = "127.0.0.1:8080"
	cfg.Teriteri.Enabled = false
	cfg.Plugins.Enabled = false
	cfg.Normalize()
	return config.SaveConfigFile(filepath.Join(stage, "config.yaml"), cfg)
}

func WriteCatalog(path string, value Catalog) error { return writeJSON(path, value) }
func Encode(value any) (string, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	return string(body), err
}
