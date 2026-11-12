package store

import (
	"database/sql"
	"fmt"
	"path"
)

const assetSchema = `
CREATE TABLE IF NOT EXISTS archive_assets (
 id INTEGER PRIMARY KEY AUTOINCREMENT,bvid TEXT NOT NULL,cid INTEGER NOT NULL DEFAULT 0,
 role TEXT NOT NULL,origin TEXT NOT NULL,variant TEXT NOT NULL DEFAULT '',policy_hash TEXT NOT NULL DEFAULT '',
 sha256 TEXT NOT NULL,size INTEGER NOT NULL,storage_key TEXT NOT NULL,provenance TEXT NOT NULL,
 created_at INTEGER NOT NULL,verified_at INTEGER NOT NULL,
 UNIQUE(bvid,cid,role,origin,variant,policy_hash,sha256)
);
CREATE INDEX IF NOT EXISTS idx_assets_identity ON archive_assets(bvid,cid,role,policy_hash,id DESC);
CREATE TABLE IF NOT EXISTS current_pages(bvid TEXT NOT NULL,page INTEGER NOT NULL,cid INTEGER NOT NULL,PRIMARY KEY(bvid,page),UNIQUE(bvid,cid));
CREATE TABLE IF NOT EXISTS page_sets(bvid TEXT PRIMARY KEY,epoch INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS legacy_part_bindings(bvid TEXT NOT NULL,page INTEGER NOT NULL,cid INTEGER NOT NULL,PRIMARY KEY(bvid,page));
INSERT OR IGNORE INTO legacy_part_bindings SELECT bvid,page,MIN(cid) FROM pages GROUP BY bvid,page HAVING COUNT(*)=1;
CREATE TABLE IF NOT EXISTS video_policies(bvid TEXT PRIMARY KEY,hidden INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
INSERT OR IGNORE INTO video_policies SELECT bvid,1,unixepoch() FROM videos WHERE status='private';
CREATE TABLE IF NOT EXISTS storage_roots(id TEXT PRIMARY KEY,path TEXT NOT NULL);
`

type Asset struct {
	ID         int64  `json:"id"`
	Bvid       string `json:"bvid"`
	Cid        int64  `json:"cid"`
	Role       string `json:"role"`
	Origin     string `json:"origin"`
	Variant    string `json:"variant"`
	PolicyHash string `json:"policy_hash"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	StorageKey string `json:"storage_key"`
	Provenance string `json:"provenance"`
	CreatedAt  int64  `json:"created_at"`
	VerifiedAt int64  `json:"verified_at"`
}

const assetCols = `id,bvid,cid,role,origin,variant,policy_hash,sha256,size,storage_key,provenance,created_at,verified_at`

func scanAsset(row interface{ Scan(...any) error }) (*Asset, error) {
	a := &Asset{}
	err := row.Scan(&a.ID, &a.Bvid, &a.Cid, &a.Role, &a.Origin, &a.Variant, &a.PolicyHash, &a.SHA256, &a.Size, &a.StorageKey, &a.Provenance, &a.CreatedAt, &a.VerifiedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) AddAsset(a *Asset) error {
	err := s.DB.QueryRow(`INSERT INTO archive_assets(bvid,cid,role,origin,variant,policy_hash,sha256,size,storage_key,provenance,created_at,verified_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(bvid,cid,role,origin,variant,policy_hash,sha256) DO UPDATE SET verified_at=excluded.verified_at RETURNING id`, a.Bvid, a.Cid, a.Role, a.Origin, a.Variant, a.PolicyHash, a.SHA256, a.Size, a.StorageKey, a.Provenance, a.CreatedAt, a.VerifiedAt).Scan(&a.ID)
	return err
}

func (s *Store) FindAsset(bvid string, cid int64, role, policy string) (*Asset, error) {
	return scanAsset(s.DB.QueryRow(`SELECT `+assetCols+` FROM archive_assets WHERE bvid=? AND cid=? AND role=? AND (?='' OR policy_hash=?) ORDER BY id DESC LIMIT 1`, bvid, cid, role, policy, policy))
}
func (s *Store) Assets(bvid string) ([]Asset, error) {
	rows, err := s.DB.Query(`SELECT `+assetCols+` FROM archive_assets WHERE (?='' OR bvid=?) ORDER BY id`, bvid, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		a, e := scanAsset(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ObservePages replaces only the current projection; historical page rows remain.
func (s *Store) ObservePages(bvid string, pages []*Page, epoch int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO page_sets VALUES(?,?) ON CONFLICT(bvid) DO UPDATE SET epoch=excluded.epoch`, bvid, epoch); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM current_pages WHERE bvid=?`, bvid); err != nil {
		return err
	}
	for _, p := range pages {
		if p.Page < 1 || p.Cid < 1 {
			return fmt.Errorf("invalid part identity")
		}
		if _, err = tx.Exec(`INSERT INTO current_pages VALUES(?,?,?)`, bvid, p.Page, p.Cid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PartLocalPath(bvid string, number int) (string, error) {
	pages, err := s.ListPages(bvid)
	if err != nil {
		return "", err
	}
	for _, p := range pages {
		if p.Page != number {
			continue
		}
		a, e := s.FindAsset(bvid, p.Cid, "video", "")
		if e == nil {
			return a.StorageKey, nil
		}
		if e != ErrNotFound {
			return "", e
		}
		var oldPage int
		if e = s.DB.QueryRow(`SELECT page FROM legacy_part_bindings WHERE bvid=? AND cid=?`, bvid, p.Cid).Scan(&oldPage); e == nil {
			return path.Join("media", bvid, fmt.Sprintf("p%d", oldPage), fmt.Sprintf("%s-p%d.mp4", bvid, oldPage)), nil
		}
		var observed bool
		if e = s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM page_sets WHERE bvid=?)`, bvid).Scan(&observed); e != nil {
			return "", e
		}
		if !observed {
			if number == 1 {
				if v, e := s.GetVideo(bvid); e == nil && v.LocalPath != "" {
					return v.LocalPath, nil
				}
			}
			return path.Join("media", bvid, fmt.Sprintf("p%d", number), fmt.Sprintf("%s-p%d.mp4", bvid, number)), nil
		}
		return "", ErrNotFound
	}
	return "", ErrNotFound
}

