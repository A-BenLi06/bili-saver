package archiveops

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/archivepath"
	"bili-saver/internal/store"
)

type Issue struct {
	Kind    string `json:"kind"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}
type DisappearanceReview struct {
	Bvid  string `json:"bvid"`
	Cid   int64  `json:"cid"`
	Epoch int64  `json:"epoch"`
	Count int    `json:"count"`
}
type Report struct {
	DisappearanceReview []DisappearanceReview `json:"disappearance_review,omitempty"`
	Schema              int                   `json:"schema"`
	DatabaseOK          bool                  `json:"database_ok"`
	FilesChecked        int                   `json:"files_checked"`
	Issues              []Issue               `json:"issues"`
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
	var snapshots snapshotVerifier
	for rows.Next() {
		var key, want string
		if err = rows.Scan(&key, &want); err != nil {
			rows.Close()
			return nil, err
		}
		e := snapshots.verify(ctx, root, key, want)
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
		if err := checkAssets(ctx, db, root, r); err != nil {
			return nil, err
		}
	}
	partQuery := `SELECT bvid,page,COUNT(*) FROM pages GROUP BY bvid,page HAVING COUNT(*)>1`
	if hasTable(db, "current_pages") {
		partQuery += ` AND NOT EXISTS(SELECT 1 FROM current_pages cp WHERE cp.bvid=pages.bvid AND cp.page=pages.page)`
	}
	rows, err = db.QueryContext(ctx, partQuery)
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
	if err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT bvid,cid,deleted_at,COUNT(*) FROM danmaku WHERE status='deleted' GROUP BY bvid,cid,deleted_at HAVING COUNT(*)>100 ORDER BY bvid,cid,deleted_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate DisappearanceReview
		if err = rows.Scan(&candidate.Bvid, &candidate.Cid, &candidate.Epoch, &candidate.Count); err != nil {
			return nil, err
		}
		r.DisappearanceReview = append(r.DisappearanceReview, candidate)
	}
	return r, rows.Err()
}

func hasTable(db *sql.DB, name string) bool {
	var yes bool
	_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, name).Scan(&yes)
	return yes
}
