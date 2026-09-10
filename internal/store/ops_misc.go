package store

// 其余领域：源、字幕、标签、AI 小结、派生数据、快照、任务、插件。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ---- sources ----

// UpsertSource 保存监控源并返回其数据库 ID。
func (s *Store) UpsertSource(src *Source) (int64, error) {
	if src.SyncMode == "" {
		src.SyncMode = "interval"
	}
	res, err := s.DB.Exec(`INSERT INTO sources(type, ext_id, extra, title, watch, sync_interval_minutes,
		sync_mode, interval_comments_minutes, interval_danmaku_minutes, interval_subtitles_minutes,
		layout, auto_download, space_scope, created_at, last_sync_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(type, ext_id) DO UPDATE SET title=excluded.title, extra=excluded.extra,
		  watch=excluded.watch, sync_interval_minutes=excluded.sync_interval_minutes,
		  sync_mode=excluded.sync_mode,
		  interval_comments_minutes=excluded.interval_comments_minutes,
		  interval_danmaku_minutes=excluded.interval_danmaku_minutes,
		  interval_subtitles_minutes=excluded.interval_subtitles_minutes,
		  layout=excluded.layout, auto_download=excluded.auto_download,
		  space_scope=CASE WHEN excluded.space_scope='' THEN sources.space_scope ELSE excluded.space_scope END`,
		src.Type, src.ExtID, src.Extra, src.Title, boolInt(src.Watch),
		src.SyncIntervalMinutes,
		src.SyncMode, src.IntervalCommentsMin, src.IntervalDanmakuMin, src.IntervalSubsMin,
		src.Layout, src.AutoDownload, src.SpaceScope, src.CreatedAt, src.LastSyncAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		// 驱动在 ON CONFLICT 更新时可能返回 0/报错：查询兜底
		err2 := s.DB.QueryRow(`SELECT id FROM sources WHERE type=? AND ext_id=?`, src.Type, src.ExtID).Scan(&id)
		if err2 != nil {
			if err != nil {
				return 0, err
			}
			return 0, err2
		}
	}
	src.ID = id
	return id, nil
}

func (s *Store) SetSourceSynced(id int64, ts int64) error {
	_, err := s.DB.Exec(`UPDATE sources SET last_sync_at=? WHERE id=?`, ts, id)
	return err
}

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var src Source
	var watch int
	var syncMode string
	err := row.Scan(&src.ID, &src.Type, &src.ExtID, &src.Extra, &src.Title, &watch,
		&src.SyncIntervalMinutes, &syncMode,
		&src.IntervalCommentsMin, &src.IntervalDanmakuMin, &src.IntervalSubsMin,
		&src.Layout, &src.AutoDownload, &src.CreatedAt, &src.LastSyncAt, &src.SpaceScope)
	src.SyncMode = syncMode
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	src.Watch = watch == 1
	return &src, nil
}

const sourceCols = `id, type, ext_id, extra, title, watch, sync_interval_minutes,
sync_mode, interval_comments_minutes, interval_danmaku_minutes, interval_subtitles_minutes,
layout, auto_download, created_at, last_sync_at, COALESCE(space_scope,'')`

func (s *Store) GetSource(id int64) (*Source, error) {
	return scanSource(s.DB.QueryRow(`SELECT `+sourceCols+` FROM sources WHERE id=?`, id))
}

