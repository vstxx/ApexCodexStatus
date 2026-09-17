// Package preview renders framebuffers to PNG for offline layout work.
package preview

import (
	"image"
	"image/color"
	"image/png"
	"io"

	"codexconnector/internal/render"
)

// WritePNG scales fb by the integer factor scale (1 = native 128x40) with
// no smoothing, and writes a PNG.
func WritePNG(w io.Writer, fb *render.FB, scale int) error {
	if scale < 1 {
		scale = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, render.W*scale, render.H*scale))
	on := color.RGBA{R: 0x30, G: 0xD1, B: 0x58, A: 0xFF} // OLED-ish green-white
	off := color.RGBA{R: 0x10, G: 0x10, B: 0x12, A: 0xFF}
	for y := 0; y < render.H; y++ {
		for x := 0; x < render.W; x++ {
			c := off
			if fb.Get(x, y) != 0 {
				c = on
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	return png.Encode(w, img)
}
