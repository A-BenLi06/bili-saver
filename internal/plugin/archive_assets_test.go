package plugin

import (
	"bili-saver/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestAuditSubtitleFilesAreDistinctPerCID(t *testing.T) {
	root := t.TempDir()
	st, e := store.Open(filepath.Join(root, "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	b := NewBus(st, st, filepath.Join(root, "plugins"), nil, Context{DataDir: root})
	m := &Manifest{ID: "transcriber"}
	for i := 1; i <= 2; i++ {
		params := json.RawMessage(fmt.Sprintf(`{"Bvid":"BVtest","Cid":"%d","Lang":"en","Content":"part-%d"}`, i, i))
		if e = b.applyOp(context.Background(), m, op{Op: "save_subtitle", Params: params}); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := st.ListSubtitles("BVtest", 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 {
		t.Fatal(len(rows))
	}
	if rows[0].FilePath == rows[1].FilePath {
		t.Fatal("different CIDs overwrite and reference the same derived subtitle file")
	}
}