func (s *Store) ListSources() ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT ` + sourceCols + ` FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSource(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM sources WHERE id=?`, id)
	return err
}

// ---- video_tracks（跟踪列表，v7） ----

// TrackAdd 记录一条触发器自动下载（bvid 幂等，已存在不覆盖原状态）。
func (s *Store) TrackAdd(bvid string, sourceID int64, now int64) error {
	_, err := s.DB.Exec(`INSERT INTO video_tracks(bvid, source_id, state, created_at, updated_at)
		VALUES(?,?,'queued',?,?)
		ON CONFLICT(bvid) DO NOTHING`, bvid, sourceID, now, now)
	return err
}

// TrackSetState 回写触发下载的执行状态（记录不存在则为无害空操作）。
func (s *Store) TrackSetState(bvid, state, errMsg string, now int64) error {
	_, err := s.DB.Exec(`UPDATE video_tracks SET state=?, error=?, updated_at=? WHERE bvid=?`,
		state, errMsg, now, bvid)
	return err
}

// ListTracks 跟踪列表（新→旧），联 videos 补标题/UP主/落盘状态。
func (s *Store) ListTracks(limit int) ([]*Track, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.Query(`SELECT t.id, t.bvid, t.source_id, t.state, t.error, t.created_at, t.updated_at,
		COALESCE(v.title,''), COALESCE(v.owner_name,''), COALESCE(v.owner_mid,0), COALESCE(v.downloaded,0)
		FROM video_tracks t LEFT JOIN videos v ON v.bvid=t.bvid
		ORDER BY t.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Track
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.Bvid, &t.SourceID, &t.State, &t.Error, &t.CreatedAt, &t.UpdatedAt,
			&t.Title, &t.OwnerName, &t.OwnerMid, &t.Downloaded); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// TrackStateCounts 跟踪记录分状态计数（管理端分类 Tab 徽标用）。
func (s *Store) TrackStateCounts() (map[string]int64, error) {
	rows, err := s.DB.Query(`SELECT state, COUNT(*) FROM video_tracks GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ListTracksPage 跟踪列表分页版（新→旧）：state 空为全部（queued|done|failed），
// q 对 标题/UP主/bvid 模糊匹配；返回 (页内记录, 过滤后总数)。联 videos 与 sources 补展示字段。
func (s *Store) ListTracksPage(state, q string, limit, offset int) ([]*Track, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := " WHERE 1=1"
	var args []any
	switch state {
	case "queued", "done", "failed":
		where += " AND t.state=?"
		args = append(args, state)
	}
	if qs := strings.TrimSpace(q); qs != "" {
		pat := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(qs) + "%"
		where += ` AND (t.bvid LIKE ? ESCAPE '\' OR v.title LIKE ? ESCAPE '\' OR v.owner_name LIKE ? ESCAPE '\')`
		args = append(args, pat, pat, pat)
	}
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM video_tracks t
		LEFT JOIN videos v ON v.bvid=t.bvid`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q2 := `SELECT t.id, t.bvid, t.source_id, t.state, t.error, t.created_at, t.updated_at,
		COALESCE(v.title,''), COALESCE(v.owner_name,''), COALESCE(v.owner_mid,0), COALESCE(v.downloaded,0),
		COALESCE(src.title,'')
		FROM video_tracks t
		LEFT JOIN videos v ON v.bvid=t.bvid
		LEFT JOIN sources src ON src.id=t.source_id` + where + ` ORDER BY t.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q2, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Track
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.Bvid, &t.SourceID, &t.State, &t.Error, &t.CreatedAt, &t.UpdatedAt,
			&t.Title, &t.OwnerName, &t.OwnerMid, &t.Downloaded, &t.SourceTitle); err != nil {
			return nil, 0, err
		}
		out = append(out, &t)
	}
	return out, total, rows.Err()
}

// DueSources 返回到达重同步时间的监控源。每个源按自己的间隔判断（0=全局默认）；
// sync_mode='once' 的源完成首次同步后不再到期（watch 由任务侧关闭）。
func (s *Store) DueSources(defaultMinutes int, now int64) ([]*Source, error) {
	all, err := s.ListSources()
	if err != nil {
		return nil, err
	}
	var out []*Source
	for _, src := range all {
		if !src.Watch {
			continue
		}
		if src.SyncMode == "once" && src.LastSyncAt > 0 {
			continue // 仅一次：已完成
		}
		interval := src.SyncIntervalMinutes
		if interval <= 0 {
			interval = defaultMinutes
		}
		if src.LastSyncAt == 0 || src.LastSyncAt <= now-int64(interval)*60 {
			out = append(out, src)
		}
	}
	return out, nil
}

// ---- subtitles ----

