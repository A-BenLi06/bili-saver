package server

import (
	"archive/zip"
	"bili-saver/internal/fileio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// A single read transaction captures origin records only. Build the complete ZIP
// before sending headers so SQL/encoding failures cannot become successful downloads.
var coreExportSlot = make(chan struct{}, 1)

func (s *Server) handleCoreExport(w http.ResponseWriter, r *http.Request) {
	select {
	case coreExportSlot <- struct{}{}:
		defer func() { <-coreExportSlot }()
	default:
		w.Header().Set("Retry-After", "30")
		errJSON(w, 429, fmt.Errorf("another core export is in progress"))
		return
	}
	tx, err := s.Deps.St.DB.BeginTx(r.Context(), nil)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	defer tx.Rollback()
	f, err := os.CreateTemp("", "bili-core-*.zip")
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = fileio.ProtectPrivate(f.Name()); err != nil {
		errJSON(w, 500, err)
		return
	}
	z := zip.NewWriter(f)
	queries := map[string]string{
		"videos":      "SELECT bvid,aid,title,description,pic,owner_mid,owner_name,pubdate,duration,pages_count,status,first_archived_at,last_sync_at FROM videos ORDER BY bvid",
		"pages":       "SELECT * FROM pages ORDER BY bvid,cid",
		"comments":    "SELECT * FROM comments ORDER BY bvid,rpid",
		"danmaku":     "SELECT * FROM danmaku ORDER BY bvid,cid,dmid",
		"statistics":  "SELECT * FROM video_stats_history ORDER BY bvid,ts",
		"subtitles":   "SELECT bvid,cid,lang,lang_doc,origin,format,generator,model,created_at FROM subtitles WHERE origin='bilibili' ORDER BY bvid,cid,lang",
		"conclusions": "SELECT * FROM ai_conclusions WHERE origin='bilibili:ai-conclusion' ORDER BY bvid",
		"tags":        "SELECT vt.bvid,t.name,vt.origin,vt.created_at FROM video_tags vt JOIN tags t ON t.id=vt.tag_id WHERE vt.origin='bilibili' ORDER BY vt.bvid,t.name",
	}
	counts := map[string]int64{}
	for _, name := range []string{"videos", "pages", "comments", "danmaku", "statistics", "subtitles", "conclusions", "tags"} {
		rows, e := tx.QueryContext(r.Context(), queries[name])
		if e != nil {
			err = e
			break
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			err = e
			break
		}
		entry, e := z.Create(name + ".ndjson")
		if e != nil {
			rows.Close()
			err = e
			break
		}
		encoder := json.NewEncoder(entry)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if e = rows.Scan(pointers...); e != nil {
				break
			}
			record := map[string]any{}
			for i, key := range columns {
				if b, ok := values[i].([]byte); ok {
					record[key] = string(b)
				} else {
					record[key] = values[i]
				}
			}
			if e = encoder.Encode(record); e != nil {
				break
			}
			counts[name]++
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			err = e
			break
		}
	}
	if err == nil {
		entry, e := z.Create("manifest.json")
		err = e
		if e == nil {
			err = json.NewEncoder(entry).Encode(map[string]any{"version": 1, "kind": "origin-record-export", "created_at": time.Now().UTC().Format(time.RFC3339), "counts": counts, "excludes": []string{"media bytes", "raw response files", "subtitle files", "accounts", "credentials", "settings", "plugins", "derived data"}})
		}
	}
	closeErr := z.Close()
	_ = tx.Rollback()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		errJSON(w, 500, fmt.Errorf("core export failed: %w", err))
		return
	}
	if err = f.Sync(); err != nil {
		errJSON(w, 500, err)
		return
	}
	if _, err = f.Seek(0, 0); err != nil {
		errJSON(w, 500, err)
		return
	}
	info, err := f.Stat()
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="bili-saver-core.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "bili-saver-core.zip", info.ModTime(), f)
}
