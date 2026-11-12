package jobs

import "encoding/json"

type sourceBackoff struct {
	fails    int
	lastSeen int64
	ended    bool
}

// 一轮 tick 只读取并解析最近 20 条任务一次，保持原有退避窗口与阈值。
func (s *Scheduler) sourceBackoffs() map[int64]sourceBackoff {
	rows, err := s.St.DB.Query(`SELECT state,payload_json,created_at FROM jobs WHERE type='sync_source' ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[int64]sourceBackoff)
	for rows.Next() {
		var state, payload string
		var at int64
		if err := rows.Scan(&state, &payload, &at); err != nil {
			continue
		}
		var p struct {
			SourceID int64 `json:"source_id"`
		}
		if json.Unmarshal([]byte(payload), &p) != nil {
			continue
		}
		b := out[p.SourceID]
		if b.ended {
			continue
		}
		if b.lastSeen == 0 {
			b.lastSeen = at
		}
		if state != "failed" {
			b.ended = true
		} else {
			b.fails++
		}
		out[p.SourceID] = b
	}
	return out
}
