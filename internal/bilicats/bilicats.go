// Package bilicats B 站官方分区映射表（tid → 分区现名 / 一级分区名）。
//
// 数据来源：x/web-interface/ranking/v2 各主分区排行榜实测收割（2026-09-09，
// 带 cookie 环境实测，共覆盖 16 个库内出现过的分区）。分区树会被 B 站调整
// （例：76 现为"美食制作"而非旧表"美食圈"，173 现为"桌游棋牌"），因此本表
// 只收录经实测确认的条目；未收录的 tid 返回空串——宁缺毋滥，错误名称会静默
// 错路由。新增条目：从排行接口核实后补一行即可。
package bilicats

type Cat struct {
	Name    string // 分区现名（视频所属分区）
	Primary string // 所属一级分区名
}

var table = map[int]Cat{
	21:  {"日常", "生活"},
	65:  {"网络游戏", "游戏"},
	76:  {"美食制作", "美食"},
	86:  {"特摄", "动画"},
	171: {"电子竞技", "游戏"},
	172: {"手机游戏", "游戏"},
	173: {"桌游棋牌", "游戏"},
	183: {"影视剪辑", "影视"},
	184: {"预告·资讯", "影视"},
	193: {"MV", "音乐"},
	212: {"美食侦探", "美食"},
	218: {"喵星人", "动物圈"},
	221: {"野生动物", "动物圈"},
	228: {"人文历史", "知识"},
	249: {"足球", "运动"},
}

// Name 分区现名；未收录返回空串。
func Name(tid int) string { return table[tid].Name }

// Primary 所属一级分区名；未收录返回空串。
func Primary(tid int) string { return table[tid].Primary }

// Names 返回该 tid 的全部候选分区名（分区现名 + 一级分区名，去空去重），
// 供分区名路由匹配使用。
func Names(tid int) []string {
	var out []string
	for _, s := range []string{table[tid].Name, table[tid].Primary} {
		if s != "" && !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
