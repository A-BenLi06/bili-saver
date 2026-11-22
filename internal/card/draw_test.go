package card

import (
	"image"
	"math"
	"testing"
)

func TestRoundedDistanceMatchesReference(t *testing.T) {
	for _, rect := range []image.Rectangle{
		image.Rect(0, 0, 1, 1),
		image.Rect(-4, 2, 18, 15),
		image.Rect(0, 0, 17, 30),
	} {
		halfW := float64(rect.Dx()) / 2
		halfH := float64(rect.Dy()) / 2
		for _, radius := range []float64{0, 0.5, 3.75, 8.5, 23} {
			clippedRadius := min(radius, halfW, halfH)
			for y := rect.Min.Y - 3; y < rect.Max.Y+3; y++ {
				for x := rect.Min.X - 3; x < rect.Max.X+3; x++ {
					px, py := float64(x)+0.5, float64(y)+0.5
					qx := math.Abs(px-(float64(rect.Min.X)+halfW)) - (halfW - clippedRadius)
					qy := math.Abs(py-(float64(rect.Min.Y)+halfH)) - (halfH - clippedRadius)
					dx, dy := math.Max(qx, 0), math.Max(qy, 0)
					want := math.Hypot(dx, dy) + math.Min(math.Max(qx, qy), 0) - clippedRadius
					if got := sdfRound(px, py, rect, radius); got != want {
						t.Fatalf("sdfRound(%g, %g, %v, %g) = %g, want %g", px, py, rect, radius, got, want)
					}
				}
			}
		}
	}
}
