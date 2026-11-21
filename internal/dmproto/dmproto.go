// Package dmproto 解析 B 站弹幕分段 protobuf（DmSegMobileReply）。
// 手写 wire-format 解析，避免 protobuf 代码生成依赖；只解码已知字段号，
// 未知字段按 wire type 跳过。原始字节仍会原样落盘（origin 层快照）。
package dmproto

import (
	"errors"
	"fmt"
)

// DanmakuElem 对应 DanmakuElem 消息的常用字段。
type DanmakuElem struct {
	ID       int64  // field 1: dmid
	Progress int32  // field 2: 出现时间 ms
	Mode     int32  // field 3: 1-5 滚动/固定等
	Fontsize int32  // field 4
	Color    uint32 // field 5
	Mid      int64  // field 6 (哈希/uid)
	Content  string // field 7
	Ctime    int64  // field 8: 发送时间戳
}

// wire types
const (
	wireVarint     = 0
	wireI64        = 1
	wireLen        = 2
	wireGroupStart = 3
	wireGroupEnd   = 4
	wireI32        = 5
)

// ParseSegReply 解析 DmSegMobileReply（repeated DanmakuElem elem = 1）。
func ParseSegReply(data []byte) ([]DanmakuElem, error) {
	var out []DanmakuElem
	err := walkFields(data, func(key uint64, value []byte, num uint64) error {
		if key == 1<<3|wireLen {
			elem, err := parseElem(value)
			if err != nil {
				return err
			}
			out = append(out, elem)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseElem(b []byte) (DanmakuElem, error) {
	var e DanmakuElem
	err := walkFields(b, func(key uint64, value []byte, num uint64) error {
		switch key {
		case 1<<3 | wireVarint:
			e.ID = int64(num)
		case 2<<3 | wireVarint:
			e.Progress = int32(num)
		case 3<<3 | wireVarint:
			e.Mode = int32(num)
		case 4<<3 | wireVarint:
			e.Fontsize = int32(num)
		case 5<<3 | wireVarint:
			e.Color = uint32(num)
		case 6<<3 | wireVarint:
			e.Mid = int64(num)
		case 7<<3 | wireLen:
			e.Content = string(value)
		case 8<<3 | wireVarint:
			e.Ctime = int64(num)
		}
		return nil
	})
	return e, err
}

// walkFields 遍历 message 的顶层字段。
// value 只在 wireLen 时有效；num 只在 varint 时有效。
func walkFields(b []byte, fn func(key uint64, value []byte, num uint64) error) error {
	pos := 0
	for pos < len(b) {
		// Most field keys fit in one byte. The loop already checked its bounds.
		key := uint64(b[pos])
		if key < 0x80 {
			pos++
		} else {
			var err error
			key, pos, err = readVarint(b, pos)
			if err != nil {
				return err
			}
		}
		switch key & 7 {
		case wireVarint:
			num, next, err := readVarint(b, pos)
			if err != nil {
				return err
			}
			pos = next
			if err := fn(key, nil, num); err != nil {
				return err
			}
		case wireLen:
			l, next, err := readVarint(b, pos)
			if err != nil {
				return err
			}
			pos = next
			end := pos + int(l)
			if end > len(b) || end < pos {
				return errors.New("dmproto: 长度字段越界")
			}
			if err := fn(key, b[pos:end], 0); err != nil {
				return err
			}
			pos = end
		case wireI64:
			if pos+8 > len(b) {
				return errors.New("dmproto: i64 越界")
			}
			pos += 8
			if err := fn(key, nil, 0); err != nil {
				return err
			}
		case wireI32:
			if pos+4 > len(b) {
				return errors.New("dmproto: i32 越界")
			}
			pos += 4
			if err := fn(key, nil, 0); err != nil {
				return err
			}
		case wireGroupStart: // 遗留 group：跳过到匹配的 group end
			end, err := skipGroup(b, pos, int(key>>3))
			if err != nil {
				return err
			}
			pos = end
		case wireGroupEnd:
			return errors.New("dmproto: 不配对的 group end")
		default:
			return fmt.Errorf("dmproto: 未知 wire type %d", key&7)
		}
	}
	return nil
}

// skipGroup 跳过 wire type 3 的 group 内容，返回 group end 之后的位置。
func skipGroup(b []byte, pos, field int) (int, error) {
	for pos < len(b) {
		key, next, err := readVarint(b, pos)
		if err != nil {
			return 0, err
		}
		pos = next
		f := int(key >> 3)
		w := int(key & 7)
		switch w {
		case wireVarint:
			_, next, err := readVarint(b, pos)
			if err != nil {
				return 0, err
			}
			pos = next
		case wireLen:
			l, next, err := readVarint(b, pos)
			if err != nil {
				return 0, err
			}
			pos = next + int(l)
			if pos > len(b) {
				return 0, errors.New("dmproto: group 内长度越界")
			}
		case wireI64:
			pos += 8
			if pos > len(b) {
				return 0, errors.New("dmproto: group 内 i64 越界")
			}
		case wireI32:
			pos += 4
			if pos > len(b) {
				return 0, errors.New("dmproto: group 内 i32 越界")
			}
		case wireGroupStart:
			end, err := skipGroup(b, pos, f)
			if err != nil {
				return 0, err
			}
			pos = end
		case wireGroupEnd:
			if f != field {
				return 0, errors.New("dmproto: group end 字段号不匹配")
			}
			return pos, nil
		default:
			return 0, fmt.Errorf("dmproto: group 内未知 wire type %d", w)
		}
	}
	return 0, errors.New("dmproto: group 未闭合")
}

func readVarint(b []byte, pos int) (uint64, int, error) {
	var v uint64
	var shift uint
	for i := 0; i < 10; i++ {
		if pos >= len(b) {
			return 0, pos, errors.New("dmproto: varint 越界")
		}
		c := b[pos]
		pos++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, pos, nil
		}
		shift += 7
	}
	return 0, pos, errors.New("dmproto: varint 过长")
}
