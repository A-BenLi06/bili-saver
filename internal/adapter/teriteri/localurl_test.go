package teriteri

// 图片 URL 本地化改写测试：本地文件存在时 coverUrl/avatar_url 必须指向 /media，
// 缺失时回退 CDN 原始 URL（B 站图床防盗链，前端无法直接加载）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeLocalImage(t *testing.T, dataDir, rel string) {
	t.Helper()
	full := filepath.Join(dataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, make([]byte, 128), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImageURLsPreferLocalFiles(t *testing.T) {
	st := seed(t)
	dataDir := t.TempDir()
	a := &Adapter{St: st, DataDir: dataDir, AdminUser: "admin", AdminPass: "pw"}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 本地化封面 + UP主(42)与评论者(1001)头像；1002 不落盘用于验证回退
	writeLocalImage(t, dataDir, "media/BV1Test00001/cover.jpg")
	writeLocalImage(t, dataDir, "media/faces/42.jpg")
	writeLocalImage(t, dataDir, "media/faces/1001.jpg")

	// getone：封面与 UP主头像走本地
	_, e := get(t, srv.URL+"/api/video/getone?vid=BV1Test00001", nil)
	var d1 struct {
		Video struct {
			CoverUrl string `json:"coverUrl"`
		} `json:"video"`
		User struct {
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
	}
	if err := json.Unmarshal(e.Data, &d1); err != nil {
		t.Fatal(err)
	}
	if d1.Video.CoverUrl != "/media/media/BV1Test00001/cover.jpg" {
		t.Errorf("coverUrl 应指向本地封面，实际 %q", d1.Video.CoverUrl)
	}
	if d1.User.AvatarURL != "/media/media/faces/42.jpg" {
		t.Errorf("UP主 avatar_url 应指向本地头像，实际 %q", d1.User.AvatarURL)
	}

	// 评论：1001 本地、1002 回退 CDN
	_, e = get(t, srv.URL+"/api/comment/get?vid=BV1Test00001&offset=0&type=1", nil)
	var d2 struct {
		Comments []struct {
			User struct {
				UID       int64  `json:"uid"`
				AvatarURL string `json:"avatar_url"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(e.Data, &d2); err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, c := range d2.Comments {
		got[c.User.UID] = c.User.AvatarURL
	}
	if got[1001] != "/media/media/faces/1001.jpg" {
		t.Errorf("评论者1001 应走本地头像，实际 %q", got[1001])
	}
	if got[1002] != "https://i0.hdslb.com/face2.jpg" {
		t.Errorf("评论者1002 缺本地文件应回退 CDN，实际 %q", got[1002])
	}

	// 全部缺失时 getone 回退 CDN 原值
	v, err := st.GetVideo("BV1Test00001")
	if err != nil {
		t.Fatal(err)
	}
	a2 := &Adapter{St: st, DataDir: t.TempDir(), AdminUser: "admin", AdminPass: "pw"}
	if u := a2.coverURL(v); u != "https://i0.hdslb.com/cover.jpg" {
		t.Errorf("无本地文件时 coverUrl 应回退 CDN，实际 %q", u)
	}
}
