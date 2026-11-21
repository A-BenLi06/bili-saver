package archivepath

import "testing"

func BenchmarkKey(b *testing.B) {
	for _, tc := range []struct {
		name  string
		root  string
		value string
		want  string
	}{
		{"Portable", "/var/bili/data", "media/BV0001/p1/video.mp4", "media/BV0001/p1/video.mp4"},
		{"WindowsRoot", `C:\archives\bili\data`, `C:\archives\bili\data\derived\BV0001\zh.bcc`, "derived/BV0001/zh.bcc"},
		{"Relocated", "/var/bili/data", `D:\old\data\raw\BV0001\detail.json.gz`, "raw/BV0001/detail.json.gz"},
		{"Clean", "/var/bili/data", "./data/media//BV0001/./p1/video.mp4", "media/BV0001/p1/video.mp4"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				got, err := Key(tc.root, tc.value)
				if err != nil || got != tc.want {
					b.Fatalf("Key(%q, %q) = %q, %v; want %q", tc.root, tc.value, got, err, tc.want)
				}
			}
		})
	}
}
