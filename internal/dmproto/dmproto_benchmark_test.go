package dmproto

import (
	"encoding/binary"
	"strings"
	"testing"
)

func BenchmarkParseSegReply(b *testing.B) {
	for _, scenario := range []struct {
		name         string
		elements     int
		includeExtra bool
	}{
		{name: "Dense", elements: 1500},
		{name: "Short", elements: 8},
		{name: "Mixed", elements: 256, includeExtra: true},
		{name: "UnknownOnly", includeExtra: true},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			var reply []byte
			if scenario.includeExtra {
				for range 512 {
					reply = putString(reply, 99, "unknown")
				}
			}
			for i := range scenario.elements {
				content := "你好弹幕"
				if scenario.includeExtra && i%4 == 0 {
					content = strings.Repeat("弹幕", 18)
				}
				element := buildElem(170000000000000000+int64(i), int32(240*i), 1, 25, 0xFFFFFF, content, 1700000000)
				element = putVarint(element, 12, 1)
				reply = putKey(reply, 1, wireLen)
				reply = binary.AppendUvarint(reply, uint64(len(element)))
				reply = append(reply, element...)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				elements, err := ParseSegReply(reply)
				if err != nil || len(elements) != scenario.elements {
					b.Fatalf("parsed %d elements, want %d, error %v", len(elements), scenario.elements, err)
				}
			}
		})
	}
}
