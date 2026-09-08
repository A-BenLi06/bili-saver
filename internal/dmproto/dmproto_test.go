package dmproto

import (
	"testing"
)

// 手工构造 protobuf wire 格式，验证解析器。
func putVarint(b []byte, field int, v uint64) []byte {
	key := uint64(field)<<3 | wireVarint
	for {
		if key < 0x80 {
			b = append(b, byte(key))
			break
		}
		b = append(b, byte(key)|0x80)
		key >>= 7
	}
	for {
		if v < 0x80 {
			b = append(b, byte(v))
			break
		}
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return b
}

func putKey(b []byte, field int, wire int) []byte {
	key := uint64(field)<<3 | uint64(wire)
	for {
		if key < 0x80 {
			return append(b, byte(key))
		}
		b = append(b, byte(key)|0x80)
		key >>= 7
	}
}

func putString(b []byte, field int, s string) []byte {
	b = putKey(b, field, wireLen)
	b = append(b, byte(len(s)))
	return append(b, s...)
}

func buildElem(id int64, progress int32, mode, fontsize int32, color uint32, content string, ctime int64) []byte {
	var e []byte
	e = putVarint(e, 1, uint64(id))
	e = putVarint(e, 2, uint64(progress))
	e = putVarint(e, 3, uint64(mode))
	e = putVarint(e, 4, uint64(fontsize))
	e = putVarint(e, 5, uint64(color))
	e = putString(e, 7, content)
	e = putVarint(e, 8, uint64(ctime))
	return e
}

func TestParseSegReply(t *testing.T) {
	var reply []byte
	reply = putString(reply, 99, "unknown-field") // 未知字段应被跳过
	// 每条弹幕是嵌套在 field 1 (length-delimited) 里的 DanmakuElem 消息
	e1 := buildElem(1001, 12345, 1, 25, 0xFFFFFF, "你好弹幕", 1700000000)
	reply = putString(reply, 1, string(e1))
	e2 := buildElem(1002, 67890, 4, 18, 0xFF0000, "second <xml>&", 1700000001)
	reply = putString(reply, 1, string(e2))

	elems, err := ParseSegReply(reply)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(elems) != 2 {
		t.Fatalf("期望 2 条弹幕，得到 %d", len(elems))
	}
	if elems[0].ID != 1001 || elems[0].Progress != 12345 || elems[0].Content != "你好弹幕" {
		t.Errorf("第一条不匹配: %+v", elems[0])
	}
	if elems[0].Mode != 1 || elems[0].Fontsize != 25 || elems[0].Color != 0xFFFFFF || elems[0].Ctime != 1700000000 {
		t.Errorf("第一条字段不匹配: %+v", elems[0])
	}
	if elems[1].Content != "second <xml>&" {
		t.Errorf("第二条内容不匹配: %q", elems[1].Content)
	}
}

func TestParseSegReplyEmpty(t *testing.T) {
	elems, err := ParseSegReply([]byte{})
	if err != nil || len(elems) != 0 {
		t.Fatalf("空输入应返回空: %v %d", err, len(elems))
	}
}
