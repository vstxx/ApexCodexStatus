package render

// FrameBytes is the packed size of one 128x40 1bpp frame.
const FrameBytes = W * H / 8 // 640

// Pack640 packs the framebuffer row-major, 8 pixels per byte, MSB first —
// the exact GameSense image-data-128x40 layout.
func (f *FB) Pack640() [FrameBytes]byte {
	var out [FrameBytes]byte
	for y := 0; y < H; y++ {
		base := y * (W / 8)
		row := y * W
		for x := 0; x < W; x++ {
			if f.Pix[row+x] != 0 {
				out[base+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	return out
}

// AppendJSONUints writes b as a JSON array of numbers into dst.
func AppendJSONUints(dst []byte, b []byte) []byte {
	dst = append(dst, '[')
	for i, v := range b {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendUint(dst, int(v))
	}
	return append(dst, ']')
}

func appendUint(dst []byte, v int) []byte {
	if v == 0 {
		return append(dst, '0')
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, buf[i:]...)
}