func (s *Store) UpsertSubtitle(sub *Subtitle) error {
	_, err := s.DB.Exec(`INSERT INTO subtitles(bvid, cid, lang, lang_doc, origin, format, file_path, generator, model, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(bvid, cid, lang, origin) DO UPDATE SET lang_doc=excluded.lang_doc, format=excluded.format,
		  file_path=excluded.file_path, generator=excluded.generator, model=excluded.model`,
		sub.Bvid, sub.Cid, sub.Lang, sub.LangDoc, sub.Origin, sub.Format,
		sub.FilePath, sub.Generator, sub.Model, sub.CreatedAt)
	return err
}

func (s *Store) ListSubtitles(bvid string, cid int64) ([]*Subtitle, error) {
	q := `SELECT id, bvid, cid, lang, lang_doc, origin, format, file_path, generator, model, created_at FROM subtitles WHERE bvid=?`
	args := []any{bvid}
	if cid > 0 {
		q += ` AND cid=?`
		args = append(args, cid)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subtitle
	for rows.Next() {
		var t Subtitle
		if err := rows.Scan(&t.ID, &t.Bvid, &t.Cid, &t.Lang, &t.LangDoc, &t.Origin, &t.Format,
			&t.FilePath, &t.Generator, &t.Model, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// HasCCSubtitle 判断某分P是否已有 B 站 CC 字幕（供 subtitle.missing 事件判断）。
func (s *Store) HasCCSubtitle(cid int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM subtitles WHERE cid=? AND origin='bilibili'`, cid).Scan(&n)
	return n > 0, err
}

// ---- tags ----

func (s *Store) AddTags(bvid string, names []string, origin string) error {
	if len(names) == 0 {
		return nil
	}
	// 单事务包住整批（每标签 3 条语句，逐条自动提交在批量归档时是纯开销）
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO tags(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return err
		}
		var tagID int64
		if err := tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO video_tags(bvid, tag_id, origin, created_at) VALUES(?,?,?,?)
			ON CONFLICT(bvid, tag_id, origin) DO NOTHING`, bvid, tagID, origin, Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// VideoTags 返回按 origin 分组的标签（保持 bilibili / 派生 分离）。
func (s *Store) VideoTags(bvid string) (map[string][]string, error) {
	rows, err := s.DB.Query(`SELECT vt.origin, t.name FROM video_tags vt JOIN tags t ON t.id=vt.tag_id
		WHERE vt.bvid=? ORDER BY vt.rowid`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var origin, name string
		if err := rows.Scan(&origin, &name); err != nil {
			return nil, err
		}
		out[origin] = append(out[origin], name)
	}
	return out, rows.Err()
}

// ---- ai conclusion (origin 层) ----

func (s *Store) UpsertAIConclusion(bvid, model, summary, outlineJSON string) error {
	_, err := s.DB.Exec(`INSERT INTO ai_conclusions(bvid, model, summary, outline_json, origin, fetched_at)
		VALUES(?,?,?,?, 'bilibili:ai-conclusion', ?)
		ON CONFLICT(bvid) DO UPDATE SET model=excluded.model, summary=excluded.summary,
		  outline_json=excluded.outline_json, fetched_at=excluded.fetched_at`,
		bvid, model, summary, outlineJSON, Now())
	return err
}

func (s *Store) GetAIConclusion(bvid string) (model, summary, outline string, err error) {
	err = s.DB.QueryRow(`SELECT model, summary, outline_json FROM ai_conclusions WHERE bvid=?`, bvid).
		Scan(&model, &summary, &outline)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return
}

// ---- derived ----

func (s *Store) AddSummary(sm *Summary) error {
	_, err := s.DB.Exec(`INSERT INTO derived_summaries(bvid, origin, generator, model, prompt_ver, lang, content, created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		sm.Bvid, sm.Origin, sm.Generator, sm.Model, sm.PromptVer, sm.Lang, sm.Content, Now())
	return err
}

func (s *Store) ListSummaries(bvid string) ([]*Summary, error) {
	rows, err := s.DB.Query(`SELECT id, bvid, origin, generator, model, prompt_ver, lang, content, created_at
		FROM derived_summaries WHERE bvid=? ORDER BY id`, bvid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Summary
	for rows.Next() {
		var m Summary
		if err := rows.Scan(&m.ID, &m.Bvid, &m.Origin, &m.Generator, &m.Model, &m.PromptVer, &m.Lang, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *Store) AddRelation(r *Relation) error {
	_, err := s.DB.Exec(`INSERT INTO derived_relations(src_type, src_id, dst_type, dst_id, relation, weight, origin, created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		r.SrcType, r.SrcID, r.DstType, r.DstID, r.Relation, r.Weight, r.Origin, Now())
	return err
}

func (s *Store) ListRelations(srcType, srcID string) ([]*Relation, error) {
	rows, err := s.DB.Query(`SELECT id, src_type, src_id, dst_type, dst_id, relation, weight, origin, created_at
		FROM derived_relations WHERE src_type=? AND src_id=?`, srcType, srcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.ID, &r.SrcType, &r.SrcID, &r.DstType, &r.DstID, &r.Relation, &r.Weight, &r.Origin, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) AddEmbedding(entityType, entityID, model string, vec []float32, origin string) error {
	b, _ := json.Marshal(vec)
	_, err := s.DB.Exec(`INSERT INTO embeddings(entity_type, entity_id, model, dim, vec, origin, created_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(entity_type, entity_id, model) DO UPDATE SET vec=excluded.vec, dim=excluded.dim`,
		entityType, entityID, model, len(vec), b, origin, Now())
	return err
}

// ListVideoEmbeddings 返回某模型下全部视频向量（相似检索用）。
func (s *Store) ListVideoEmbeddings(model string) (map[string][]float32, error) {
	q := `SELECT entity_id, vec FROM embeddings WHERE entity_type='video'`
	args := []any{}
	if model != "" {
		q += ` AND model=?`
		args = append(args, model)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var vec []float32
		if json.Unmarshal(raw, &vec) == nil {
			out[id] = vec
		}
	}
	return out, rows.Err()
}

// ---- snapshots ----

func (s *Store) AddSnapshot(sp *Snapshot) error {
	_, err := s.DB.Exec(`INSERT INTO snapshots(endpoint, params_json, resp_hash, body_path, status_code, fetched_at)
		VALUES(?,?,?,?,?,?)`,
		sp.Endpoint, sp.ParamsJSON, sp.RespHash, sp.BodyPath, sp.StatusCode, sp.FetchedAt)
	return err
}

// ---- jobs ----

func (s *Store) CreateJob(jobType, payloadJSON string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO jobs(type, payload_json, state, created_at, updated_at) VALUES(?,?, 'queued', ?, ?)`,
		jobType, payloadJSON, Now(), Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// HasActiveJob 防重复排队。
func (s *Store) HasActiveJob(jobType, payloadJSON string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type=? AND payload_json=? AND state IN('queued','running')`,
		jobType, payloadJSON).Scan(&n)
	return n > 0, err
}

// PruneTerminalJobs 清理终态任务（done/failed），滚动保留最近 keep 条。
// jobs 表只增不清会无限膨胀（历史上单源失败循环 8 小时刷了 300+ 条）。
// 只动终态：崩溃恢复依赖 queued/running 不受影响；syncSourceBlocked 退避只看
// 最近 20 条、管理端任务页展示近期记录，审计窗口足够。
func (s *Store) PruneTerminalJobs(keep int) (int64, error) {
	if keep < 1 {
		keep = 1
	}
	res, err := s.DB.Exec(`DELETE FROM jobs WHERE state IN ('done','failed') AND id NOT IN
		(SELECT id FROM jobs WHERE state IN ('done','failed') ORDER BY id DESC LIMIT ?)`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) UpdateJobState(id int64, state, detail, errMsg string, progress float64) error {
	_, err := s.DB.Exec(`UPDATE jobs SET state=?, detail=?, error=?, progress=?, updated_at=? WHERE id=?`,
		state, detail, errMsg, progress, Now(), id)
	return err
}

func (s *Store) GetJob(id int64) (*Job, error) {
	row := s.DB.QueryRow(`SELECT id, type, payload_json, state, progress, detail, error, created_at, updated_at FROM jobs WHERE id=?`, id)
	var j Job
	err := row.Scan(&j.ID, &j.Type, &j.PayloadJSON, &j.State, &j.Progress, &j.Detail, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) ListJobs(limit int, states []string) ([]*Job, error) {
	q := `SELECT id, type, payload_json, state, progress, detail, error, created_at, updated_at FROM jobs`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (`
		for i, st := range states {
			if i > 0 {
				q += `,`
			}
			q += `?`
			args = append(args, st)
		}
		q += `)`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Type, &j.PayloadJSON, &j.State, &j.Progress, &j.Detail, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// ---- plugins ----

func (s *Store) UpsertPlugin(p *PluginRow) error {
	_, err := s.DB.Exec(`INSERT INTO plugins(id, version, enabled, manifest_json, config_json) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET version=excluded.version, manifest_json=excluded.manifest_json`,
		p.ID, p.Version, boolInt(p.Enabled), p.ManifestJSON, p.ConfigJSON)
	return err
}

func (s *Store) ListPlugins() ([]*PluginRow, error) {
	rows, err := s.DB.Query(`SELECT id, version, enabled, manifest_json, config_json FROM plugins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PluginRow
	for rows.Next() {
		var p PluginRow
		var en int
		if err := rows.Scan(&p.ID, &p.Version, &en, &p.ManifestJSON, &p.ConfigJSON); err != nil {
			return nil, err
		}
		p.Enabled = en == 1
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *Store) SetPluginEnabled(id string, enabled bool) error {
	_, err := s.DB.Exec(`UPDATE plugins SET enabled=? WHERE id=?`, boolInt(enabled), id)
	return err
}

func (s *Store) SetPluginConfig(id, configJSON string) error {
	_, err := s.DB.Exec(`UPDATE plugins SET config_json=? WHERE id=?`, configJSON, id)
	return err
}

func (s *Store) AddPluginRun(r *PluginRun) error {
	_, err := s.DB.Exec(`INSERT INTO plugin_runs(plugin_id, event, ok, error, duration_ms, created_at) VALUES(?,?,?,?,?,?)`,
		r.PluginID, r.Event, boolInt(r.Ok), r.Error, r.DurationMs, Now())
	return err
}

// ---- 全局统计 ----

type ArchiveStats struct {
	Videos          int   `json:"videos"`
	Comments        int   `json:"comments"`
	DeletedComments int   `json:"deleted_comments"`
	Danmaku         int64 `json:"danmaku"`
	Subtitles       int   `json:"subtitles"`
	Snapshots       int64 `json:"snapshots"`
	Downloaded      int   `json:"downloaded"`
}

func (s *Store) ArchiveStats() (*ArchiveStats, error) {
	st := &ArchiveStats{}
	var v int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM videos`).Scan(&st.Videos); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM videos WHERE downloaded=1`).Scan(&st.Downloaded); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&st.Comments); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE status='deleted'`).Scan(&st.DeletedComments); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM danmaku`).Scan(&st.Danmaku); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM subtitles`).Scan(&st.Subtitles); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&st.Snapshots); err != nil {
		return nil, err
	}
	_ = v
	return st, nil
}

// ListPluginRuns 供测试与审计界面使用。
func (s *Store) ListPluginRuns() ([]*PluginRun, error) {
	rows, err := s.DB.Query(`SELECT id, plugin_id, event, ok, error, duration_ms, created_at FROM plugin_runs ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PluginRun
	for rows.Next() {
		var r PluginRun
		var ok int
		if err := rows.Scan(&r.ID, &r.PluginID, &r.Event, &ok, &r.Error, &r.DurationMs, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Ok = ok == 1
		out = append(out, &r)
	}
	return out, rows.Err()
}

// SetSourceWatch 更新源的监控开关。
func (s *Store) SetSourceWatch(id int64, watch bool) error {
	_, err := s.DB.Exec(`UPDATE sources SET watch=? WHERE id=?`, boolInt(watch), id)
	return err
}
