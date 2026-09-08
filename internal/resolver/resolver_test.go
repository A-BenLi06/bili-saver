package resolver

import "testing"

func TestParseVideo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"BV1xx411c7mD", "BV1xx411c7mD"},
		{"https://www.bilibili.com/video/BV1GJ411x7h7/?spm_id_from=333.5", "BV1GJ411x7h7"},
		{"https://www.bilibili.com/video/av170001", "av170001"},
		{"av170001", "av170001"},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) 失败: %v", c.in, err)
			continue
		}
		if got.Type != TypeVideo || got.ExtID != c.want {
			t.Errorf("Parse(%q) = %v %v，期望 video/%v", c.in, got.Type, got.ExtID, c.want)
		}
	}
}

func TestParseSpace(t *testing.T) {
	got, err := Parse("https://space.bilibili.com/946974")
	if err != nil || got.Type != TypeSpace || got.ExtID != "946974" {
		t.Errorf("space 解析错误: %+v %v", got, err)
	}
	got, err = Parse("946974")
	if err != nil || got.Type != TypeSpace || got.ExtID != "946974" {
		t.Errorf("纯数字 mid 解析错误: %+v %v", got, err)
	}
}

func TestParseFavlist(t *testing.T) {
	got, err := Parse("https://space.bilibili.com/946974/favlist?fid=123456&ftype=create")
	if err != nil || got.Type != TypeFavList || got.ExtID != "123456" {
		t.Errorf("favlist 解析错误: %+v %v", got, err)
	}
	if got.Extra["mid"] != "946974" {
		t.Errorf("favlist extra mid 错误: %+v", got.Extra)
	}
	got, err = Parse("https://www.bilibili.com/medialist/play/789?business_space=1")
	if err != nil {
		t.Fatalf("medialist 解析失败: %v", err)
	}
	if got.Type != TypeFavList || got.ExtID != "789" {
		t.Errorf("medialist 应解析为收藏夹 789: %+v %+v", got.Type, got.ExtID)
	}
	got, err = Parse("https://www.bilibili.com/video/av170001")
	if err != nil || got.ExtID != "av170001" {
		t.Errorf("av 链接解析错误: %+v %v", got, err)
	}
}

func TestParseSeasonSeries(t *testing.T) {
	got, err := Parse("https://space.bilibili.com/546195/channel/collectiondetail?sid=1234")
	if err != nil || got.Type != TypeSeason || got.ExtID != "1234" {
		t.Errorf("collectiondetail 解析错误: %+v %v", got, err)
	}
	got, err = Parse("https://space.bilibili.com/546195/channel/seriesdetail?sid=567&mid=546195")
	if err != nil || got.Type != TypeSeries || got.ExtID != "567" {
		t.Errorf("seriesdetail 解析错误: %+v %v", got, err)
	}
	got, err = Parse("https://www.bilibili.com/lists/3929610?type=series")
	if err != nil || got.Type != TypeSeries || got.ExtID != "3929610" {
		t.Errorf("lists series 解析错误: %+v %v", got, err)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{"", "hello world", "https://example.com/video/BV1xx411c7mD"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) 应该失败", in)
		}
	}
}
