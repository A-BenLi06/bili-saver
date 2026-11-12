package store

import "database/sql"

const projectionSchema = `
CREATE TABLE IF NOT EXISTS library_outputs(library_id INTEGER NOT NULL,root TEXT NOT NULL,key TEXT NOT NULL,fingerprint TEXT NOT NULL,previous TEXT NOT NULL DEFAULT '',PRIMARY KEY(library_id,root,key));
CREATE TABLE IF NOT EXISTS library_groups(library_id INTEGER NOT NULL,group_key TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(library_id,group_key));
CREATE TABLE IF NOT EXISTS library_episodes(library_id INTEGER NOT NULL,group_key TEXT NOT NULL,bvid TEXT NOT NULL,episode INTEGER NOT NULL,PRIMARY KEY(library_id,group_key,bvid),UNIQUE(library_id,group_key,episode));
CREATE TABLE IF NOT EXISTS source_tombstones(source_id INTEGER PRIMARY KEY,type TEXT NOT NULL,ext_id TEXT NOT NULL,title TEXT NOT NULL,extra TEXT NOT NULL,deleted_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS historical_source_videos(source_id INTEGER NOT NULL,bvid TEXT NOT NULL,PRIMARY KEY(source_id,bvid));
CREATE TRIGGER IF NOT EXISTS preserve_source_identity BEFORE DELETE ON sources BEGIN
 INSERT INTO source_tombstones(source_id,type,ext_id,title,extra,deleted_at) VALUES(old.id,old.type,old.ext_id,old.title,old.extra,unixepoch()) ON CONFLICT(source_id) DO UPDATE SET title=excluded.title,extra=excluded.extra,deleted_at=excluded.deleted_at;
 INSERT OR IGNORE INTO historical_source_videos SELECT old.id,bvid FROM source_videos WHERE source_id=old.id;
 INSERT OR IGNORE INTO historical_source_videos SELECT old.id,bvid FROM videos WHERE source_id=old.id;
END;
INSERT OR IGNORE INTO source_videos(source_id,bvid) SELECT source_id,bvid FROM videos WHERE source_id IN(SELECT id FROM sources);
`

type LibraryOutput struct{ Key, Fingerprint, Previous string }

func (s *Store) LibraryOutputs(id int64, root string) (map[string]LibraryOutput, error) {
	rows, err := s.DB.Query(`SELECT key,fingerprint,previous FROM library_outputs WHERE library_id=? AND root=?`, id, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LibraryOutput{}
	for rows.Next() {
		var v LibraryOutput
		if err = rows.Scan(&v.Key, &v.Fingerprint, &v.Previous); err != nil {
			return nil, err
		}
		out[v.Key] = v
	}
	return out, rows.Err()
}
func (s *Store) OutputIntent(id int64, root, key, fingerprint, previous string) error {
	_, err := s.DB.Exec(`INSERT INTO library_outputs(library_id,root,key,fingerprint,previous) VALUES(?,?,?,?,?) ON CONFLICT(library_id,root,key) DO UPDATE SET fingerprint=excluded.fingerprint,previous=excluded.previous`, id, root, key, fingerprint, previous)
	return err
}
func (s *Store) OutputPublished(id int64, root, key string) error {
	_, err := s.DB.Exec(`UPDATE library_outputs SET previous='' WHERE library_id=? AND root=? AND key=?`, id, root, key)
	return err
}
func (s *Store) ForgetOutput(id int64, root, key string) error {
	_, err := s.DB.Exec(`DELETE FROM library_outputs WHERE library_id=? AND root=? AND key=?`, id, root, key)
	return err
}

func (s *Store) StableGroup(id int64, key, name string, ordered []string) (string, map[string]int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO library_groups VALUES(?,?,?) ON CONFLICT DO NOTHING`, id, key, name); err != nil {
		return "", nil, err
	}
	if err = tx.QueryRow(`SELECT name FROM library_groups WHERE library_id=? AND group_key=?`, id, key).Scan(&name); err != nil {
		return "", nil, err
	}
	out := map[string]int{}
	for _, bvid := range ordered {
		var episode int
		err = tx.QueryRow(`SELECT episode FROM library_episodes WHERE library_id=? AND group_key=? AND bvid=?`, id, key, bvid).Scan(&episode)
		if err == sql.ErrNoRows {
			err = tx.QueryRow(`INSERT INTO library_episodes SELECT ?,?,?,COALESCE(MAX(episode),0)+1 FROM library_episodes WHERE library_id=? AND group_key=? RETURNING episode`, id, key, bvid, id, key).Scan(&episode)
		}
		if err != nil {
			return "", nil, err
		}
		out[bvid] = episode
	}
	return name, out, tx.Commit()
}

// Collections retains memberships even after the monitoring source is deleted.
func (s *Store) Collections(bvid string) ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT id,type,ext_id,title FROM sources WHERE type IN('favlist','season','series') AND (id IN(SELECT source_id FROM source_videos WHERE bvid=?) OR id IN(SELECT source_id FROM videos WHERE bvid=?)) UNION SELECT source_id,type,ext_id,title FROM source_tombstones WHERE type IN('favlist','season','series') AND source_id IN(SELECT source_id FROM historical_source_videos WHERE bvid=?) ORDER BY 1`, bvid, bvid, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		var v Source
		if err = rows.Scan(&v.ID, &v.Type, &v.ExtID, &v.Title); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}
