// Package render owns the 128x40 1-bit framebuffer, the bitmap font,
// status icons and the per-state OLED layouts.
package render

// W and H are the Apex 5 OLED dimensions in pixels.
const (
	W = 128
	H = 40
)

// FB is a 128x40 monochrome framebuffer, one byte per pixel (0 or 1).
// The flat slice keeps hashing and blitting trivial.
type FB struct {
	Pix [W * H]uint8
}

// Clear resets every pixel to black.
func (f *FB) Clear() {
	f.Pix = [W * H]uint8{}
}

// Set turns on a single pixel; out-of-range coordinates are ignored.
func (f *FB) Set(x, y int) {
	if x < 0 || y < 0 || x >= W || y >= H {
		return
	}
	f.Pix[y*W+x] = 1
}

// Get returns the pixel value at x,y (0 outside the frame).
func (f *FB) Get(x, y int) uint8 {
	if x < 0 || y < 0 || x >= W || y >= H {
		return 0
	}
	return f.Pix[y*W+x]
}

// InvertRect flips every pixel inside the rectangle (clipped).
func (f *FB) InvertRect(x, y, w, h int) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if xx < 0 || yy < 0 || xx >= W || yy >= H {
				continue
			}
			i := yy*W + xx
			f.Pix[i] ^= 1
		}
	}
}

// FillRect sets every pixel inside the rectangle (clipped).
func (f *FB) FillRect(x, y, w, h int) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			f.Set(xx, yy)
		}
	}
}

// HLine draws a horizontal 1px line.
func (f *FB) HLine(y int) {
	for x := 0; x < W; x++ {
		f.Set(x, y)
	}
}

// Hash returns FNV-1a over the framebuffer; used to skip identical frames.
func (f *FB) Hash() uint64 {
	h := uint64(14695981039346656037)
	for _, b := range f.Pix {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

// Blit draws a bitmap glyph: rows are packed MSB-first, width <= 8 bits.
func (f *FB) Blit(x, y int, rows []uint8, w, h int) {
	for ry := 0; ry < h && ry < len(rows); ry++ {
		for rx := 0; rx < w && rx < 8; rx++ {
			if rows[ry]&(0x80>>rx) != 0 {
				f.Set(x+rx, y+ry)
			}
		}
	}
}
