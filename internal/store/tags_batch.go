package store

// BilibiliTagsByBvids 只预取路由/NFO 需要的 origin 标签，每块一条查询。
func (s *Store) BilibiliTagsByBvids(ids []string) (map[string][]string, error) {
	out := make(map[string][]string, len(ids))
	err := eachSyncBatch(ids, func(ph string, args []any) error {
		rows, err := s.DB.Query(`SELECT vt.bvid,t.name FROM video_tags vt JOIN tags t ON t.id=vt.tag_id WHERE vt.bvid IN (`+ph+`) AND vt.origin='bilibili' ORDER BY vt.bvid,vt.rowid`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			out[id] = append(out[id], name)
		}
		return rows.Err()
	})
	return out, err
}
