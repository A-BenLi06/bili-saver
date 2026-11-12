package store

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// CollectionStatus separates successful retrieval from authoritative coverage.
type CollectionStatus struct {
	Bvid         string `json:"bvid"`
	Cid          int64  `json:"cid"`
	Component    string `json:"component"`
	AttemptedAt  int64  `json:"attempted_at"`
	SuccessfulAt int64  `json:"successful_at"`
	CompleteAt   int64  `json:"complete_at"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

func (s *Store) RecordCollectionStatus(bvid string, cid int64, component string, at int64, complete bool, cause error) error {
	state, success, full, message := "partial", int64(0), int64(0), ""
	if cause != nil {
		state, message = "failed", cause.Error()
	} else {
		success = at
		if complete {
			state, full = "complete", at
		}
	}
	_, err := s.DB.Exec(`INSERT INTO collection_status VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(bvid,cid,component) DO UPDATE SET attempted_at=excluded.attempted_at,successful_at=MAX(successful_at,excluded.successful_at),complete_at=MAX(complete_at,excluded.complete_at),state=excluded.state,error=excluded.error`, bvid, cid, component, at, success, full, state, message)
	return err
}

func (s *Store) CollectionStatuses(bvid string) ([]CollectionStatus, error) {
	rows, err := s.DB.Query(`SELECT bvid,cid,component,attempted_at,successful_at,complete_at,state,error FROM collection_status WHERE bvid=? ORDER BY component,cid`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CollectionStatus{}
	for rows.Next() {
		var v CollectionStatus
		if err := rows.Scan(&v.Bvid, &v.Cid, &v.Component, &v.AttemptedAt, &v.SuccessfulAt, &v.CompleteAt, &v.State, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReconcileDanmaku acknowledges a complete round atomically with its decision.
// Callers must never grant this permission to truncated/fallback observations.
func (s *Store) ReconcileDanmaku(bvid string, cid, epoch int64, first bool, seen int) (int64, bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO danmaku_sync_state(bvid,cid) VALUES(?,?) ON CONFLICT DO NOTHING`, bvid, cid); err != nil {
		return 0, false, err
	}
	var last int64
	var prior int
	var oldHash string
	if err = tx.QueryRow(`SELECT last_epoch,anomalous,missing_hash FROM danmaku_sync_state WHERE bvid=? AND cid=?`, bvid, cid).Scan(&last, &prior, &oldHash); err != nil {
		return 0, false, err
	}
	if epoch <= last {
		return 0, prior != 0, tx.Commit()
	}
	var pending int64
	if err = tx.QueryRow(`SELECT COUNT(*) FROM danmaku WHERE bvid=? AND cid=? AND status='visible' AND last_seen_at<?`, bvid, cid, epoch).Scan(&pending); err != nil {
		return 0, false, err
	}
	anomalous := !first && pending > 100 && float64(pending)/float64(pending+int64(seen)) > 0.3
	digest := ""
	if anomalous {
		rows, e := tx.Query(`SELECT dmid FROM danmaku WHERE bvid=? AND cid=? AND status='visible' AND last_seen_at<? ORDER BY dmid`, bvid, cid, epoch)
		if e != nil {
			return 0, false, e
		}
		h := sha256.New()
		var buf [8]byte
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return 0, false, e
			}
			binary.LittleEndian.PutUint64(buf[:], uint64(id))
			h.Write(buf[:])
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return 0, false, e
		}
		digest = fmt.Sprintf("%x", h.Sum(nil))
	}
	guarded := anomalous && (prior == 0 || oldHash != digest)
	var marked int64
	if !first && !guarded && pending > 0 {
		r, e := tx.Exec(`UPDATE danmaku SET status='deleted',deleted_at=? WHERE bvid=? AND cid=? AND status='visible' AND last_seen_at<?`, Now(), bvid, cid, epoch)
		if e != nil {
			return 0, false, e
		}
		marked, err = r.RowsAffected()
		if err != nil {
			return 0, false, err
		}
	}
	if _, err = tx.Exec(`UPDATE danmaku_sync_state SET last_epoch=?,anomalous=?,missing_hash=? WHERE bvid=? AND cid=?`, epoch, boolInt(guarded), digest, bvid, cid); err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(`UPDATE videos SET danmaku_anomaly=0 WHERE bvid=? AND danmaku_anomaly<>0`, bvid); err != nil {
		return 0, false, err
	}
	return marked, guarded, tx.Commit()
}
