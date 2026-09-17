package tray

import "testing"

func TestDrawStatusIconLayout(t *testing.T) {
	const size = 16
	for _, kind := range []string{"busy", "done", "input", "fail", "stop", "idle"} {
		pix := DrawStatusIcon(kind, size)
		if len(pix) != size*size*4 {
			t.Fatalf("%s: buffer = %d", kind, len(pix))
		}
		at := func(x, y int) [4]byte {
			i := (y*size + x) * 4
			return [4]byte{pix[i], pix[i+1], pix[i+2], pix[i+3]}
		}
		// Corner must be fully transparent; disc center fully opaque.
		if a := at(0, 0); a[3] != 0 {
			t.Errorf("%s: corner alpha = %d, want 0", kind, a[3])
		}
		if a := at(8, 8); a[3] != 0xFF {
			t.Errorf("%s: center alpha = %d, want 255", kind, a[3])
		}
	}
	// Colors: busy is green (G dominant), fail is red (R dominant). Sampled
	// off the glyphs (x=4 is disc-only on both).
	busy := DrawStatusIcon("busy", 16)
	fail := DrawStatusIcon("fail", 16)
	c := func(p []byte) (r, g, b byte) { i := (8*16 + 4) * 4; return p[i+2], p[i+1], p[i] } // BGRA
	bb, bg, br := c(busy)
	if !(bg > br && bg > bb) {
		t.Errorf("busy disc not green-dominant: %d %d %d", bb, bg, br)
	}
	fr, fg, fb := c(fail)
	if !(fr > fg && fr > fb) {
		t.Errorf("fail disc not red-dominant: %d %d %d", fr, fg, fb)
	}
	// Glyph: done has white pixels on the check stroke (lower-left arm).
	done := DrawStatusIcon("done", 16)
	if a := at4(done, 7, 10); a != 0xFF {
		t.Errorf("done check arm alpha = %d, want 255", a)
	}
}

func at4(pix []byte, x, y int) byte {
	return pix[(y*16+x)*4+3]
}
