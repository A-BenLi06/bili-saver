package store

import (
	"database/sql"
	"errors"
)

// CreateActiveJobPrio 一条写语句完成查重和落库；SQLite 串行写入保证跨调用者原子性。
// 无新增行返回 0，不依赖连接上可能残留的 LastInsertId。
func (s *Store) CreateActiveJobPrio(jobType, payload string, priority int) (int64, error) {
	now := Now()
	var id int64
	err := s.DB.QueryRow(`INSERT INTO jobs(type,payload_json,state,priority,created_at,updated_at)
		SELECT ?,?,'queued',?,?,? WHERE NOT EXISTS
		(SELECT 1 FROM jobs WHERE type=? AND payload_json=? AND state IN('queued','running'))
		RETURNING id`, jobType, payload, priority, now, now, jobType, payload).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// QueuedJob 只含恢复需要的 ID/优先级/类型/视频键，不把全部载荷与日志搬入 Go。
type QueuedJob struct {
	ID       int64
	Priority int
	Type     string
	Bvid     string
}

func (s *Store) QueuedJobs() ([]QueuedJob, error) {
	rows, err := s.DB.Query(`SELECT id,COALESCE(priority,0),type,
		CASE WHEN json_valid(payload_json) AND json_type(payload_json,'$.bvid')='text'
		THEN json_extract(payload_json,'$.bvid') ELSE '' END
		FROM jobs WHERE state='queued' ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedJob
	for rows.Next() {
		var j QueuedJob
		if err := rows.Scan(&j.ID, &j.Priority, &j.Type, &j.Bvid); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
