package store

import (
	"path/filepath"
	"testing"
)

func TestDurableDownloadEventAndAppendReplay(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bili-saver.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertVideo(&Video{Bvid: "BVdelivery", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertPlugin(&PluginRow{ID: "test", Enabled: true, ManifestJSON: `{"events":["video.downloaded"]}`, ConfigJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteDownload("BVdelivery", "media/a.mp4", map[string]any{"bvid": "BVdelivery"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pending, err := st.PendingDeliveries(Now(), 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("recovered: %v %v", pending, err)
	}
	var delivery int64
	for _, d := range pending {
		if d.Target == "plugin:test" {
			delivery = d.ID
		}
	}
	for i := 0; i < 2; i++ {
		if err = st.AddSummary(&Summary{Bvid: "BVdelivery", Origin: "plugin:test", Content: "same operation", DeliveryID: delivery, OpIndex: 1}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListSummaries("BVdelivery")
	if err != nil || len(rows) != 1 {
		t.Fatalf("append replay duplicated: %d %v", len(rows), err)
	}
	if err = st.FinishDelivery(delivery, nil); err != nil {
		t.Fatal(err)
	}
	pending, _ = st.PendingDeliveries(Now(), 10)
	if len(pending) != 1 || pending[0].Target != "media" {
		t.Fatal("completed delivery replayed")
	}
}
func TestDownloadAndEventFailureRollBackTogether(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertVideo(&Video{Bvid: "BVatomic", Status: "ok"})
	_, err = st.DB.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON archive_events BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteDownload("BVatomic", "media/a.mp4", map[string]any{}); err == nil {
		t.Fatal("failed event ignored")
	}
	v, _ := st.GetVideo("BVatomic")
	if v.Downloaded || v.LocalPath != "" {
		t.Fatal("visible state committed without event")
	}
}