func (s *Store) VideoHidden(bvid string) bool {
	var hidden bool
	err := s.DB.QueryRow(`SELECT hidden FROM video_policies WHERE bvid=?`, bvid).Scan(&hidden)
	if err != nil && err != sql.ErrNoRows {
		return true
	}
	return hidden
}
func (s *Store) SetVideoHidden(bvid string, hidden bool) error {
	_, err := s.DB.Exec(`INSERT INTO video_policies VALUES(?,?,?) ON CONFLICT(bvid) DO UPDATE SET hidden=excluded.hidden,updated_at=excluded.updated_at`, bvid, boolInt(hidden), Now())
	return err
}

func (s *Store) ListPages(bvid string) ([]*Page, error) {
	all, err := s.ListPageHistory(bvid)
	if err != nil {
		return nil, err
	}
	var observed bool
	if err = s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM page_sets WHERE bvid=?)`, bvid).Scan(&observed); err != nil {
		return nil, err
	}
	if !observed {
		seen := map[int]bool{}
		for _, p := range all {
			if seen[p.Page] {
				return nil, fmt.Errorf("ambiguous legacy parts for %s; refresh video metadata", bvid)
			}
			seen[p.Page] = true
		}
		return all, nil
	}
	rows, err := s.DB.Query(`SELECT page,cid FROM current_pages WHERE bvid=? ORDER BY page`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byCID := map[int64]*Page{}
	for _, p := range all {
		byCID[p.Cid] = p
	}
	out := []*Page{}
	for rows.Next() {
		var number int
		var cid int64
		if err = rows.Scan(&number, &cid); err != nil {
			return nil, err
		}
		p, ok := byCID[cid]
		if !ok {
			return nil, fmt.Errorf("current part has no history")
		}
		copy := *p
		copy.Page = number
		out = append(out, &copy)
	}
	return out, rows.Err()
}

func (s *Store) LegacyPartPath(bvid string, cid int64) (string, error) {
	var page int
	if err := s.DB.QueryRow(`SELECT page FROM legacy_part_bindings WHERE bvid=? AND cid=?`, bvid, cid).Scan(&page); err != nil {
		return "", ErrNotFound
	}
	return path.Join("media", bvid, fmt.Sprintf("p%d", page), fmt.Sprintf("%s-p%d.mp4", bvid, page)), nil
}
