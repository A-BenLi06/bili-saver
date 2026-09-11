// Package store 封装 SQLite 存取：schema 迁移与各领域查询。
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// Store 是所有持久化操作的入口。
type Store struct {
	DB *sql.DB
}

// Now 统一用 unix 秒。
func Now() int64 { return time.Now().Unix() }

// Open 打开（或创建）数据库并执行迁移。dataDir 用于存放 db 文件。
func Open(dbPath string) (*Store, error) {
	if err := ensureDirFor(dbPath); err != nil {
		return nil, err
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 写并发受限；单写多读由 WAL 保证，这里限制连接数避免锁竞争。
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// migrate 采用幂等基线 + 增量补列。
// v1 基线：schema.sql 全部 IF NOT EXISTS，可安全重复执行；
// v2 增量：同步策略列，老库用 ensureColumn 幂等补列。
func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		return err
	}
	body, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	stmts := strings.Split(string(body), ";\n")
	for _, stmt := range stmts {
		// 去掉语句内部的注释行（语句可能以注释开头，不能整条跳过）
		var lines []string
		for _, line := range strings.Split(stmt, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			lines = append(lines, line)
		}
		stmt = strings.TrimSpace(strings.Join(lines, "\n"))
		if stmt == "" {
			continue
		}
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("执行迁移语句失败: %w\nSQL: %.200s", err, stmt)
		}
	}

	// v6 增量：B站用户等级（评论者随 reply 采集；UP主经 space acc/info 拉取）。
	if err := s.ensureColumn("comments", "member_level", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("videos", "owner_level", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// v7 增量：源级新视频触发器 + 跟踪列表。
	if err := s.ensureColumn("sources", "auto_download", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS video_tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bvid TEXT NOT NULL UNIQUE,
		source_id INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'queued',
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	// v5 增量：视频 ↔ 监控源 关联（归档项目展示源信息与下次同步）。
	if err := s.ensureColumn("videos", "source_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	// v9 增量：分区与横竖屏（媒体库 match_rules.categories 路由生效）。
	// orientation 由 dimension 派生：horizontal/vertical，未知为空；原始宽高在 raw 快照保真。
	if err := s.ensureColumn("videos", "tid", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("videos", "tname", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("videos", "orientation", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// v9.1 数据修复：新版收藏夹 URL 的 fid 带 "ml"（media-list）前缀曾被原样入库，
	// sync_source 每次调度都 ParseInt 失败刷库（PITFALLS P-009）。幂等：仅仍带前缀
	// 的行受影响；NOT EXISTS 防 UNIQUE(type,ext_id) 冲突。
	if _, err := s.DB.Exec(`UPDATE sources SET ext_id = substr(ext_id, 3)
		WHERE type = 'favlist' AND ext_id LIKE 'ml%'
		AND NOT EXISTS (SELECT 1 FROM sources s2 WHERE s2.type = 'favlist' AND s2.ext_id = substr(sources.ext_id, 3))`); err != nil {
		return err
	}

	// v10 增量：UP主源扩展归档（动态/动态视频/图文专栏）。
	if err := s.ensureColumn("sources", "space_scope", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS space_posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mid INTEGER NOT NULL,
		post_id TEXT NOT NULL,
		dtype TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'dynamic',
		bvid TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		pics INTEGER NOT NULL DEFAULT 0,
		origin TEXT NOT NULL DEFAULT 'bilibili',
		raw TEXT NOT NULL DEFAULT '',
		pubdate INTEGER NOT NULL DEFAULT 0,
		first_seen_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		deleted_at INTEGER NOT NULL DEFAULT 0,
		UNIQUE(mid, post_id)
	)`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_space_posts_mid ON space_posts(mid, kind)`); err != nil {
		return err
	}

	// v11 增量：补档器多用户（用户/会话/等级 + 源归属）。
	if err := s.ensureColumn("sources", "user_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		pass_hash TEXT NOT NULL,
		salt TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user',
		tier_id INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS user_tiers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		max_daily_adds INTEGER NOT NULL DEFAULT 10,
		min_interval_minutes INTEGER NOT NULL DEFAULT 60
	)`); err != nil {
		return err
	}
	// 种子：基础等级 + 初始管理员（admin / bili-saver，首次启动后请尽快改密）
	var tierN, userN int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM user_tiers`).Scan(&tierN); err != nil {
		return err
	}
	if tierN == 0 {
		if _, err := s.DB.Exec(`INSERT INTO user_tiers(name, max_daily_adds, min_interval_minutes)
			VALUES('基础', 10, 60),('进阶', 30, 15)`); err != nil {
			return err
		}
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userN); err != nil {
		return err
	}
	if userN == 0 {
		salt := newSalt()
		if _, err := s.DB.Exec(`INSERT INTO users(username, pass_hash, salt, role, tier_id, enabled, created_at)
			VALUES('admin', ?, ?, 'admin', 1, 1, ?)`,
			HashPassword("bili-saver", salt), salt, time.Now().Unix()); err != nil {
			return err
		}
	}

	// v4 增量：媒体库独立配置表。
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS media_libraries (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  name TEXT NOT NULL,
	  root_path TEXT NOT NULL,
	  layout TEXT NOT NULL DEFAULT 'movie',
	  link_mode TEXT NOT NULL DEFAULT 'auto',
	  metadata_format TEXT NOT NULL DEFAULT 'nfo',
	  match_rules TEXT NOT NULL DEFAULT '{}',
	  enabled INTEGER NOT NULL DEFAULT 1,
	  created_at INTEGER NOT NULL,
	  UNIQUE(name)
	)`); err != nil {
		return err
	}

	// v3 增量：消失检测防御——异常大批消失需连续两轮确认。
	type col3 struct{ table, name, ddl string }
	v3 := []col3{
		{"videos", "comments_anomaly", "INTEGER NOT NULL DEFAULT 0"},
		{"videos", "danmaku_anomaly", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range v3 {
		if err := s.ensureColumn(c.table, c.name, c.ddl); err != nil {
			return err
		}
	}

	// v2 增量：同步策略列。
	type col struct{ table, name, ddl string }
	v2 := []col{
		{"sources", "sync_mode", "TEXT NOT NULL DEFAULT 'interval'"},
		{"sources", "interval_comments_minutes", "INTEGER NOT NULL DEFAULT 0"},
		{"sources", "interval_danmaku_minutes", "INTEGER NOT NULL DEFAULT 0"},
		{"sources", "interval_subtitles_minutes", "INTEGER NOT NULL DEFAULT 0"},
		{"videos", "last_stats_at", "INTEGER NOT NULL DEFAULT 0"},
		{"videos", "last_comments_at", "INTEGER NOT NULL DEFAULT 0"},
		{"videos", "last_danmaku_at", "INTEGER NOT NULL DEFAULT 0"},
		{"videos", "last_subtitles_at", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range v2 {
		if err := s.ensureColumn(c.table, c.name, c.ddl); err != nil {
			return err
		}
	}
	// 仅信息展示（无代码读取）：真实 schema 版本靠上面的幂等基线 + ensureColumn
	// 增量补列保证，此值与 §5 迁移历史人工同步（当前 v9）。
	_, err = s.DB.Exec(`INSERT INTO schema_meta(k,v) VALUES('schema','9') ON CONFLICT(k) DO UPDATE SET v='9'`)
	return err
}

// ensureColumn 幂等补列：存在则跳过。
func (s *Store) ensureColumn(table, name, ddl string) error {
	rows, err := s.DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &cname, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if cname == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.DB.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, ddl))
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	return err
}

func ensureDirFor(path string) error {
	i := strings.LastIndexByte(path, '/')
	j := strings.LastIndexByte(path, '\\')
	if i < j {
		i = j
	}
	if i <= 0 {
		return nil
	}
	return os.MkdirAll(path[:i], 0o755)
}
