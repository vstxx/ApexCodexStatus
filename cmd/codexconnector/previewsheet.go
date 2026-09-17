package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"codexconnector/internal/render"
)

// fontContactSheetPNG writes an enlarged sheet of every font glyph.
func fontContactSheetPNG(path string) error {
	runes := []rune(" ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~…×●✓·")
	cols := 16
	rows := (len(runes) + cols - 1) / cols
	scale := 8
	cell := 8 * scale // glyph cell 8px at scale

	img := image.NewRGBA(image.Rect(0, 0, cols*cell, rows*cell))
	bg := color.RGBA{R: 0x10, G: 0x10, B: 0x12, A: 0xFF}
	on := color.RGBA{R: 0x30, G: 0xD1, B: 0x58, A: 0xFF}
	for y := 0; y < rows*cell; y++ {
		for x := 0; x < cols*cell; x++ {
			img.Set(x, y, bg)
		}
	}
	for i, r := range runes {
		g, _ := render.Glyph(r)
		cx := (i % cols) * cell
		cy := (i / cols) * cell
		for ry := 0; ry < 7; ry++ {
			for rx := 0; rx < 5; rx++ {
				if g[ry]&(0x80>>rx) != 0 {
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							img.Set(cx+rx*scale+dx, cy+ry*scale+dy, on)
						}
					}
				}
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func writeFontSheet(outDir string) {
	path := filepath.Join(outDir, "font-sheet.png")
	if err := fontContactSheetPNG(path); err != nil {
		fmt.Fprintln(os.Stderr, "preview:", err)
		os.Exit(1)
	}
	fmt.Println(path)
}
