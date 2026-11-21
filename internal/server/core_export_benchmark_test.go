package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bili-saver/internal/store"
)

func BenchmarkCoreExport(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "export.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	for _, query := range []string{
		`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<12000)
		INSERT INTO comments(rpid,bvid,content,status,first_seen_at,last_seen_at,raw_json)
		SELECT v,'BV-export',printf('归档评论 & <test> #%d',v),'visible',1,2,printf('{"id":%d}',v) FROM n`,
		`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<12000)
		INSERT INTO danmaku(dmid,bvid,cid,content,first_seen_at,last_seen_at)
		SELECT v,'BV-export',1,printf('弹幕 #%d',v),1,2 FROM n`,
	} {
		if _, err := st.DB.Exec(query); err != nil {
			b.Fatal(err)
		}
	}
	s := &Server{Deps: Deps{St: st}}
	req := httptest.NewRequest("GET", "/api/v1/archive/export", nil)
	b.ReportAllocs()
	for b.Loop() {
		out := httptest.NewRecorder()
		s.handleCoreExport(out, req)
		if out.Code != 200 || out.Body.Len() == 0 {
			b.Fatalf("export status %d: %s", out.Code, out.Body.String())
		}
	}
}
