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

func TestParseSegReplyFieldEncoding(t *testing.T) {
	// A legacy group can include fields with the same numbers as an element.
	reply := putKey(nil, 15, wireGroupStart)
	reply = putString(reply, 1, "skipped")
	reply = putKey(reply, 15, wireGroupEnd)

	var elem []byte
	elem = putVarint(elem, 1, 1<<63|1) // 10-byte varint
	elem = putVarint(elem, 2, 1<<14)
	elem = putVarint(elem, 4, 127)
	elem = putVarint(elem, 6, 1<<33)
	elem = putString(elem, 2, "ignored wrong wire type")
	elem = putString(elem, 7, "first")
	elem = putString(elem, 7, "最后")
	elem = putVarint(elem, 7, 42) // Wrong wire leaves the last string intact.
	reply = putString(reply, 1, string(elem))

	got, err := ParseSegReply(reply)
	want := DanmakuElem{ID: -1<<63 + 1, Progress: 1 << 14, Fontsize: 127, Mid: 1 << 33, Content: "最后"}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("ParseSegReply() = %+v, %v, want [%+v]", got, err, want)
	}

	got, err = ParseSegReply(append(reply, 0x80))
	if err == nil || got != nil {
		t.Fatalf("truncated reply = %+v, %v, want no partial results and an error", got, err)
	}
}

func TestParseSegReplyLargeWithUnknownFields(t *testing.T) {
	const count = 400
	var reply []byte
	for i := range count {
		reply = putKey(reply, 15, wireGroupStart)
		reply = putString(reply, 1, "not an element")
		reply = putKey(reply, 15, wireGroupEnd)
		reply = putString(reply, 1, string(putVarint(nil, 1, uint64(i))))
		reply = putVarint(reply, 1, uint64(i))
	}
	got, err := ParseSegReply(reply)
	if err != nil || len(got) != count {
		t.Fatalf("parsed %d elements, want %d, error %v", len(got), count, err)
	}
	for i, element := range got {
		if element.ID != int64(i) {
			t.Fatalf("element %d: got ID %d", i, element.ID)
		}
	}

	got, err = ParseSegReply(append(reply, 0x80))
	if err == nil || got != nil {
		t.Fatalf("truncated reply returned %d elements, error %v", len(got), err)
	}
}
