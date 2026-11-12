package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
)

const deliverySchema = `
CREATE TABLE IF NOT EXISTS archive_events(id INTEGER PRIMARY KEY AUTOINCREMENT,event TEXT NOT NULL,payload TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS event_deliveries(id INTEGER PRIMARY KEY AUTOINCREMENT,event_id INTEGER NOT NULL,target TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL,UNIQUE(event_id,target));
CREATE INDEX IF NOT EXISTS idx_delivery_pending ON event_deliveries(state,next_at,id);
CREATE TABLE IF NOT EXISTS derived_delivery_ops(delivery_id INTEGER NOT NULL,op_index INTEGER NOT NULL,checksum TEXT NOT NULL,PRIMARY KEY(delivery_id,op_index));
`

// EnqueueEvent and its delivery targets commit together. Delivery is at least once.
func (s *Store) EnqueueEvent(event string, payload map[string]any) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := enqueueEvent(tx, event, payload)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func enqueueEvent(tx *sql.Tx, event string, payload map[string]any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	if err = tx.QueryRow(`INSERT INTO archive_events(event,payload,created_at) VALUES(?,?,?) RETURNING id`, event, string(body), Now()).Scan(&id); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`INSERT INTO event_deliveries(event_id,target,updated_at) SELECT ?, 'plugin:'||id,? FROM plugins WHERE enabled=1 AND EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(manifest_json) THEN json_extract(manifest_json,'$.events') ELSE '[]' END) WHERE value=? OR value='*')`, id, Now(), event); err != nil {
		return 0, err
	}
	if event == "video.downloaded" || event == "video.archived" || event == "video.gone" {
		_, err = tx.Exec(`INSERT INTO event_deliveries(event_id,target,updated_at) VALUES(?,'media',?)`, id, Now())
	}
	return id, err
}

type Delivery struct {
	ID, EventID            int64
	Target, Event, Payload string
	Attempts               int
}

func (s *Store) PendingDeliveries(now int64, limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(`SELECT d.id,d.event_id,d.target,e.event,e.payload,d.attempts FROM event_deliveries d JOIN archive_events e ON e.id=d.event_id WHERE d.state='pending' AND d.next_at<=? ORDER BY d.id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err = rows.Scan(&d.ID, &d.EventID, &d.Target, &d.Event, &d.Payload, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) FinishDelivery(id int64, runErr error) error {
	if runErr == nil {
		_, err := s.DB.Exec(`UPDATE event_deliveries SET state='done',error='',updated_at=? WHERE id=?`, Now(), id)
		return err
	}
	_, err := s.DB.Exec(`UPDATE event_deliveries SET attempts=attempts+1,error=?,next_at=?+min(3600,10*(1<<min(attempts,8))),updated_at=? WHERE id=?`, runErr.Error(), Now(), Now(), id)
	return err
}

// CompleteDownload binds the visible projection and its side effects atomically.
func (s *Store) CompleteDownload(bvid, path string, payload map[string]any) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE videos SET downloaded=1,local_path=? WHERE bvid=?`, path, bvid); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE user_download_requests SET next_attempt_at=0 WHERE bvid=? AND state='waiting'`, bvid); err != nil {
		return err
	}
	if _, err = enqueueEvent(tx, "video.downloaded", payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) execDerived(deliveryID int64, opIndex int, query string, args ...any) error {
	if deliveryID == 0 {
		_, err := s.DB.Exec(query, args...)
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	body, _ := json.Marshal([]any{query, args[:len(args)-1]})
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	res, err := tx.Exec(`INSERT INTO derived_delivery_ops VALUES(?,?,?) ON CONFLICT DO NOTHING`, deliveryID, opIndex, digest)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var saved string
		if err = tx.QueryRow(`SELECT checksum FROM derived_delivery_ops WHERE delivery_id=? AND op_index=?`, deliveryID, opIndex).Scan(&saved); err != nil {
			return err
		}
		if saved != digest {
			return fmt.Errorf("plugin output changed across delivery replay")
		}
	}
	if n > 0 {
		if _, err = tx.Exec(query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
