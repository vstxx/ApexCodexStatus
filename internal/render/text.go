package render

// DrawText draws sanitized text at (x,y) and returns the x position after it.
// Text is never allowed to overflow the framebuffer.
func (f *FB) DrawText(x, y int, s string) int {
	px := x
	for _, r := range Sanitize(s) {
		g, ok := font[r]
		if !ok {
			g = font['?']
		}
		f.blit5(px, y, g)
		px += GlyphWidth
		if px > W {
			return W
		}
	}
	return px
}

// TextWidth returns the rendered width of s in pixels.
func TextWidth(s string) int {
	return len([]rune(Sanitize(s))) * GlyphWidth
}

// DrawTextTrunc draws s clipped to maxWidth pixels; if it does not fit, the
// tail is replaced with an ellipsis so the ending stays visible.
func (f *FB) DrawTextTrunc(x, y int, s string, maxWidth int) {
	if TextWidth(s) <= maxWidth {
		f.DrawText(x, y, s)
		return
	}
	runes := []rune(Sanitize(s))
	const ell = "…"
	for n := len(runes); n > 0; n-- {
		cand := string(runes[:n]) + ell
		if TextWidth(cand) <= maxWidth {
			f.DrawText(x, y, cand)
			return
		}
	}
}

// TruncTail shortens s to at most maxChars runes, keeping the tail and
// prefixing an ellipsis (used for file paths where the suffix matters).
func TruncTail(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return "…" + string(r[len(r)-maxChars+1:])
}

// TruncHead shortens s to at most maxChars runes, keeping the head and
// appending an ellipsis (used for commands where the program name matters).
func TruncHead(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars-1]) + "…"
}

// blit5 draws one font glyph.
func (f *FB) blit5(x, y int, g [7]byte) {
	for ry := 0; ry < GlyphHeight; ry++ {
		for rx := 0; rx < 5; rx++ {
			if g[ry]&(0x80>>rx) != 0 {
				f.Set(x+rx, y+ry)
			}
		}
	}
}

// GlyphTallWidth is the advance of one double-height glyph (5px wide + 2px
// gap — tall-narrow, matching the reference display).
const GlyphTallWidth = 7

// GlyphTallHeight is the height of a double-height glyph (7*2).
const GlyphTallHeight = 14

// DrawTextTall draws double-height text (glyphs stretched vertically) at
// (x,y) and returns the x position after it. Never overflows.
func (f *FB) DrawTextTall(x, y int, s string) int {
	px := x
	for _, r := range Sanitize(s) {
		g, ok := font[r]
		if !ok {
			g = font['?']
		}
		for ry := 0; ry < GlyphHeight; ry++ {
			for rx := 0; rx < 5; rx++ {
				if g[ry]&(0x80>>rx) != 0 {
					f.Set(px+rx, y+ry*2)
					f.Set(px+rx, y+ry*2+1)
				}
			}
		}
		px += GlyphTallWidth
		if px > W {
			return W
		}
	}
	return px
}

// TextWidthTall returns the rendered width of s at double height.
func TextWidthTall(s string) int {
	return len([]rune(Sanitize(s))) * GlyphTallWidth
}
