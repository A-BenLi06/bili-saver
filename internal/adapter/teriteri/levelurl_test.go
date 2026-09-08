package teriteri

// 用户等级契约测试：teriteri-client 用 VLevel(handleLevel(user.exp)) 渲染等级徽章，
// 适配层必须把 B 站等级映射为经验下限写入 exp；uid 全链路透出。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserLevelExpExposed(t *testing.T) {
	st := seed(t)
	// UP主 Lv6、评论者1001 Lv5、评论者1002 Lv2
	if _, err := st.DB.Exec(`UPDATE videos SET owner_level=6 WHERE bvid='BV1Test00001'`); err != nil {
		t.Fatal(err)
	}
	for rpid, level := range map[int64]int{1: 5, 2: 2} {
		if _, err := st.DB.Exec(`UPDATE comments SET member_level=? WHERE rpid=?`, level, rpid); err != nil {
			t.Fatal(err)
		}
	}
	a := &Adapter{St: st, DataDir: t.TempDir(), AdminUser: "admin", AdminPass: "pw"}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// getone：UP主 exp = Lv6 下限 28800
	_, e := get(t, srv.URL+"/api/video/getone?vid=BV1Test00001", nil)
	var d1 struct {
		User struct {
			UID int64 `json:"uid"`
			Exp int   `json:"exp"`
		} `json:"user"`
	}
	if err := json.Unmarshal(e.Data, &d1); err != nil {
		t.Fatal(err)
	}
	if d1.User.UID != 42 {
		t.Errorf("uid 应为 B 站 mid 42，实际 %d", d1.User.UID)
	}
	if d1.User.Exp != 28800 {
		t.Errorf("UP主 Lv6 exp 应为 28800，实际 %d", d1.User.Exp)
	}

	// 评论：exp 按等级取下限（Lv5→10800，Lv2→200）
	_, e = get(t, srv.URL+"/api/comment/get?vid=BV1Test00001&offset=0&type=1", nil)
	var d2 struct {
		Comments []struct {
			User struct {
				UID int64 `json:"uid"`
				Exp int   `json:"exp"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(e.Data, &d2); err != nil {
		t.Fatal(err)
	}
	got := map[int64]int{}
	for _, c := range d2.Comments {
		got[c.User.UID] = c.User.Exp
	}
	if got[1001] != 10800 {
		t.Errorf("评论者1001 Lv5 exp 应为 10800，实际 %d", got[1001])
	}
	if got[1002] != 200 {
		t.Errorf("评论者1002 Lv2 exp 应为 200，实际 %d", got[1002])
	}

	// UP主空间：exp 同样映射
	_, e = get(t, srv.URL+"/api/user/info/get-one?uid=42", nil)
	var d3 struct {
		UID int64 `json:"uid"`
		Exp int   `json:"exp"`
	}
	if err := json.Unmarshal(e.Data, &d3); err != nil {
		t.Fatal(err)
	}
	if d3.UID != 42 || d3.Exp != 28800 {
		t.Errorf("空间页 uid/exp 错误: %+v", d3)
	}
}
