package store

// 媒体库独立配置：路径/布局/链接模式/元数据格式可按库自定义；
// match_rules 预留按 tag/分区/UP主把视频路由到不同库。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"bili-saver/internal/bilicats"
)

// MatchRules 媒体库路由规则；全空 = 匹配所有视频（默认库语义）。
type MatchRules struct {
	Tags       []string `json:"tags"`       // 命中任一 B 站标签即匹配
	Categories []string `json:"categories"` // 预留：视频分区（tname）
	OwnerMids  []int64  `json:"owner_mids"` // 预留：UP 主
}

// MediaLibrary 一个独立配置的媒体库输出。
type MediaLibrary struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	RootPath       string `json:"root_path"`
	Layout         string `json:"layout"`          // movie | show | collection
	LinkMode       string `json:"link_mode"`       // auto | symlink | hardlink | copy
	MetadataFormat string `json:"metadata_format"` // nfo | json | none
	MatchRules     string `json:"match_rules"`     // JSON MatchRules
	Enabled        bool   `json:"enabled"`
	CreatedAt      int64  `json:"created_at"`
}

// ParseRules 解析路由规则。
func (m *MediaLibrary) ParseRules() MatchRules {
	var r MatchRules
	if m.MatchRules != "" && m.MatchRules != "{}" {
		_ = json.Unmarshal([]byte(m.MatchRules), &r)
	}
	return r
}

// Matches 判断视频是否路由到该库。tags 为该视频的 B 站原生标签。
func (m *MediaLibrary) Matches(v *Video, tags []string) bool {
	r := m.ParseRules()
	if len(r.Tags) == 0 && len(r.Categories) == 0 && len(r.OwnerMids) == 0 {
		return true // 默认库：全匹配
	}
	for _, want := range r.Tags {
		for _, have := range tags {
			if have == want {
				return true
			}
		}
	}
	for _, mid := range r.OwnerMids {
		if v.OwnerMid == mid {
			return true
		}
	}
	// categories：分区名匹配。候选名 = 落库 tname + 本地分区表（bilicats）按 tid
	// 解析的现名与一级分区名——存量行 tname 为空也能凭 tid 立即命中；
	// 规则里填二级名（美食制作）或一级名（美食）均可。
	for _, want := range r.Categories {
		if want == "" {
			continue
		}
		for _, have := range catNames(v) {
			if have == want {
				return true
			}
		}
	}
	return false
}

// catNames 该视频的全部候选分区名（去空）。
func catNames(v *Video) []string {
	var out []string
	if v.Tname != "" {
		out = append(out, v.Tname)
	}
	if v.Tid != 0 {
		out = append(out, bilicats.Names(int(v.Tid))...)
	}
	return out
}

const libCols = `id, name, root_path, layout, link_mode, metadata_format, match_rules, enabled, created_at`

func scanLibrary(row interface{ Scan(...any) error }) (*MediaLibrary, error) {
	var l MediaLibrary
	var enabled int
	err := row.Scan(&l.ID, &l.Name, &l.RootPath, &l.Layout, &l.LinkMode,
		&l.MetadataFormat, &l.MatchRules, &enabled, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.Enabled = enabled == 1
	return &l, nil
}

// UpsertMediaLibrary 保存媒体库配置。
func (s *Store) UpsertMediaLibrary(l *MediaLibrary) (int64, error) {
	if l.Layout == "" {
		l.Layout = "movie"
	}
	if l.LinkMode == "" {
		l.LinkMode = "auto"
	}
	if l.MetadataFormat == "" {
		l.MetadataFormat = "nfo"
	}
	if l.MatchRules == "" {
		l.MatchRules = "{}"
	}
	res, err := s.DB.Exec(`INSERT INTO media_libraries(name, root_path, layout, link_mode, metadata_format, match_rules, enabled, created_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET root_path=excluded.root_path, layout=excluded.layout,
		  link_mode=excluded.link_mode, metadata_format=excluded.metadata_format,
		  match_rules=excluded.match_rules, enabled=excluded.enabled`,
		l.Name, l.RootPath, l.Layout, l.LinkMode, l.MetadataFormat, l.MatchRules,
		boolInt(l.Enabled), l.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		var qid int64
		if err2 := s.DB.QueryRow(`SELECT id FROM media_libraries WHERE name=?`, l.Name).Scan(&qid); err2 == nil {
			id = qid
		}
	}
	l.ID = id
	return id, nil
}

func (s *Store) GetMediaLibrary(id int64) (*MediaLibrary, error) {
	return scanLibrary(s.DB.QueryRow(`SELECT `+libCols+` FROM media_libraries WHERE id=?`, id))
}

func (s *Store) ListMediaLibraries() ([]*MediaLibrary, error) {
	rows, err := s.DB.Query(`SELECT ` + libCols + ` FROM media_libraries ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MediaLibrary
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMediaLibrary(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM media_libraries WHERE id=?`, id)
	return err
}

// EnsureDefaultLibrary 若无任何库且 cfg 指定了 root，则迁移为默认库（向后兼容）。
func (s *Store) EnsureDefaultLibrary(root, layout, linkMode string) error {
	if root == "" {
		return nil
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM media_libraries`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.UpsertMediaLibrary(&MediaLibrary{
		Name: "默认库", RootPath: root, Layout: layout, LinkMode: linkMode,
		MetadataFormat: "nfo", Enabled: true, CreatedAt: Now(),
	})
	return err
}

var _ = strings.TrimSpace
