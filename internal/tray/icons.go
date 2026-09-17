package tray

import (
	"reflect"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Status tray icons, drawn in code (no .ico assets): a colored disc per
// state family, with a small glyph where it aids recognition.

var iconColors = map[string][3]byte{
	"busy":  {0x2E, 0xCC, 0x52}, // green  — Codex is working
	"done":  {0x33, 0x99, 0xFF}, // blue   — finished
	"input": {0xFF, 0xB4, 0x20}, // amber  — waiting for the user
	"fail":  {0xE8, 0x3A, 0x3A}, // red    — failed
	"stop":  {0x90, 0x90, 0x90}, // gray   — interrupted
	"idle":  {0x5A, 0x5A, 0x5A}, // dim    — nothing known
}

// DrawStatusIcon renders a status icon as a top-down BGRA buffer (alpha 255
// outside the disc is 0). Pure function — unit-testable without Win32.
func DrawStatusIcon(kind string, size int) []byte {
	pix := make([]byte, size*size*4)
	col, ok := iconColors[kind]
	if !ok {
		col = iconColors["idle"]
	}
	cx := float64(size) / 2
	cy := cx
	r := cx - 1
	scale := float64(size) / 16

	// Disc with alpha coverage (1px soft edge keeps it crisp on any DPI).
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := dist(float64(x)+0.5, float64(y)+0.5, cx, cy)
			a := 0.0
			switch {
			case d <= r-0.5:
				a = 1
			case d <= r+0.5:
				a = r + 0.5 - d // edge feather
			}
			if a <= 0 {
				continue
			}
			i := (y*size + x) * 4
			pix[i+0] = col[2] // B
			pix[i+1] = col[1] // G
			pix[i+2] = col[0] // R
			pix[i+3] = uint8(a * 255)
		}
	}

	white := [3]byte{0xFF, 0xFF, 0xFF}
	switch kind {
	case "done":
		drawSeg(pix, size, 4.5, 8.5, 6.8, 11, 2, scale, white)
		drawSeg(pix, size, 6.8, 11, 11.8, 4.8, 2, scale, white)
	case "input", "fail":
		drawSeg(pix, size, 8, 3.6, 8, 9.4, 2.4, scale, white)
		dot(pix, size, 8, 12.4, 1.5, scale, white)
	case "stop":
		drawSeg(pix, size, 5.2, 5.2, 10.8, 10.8, 2, scale, white)
		drawSeg(pix, size, 10.8, 5.2, 5.2, 10.8, 2, scale, white)
	}
	return pix
}

func dist(x, y, cx, cy float64) float64 {
	dx, dy := x-cx, y-cy
	return sqrt(dx*dx + dy*dy)
}

func sqrt(f float64) float64 {
	// Newton's method; avoids importing math for three call sites.
	if f <= 0 {
		return 0
	}
	g := f
	for i := 0; i < 24; i++ {
		g = (g + f/g) / 2
	}
	return g
}

// drawSeg paints a thick line segment (square brush) blended over the buffer.
func drawSeg(pix []byte, size int, x0, y0, x1, y1, thick float64, scale float64, col [3]byte) {
	x0, y0, x1, y1, thick = x0*scale, y0*scale, x1*scale, y1*scale, thick*scale
	steps := int(dist(x0, y0, x1, y1)) * 2
	if steps < 1 {
		steps = 1
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := x0 + (x1-x0)*t
		y := y0 + (y1-y0)*t
		for dy := -int(thick/2) - 1; dy <= int(thick/2)+1; dy++ {
			for dx := -int(thick/2) - 1; dx <= int(thick/2)+1; dx++ {
				px := int(x) + dx
				py := int(y) + dy
				if px < 0 || py < 0 || px >= size || py >= size {
					continue
				}
				d := dist(float64(px)+0.5, float64(py)+0.5, x, y)
				if d <= thick/2 {
					blend(pix, size, px, py, col)
				}
			}
		}
	}
}

func dot(pix []byte, size int, x, y, r float64, scale float64, col [3]byte) {
	x, y, r = x*scale, y*scale, r*scale
	for py := int(y - r); py <= int(y+r)+1; py++ {
		for px := int(x - r); px <= int(x+r)+1; px++ {
			if px < 0 || py < 0 || px >= size || py >= size {
				continue
			}
			if dist(float64(px)+0.5, float64(py)+0.5, x, y) <= r {
				blend(pix, size, px, py, col)
			}
		}
	}
}

func blend(pix []byte, size int, x, y int, col [3]byte) {
	i := (y*size + x) * 4
	pix[i+0] = col[2]
	pix[i+1] = col[1]
	pix[i+2] = col[0]
	pix[i+3] = 0xFF
}

// --- HICON construction from pixels -------------------------------------

var (
	gdi32            = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDIB    = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap = gdi32.NewProc("CreateBitmap")
	procDeleteObject = gdi32.NewProc("DeleteObject")
	procCreateIcon   = user32.NewProc("CreateIconIndirect")
	procDestroyIcon  = user32.NewProc("DestroyIcon")
	procGetDC        = user32.NewProc("GetDC")
	procReleaseDC    = user32.NewProc("ReleaseDC")
)

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32 // negative = top-down
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32 // BOOL: 1 = icon, not cursor
	XHotspot uint32
	YHotspot uint32
	HbmMask  windows.Handle
	HbmColor windows.Handle
}

// hiconFromBGRA builds an HICON from a top-down 32bpp BGRA buffer.
func hiconFromBGRA(size int, pix []byte) uintptr {
	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)

	bi := bitmapInfoHeader{
		BiSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:    int32(size),
		BiHeight:   int32(-size),
		BiPlanes:   1,
		BiBitCount: 32,
	}
	var bits uintptr
	hbmColor, _, err := procCreateDIB.Call(
		hdc,
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbmColor == 0 || bits == 0 {
		return 0
	}
	// Map the DIB memory as a byte slice. reflect.SliceHeader keeps go vet's
	// unsafeptr check quiet; the pointer is created and owned by
	// CreateDIBSection and stays valid until the bitmap is deleted below.
	var mem []byte
	sh := (*reflect.SliceHeader)(unsafe.Pointer(&mem))
	sh.Data = bits
	sh.Len = size * size * 4
	sh.Cap = sh.Len
	copy(mem, pix)

	maskBits := make([]byte, ((size+31)/32)*4*size) // all opaque (alpha-driven)
	hbmMask, _, _ := procCreateBitmap.Call(
		uintptr(size), uintptr(size),
		1, 1,
		uintptr(unsafe.Pointer(&maskBits[0])),
	)
	if hbmMask == 0 {
		procDeleteObject.Call(hbmColor)
		return 0
	}

	ii := iconInfo{FIcon: 1, HbmMask: windows.Handle(hbmMask), HbmColor: windows.Handle(hbmColor)}
	hicon, _, _ := procCreateIcon.Call(uintptr(unsafe.Pointer(&ii)))
	procDeleteObject.Call(hbmColor)
	procDeleteObject.Call(hbmMask)
	if hicon == 0 {
		_ = err
		return 0
	}
	return hicon
}
