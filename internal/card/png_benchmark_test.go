package card

import (
	"image"
	"image/png"
	"os"
	"testing"
)

func BenchmarkCardPNG(b *testing.B) {
	for _, name := range []string{"minimal", "dense"} {
		b.Run(name, func(b *testing.B) {
			im := renderCard(rasterFixture(name == "dense"), newFaceCache(), newIconCache())
			benchmarkPNGFile(b, func() *image.RGBA { return im })
		})
	}
}

func BenchmarkCardRenderPNG(b *testing.B) {
	for _, name := range []string{"minimal", "dense"} {
		b.Run(name, func(b *testing.B) {
			data := rasterFixture(name == "dense")
			faces, icons := newFaceCache(), newIconCache()
			benchmarkPNGFile(b, func() *image.RGBA { return renderCard(data, faces, icons) })
		})
	}
}

func benchmarkPNGFile(b *testing.B, render func() *image.RGBA) {
	f, err := os.CreateTemp(b.TempDir(), "card-*.png")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	im := render()
	if err := encodePNG(f, im); err != nil {
		b.Fatal(err)
	}
	expected := make([]byte, len(im.Pix))
	copy(expected, im.Pix)
	if _, err := f.Seek(0, 0); err != nil {
		b.Fatal(err)
	}
	decoded, err := png.Decode(f)
	if err != nil {
		b.Fatal(err)
	}
	if decoded.Bounds() != im.Bounds() {
		b.Fatalf("encoded bounds %v, want %v", decoded.Bounds(), im.Bounds())
	}
	for y := range im.Bounds().Dy() {
		for x := range im.Bounds().Dx() {
			r, g, bl, a := decoded.At(x, y).RGBA()
			i := im.PixOffset(x, y)
			if byte(r>>8) != expected[i] || byte(g>>8) != expected[i+1] ||
				byte(bl>>8) != expected[i+2] || byte(a>>8) != expected[i+3] {
				b.Fatalf("encoded pixel differs at (%d, %d)", x, y)
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := f.Seek(0, 0); err != nil {
			b.Fatal(err)
		}
		if err := encodePNG(f, render()); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	size, err := f.Seek(0, 1)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(size), "png-bytes")
}
