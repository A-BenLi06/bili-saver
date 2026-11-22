package card

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodePNGConcurrentReuse(t *testing.T) {
	// First force a write failure. A later encode may reuse that encoder.
	failed, err := os.CreateTemp(t.TempDir(), "closed-*.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encodePNG(failed, image.NewRGBA(image.Rect(0, 0, 31, 47))); err == nil {
		t.Fatal("encoding to a closed file unexpectedly succeeded")
	}

	cases := []struct {
		name string
		rect image.Rectangle
		fill color.RGBA
	}{
		{"opaque", image.Rect(0, 0, 53, 27), color.RGBA{R: 51, G: 116, B: 27, A: 255}},
		{"translucent", image.Rect(-2, 4, 29, 17), color.RGBA{R: 51, G: 84, B: 9, A: 170}},
		{"small", image.Rect(2, -3, 5, 5), color.RGBA{R: 30, G: 51, B: 91, A: 255}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			im := image.NewRGBA(tc.rect)
			draw.Draw(im, tc.rect, image.NewUniform(tc.fill), image.Point{}, draw.Src)

			var expected bytes.Buffer
			encoder := png.Encoder{CompressionLevel: png.BestSpeed}
			if err := encoder.Encode(&expected, im); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "card.png")
			for range 3 {
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				encodeErr, closeErr := encodePNG(f, im), f.Close()
				if encodeErr != nil || closeErr != nil {
					t.Fatalf("encode: %v, close: %v", encodeErr, closeErr)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, expected.Bytes()) {
					t.Fatal("pooled encoding differs from a fresh encoder")
				}
			}
		})
	}
}
