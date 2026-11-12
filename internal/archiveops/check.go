package archiveops

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/archivepath"
	"bili-saver/internal/assets"
	"bili-saver/internal/store"
)

type Issue struct {
	Kind    string `json:"kind"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}
type Report struct {
	Schema       int     `json:"schema"`
	DatabaseOK   bool    `json:"database_ok"`
	FilesChecked int     `json:"files_checked"`
	Issues       []Issue `json:"issues"`
}

func Check(ctx context.Context, root string) (*Report, error) {
	if _, err := os.Stat(filepath.Join(root, "bili-saver.db")); err != nil {
		return nil, err
	}
	lock, err := archiveguard.Acquire(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return checkLocked(ctx, root)
}

func checkLocked(ctx context.Context, root string) (*Report, error) {
	db, err := openReadOnly(ctx, root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	r := &Report{Issues: []Issue{}}
	if err = db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&r.Schema); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return nil, err
	}
	r.DatabaseOK = true
	for rows.Next() {
		var message string
		if err = rows.Scan(&message); err != nil {
			rows.Close()
			return nil, err
		}
		if message != "ok" {
			r.DatabaseOK = false
			r.Issues = append(r.Issues, Issue{Kind: "database", Message: message})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if r.Schema > store.SchemaVersion {
		r.Issues = append(r.Issues, Issue{Kind: "schema", Message: "database requires a newer application"})
		return r, nil
	}
	for _, item := range []struct{ table, column string }{{"videos", "local_path"}, {"subtitles", "file_path"}} {
		rows, err = db.QueryContext(ctx, `SELECT DISTINCT `+item.column+` FROM `+item.table+` WHERE `+item.column+`<>''`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			if err = rows.Scan(&key); err != nil {
				rows.Close()
				return nil, err
			}
			p, e := archivepath.Resolve(root, key)
			if e == nil {
				var fi os.FileInfo
				fi, e = os.Stat(p)
				if e == nil && (!fi.Mode().IsRegular() || fi.Size() == 0) {
					e = fmt.Errorf("not a nonempty regular file")
				}
			}
			if e != nil {
				r.Issues = append(r.Issues, Issue{item.table, key, e.Error()})
			}
			r.FilesChecked++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT body_path,resp_hash FROM snapshots`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, want string
		if err = rows.Scan(&key, &want); err != nil {
			rows.Close()
			return nil, err
		}
		e := verifySnapshot(ctx, root, key, want)
		if e != nil {
			r.Issues = append(r.Issues, Issue{"snapshot", key, e.Error()})
		}
		r.FilesChecked++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var indexed bool
	if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='archive_assets')`).Scan(&indexed); err != nil {
		return nil, err
	}
	if indexed {
		st := &store.Store{DB: db}
		entries, e := st.Assets("")
		if e != nil {
			return nil, e
		}
		service := assets.Service{Store: st, Root: root}
		for _, a := range entries {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if e = service.Verify(ctx, &a); e != nil {
				r.Issues = append(r.Issues, Issue{"asset", a.StorageKey, e.Error()})
			}
			r.FilesChecked++
		}
	}
	rows, err = db.QueryContext(ctx, `SELECT bvid,page,COUNT(*) FROM pages GROUP BY bvid,page HAVING COUNT(*)>1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var bvid string
		var page, count int
		if err = rows.Scan(&bvid, &page, &count); err != nil {
			rows.Close()
			return nil, err
		}
		r.Issues = append(r.Issues, Issue{"historical-parts", bvid, fmt.Sprintf("part %d has %d retained CID versions; current-page projection and asset provenance must be checked", page, count)})
	}
	err = rows.Err()
	rows.Close()
	return r, err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func verifySnapshot(ctx context.Context, root, key, want string) error {
	p, err := archivepath.Resolve(root, key)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx, gz}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("raw response checksum mismatch")
	}
	return nil
}

func hasTable(db *sql.DB, name string) bool {
	var yes bool
	_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, name).Scan(&yes)
	return yes
}
