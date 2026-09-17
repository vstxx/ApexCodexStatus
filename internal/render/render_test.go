package render

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"codexconnector/internal/state"
)

func TestFramebufferDimensions(t *testing.T) {
	fb := &FB{}
	if len(fb.Pix) != W*H {
		t.Fatalf("pix len = %d", len(fb.Pix))
	}
	if FrameBytes != 640 {
		t.Fatalf("frame bytes = %d", FrameBytes)
	}
	// Out-of-range writes must not panic.
	fb.Set(-1, 0)
	fb.Set(W, 0)
	fb.Set(0, -1)
	fb.Set(0, H)
	fb.Set(0, 0)
	if fb.Get(0, 0) != 1 || fb.Get(W-1, H-1) == 1 {
		t.Errorf("set/get inconsistent")
	}
}

func TestPack640BitLayout(t *testing.T) {
	fb := &FB{}
	// First pixel of row 0 = MSB of byte 0.
	fb.Set(0, 0)
	// Eighth pixel = LSB of byte 0.
	fb.Set(7, 0)
	// Ninth pixel = MSB of byte 1.
	fb.Set(8, 0)
	// Last pixel of frame = LSB of last byte.
	fb.Set(W-1, H-1)
	packed := fb.Pack640()
	if packed[0] != 0x81 {
		t.Errorf("byte0 = %08b, want 10000001", packed[0])
	}
	if packed[1] != 0x80 {
		t.Errorf("byte1 = %08b", packed[1])
	}
	if packed[639] != 0x01 {
		t.Errorf("byte639 = %08b", packed[639])
	}
	for i, b := range packed {
		if i == 0 || i == 1 || i == 639 {
			continue
		}
		if b != 0 {
			t.Errorf("byte %d unexpectedly set", i)
		}
	}
}

func TestTextNeverOverflows(t *testing.T) {
	fb := &FB{}
	end := fb.DrawText(-5, 0, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if end > W {
		t.Errorf("draw end = %d", end)
	}
	fb.DrawTextTrunc(0, 8, "SERALLY LONG TEXT THAT FAR EXCEEDS THE DISPLAY WIDTH ONE TWO THREE", W-2)
	// Truncation must respect the reserved margin (layouts keep a margin).
	// Only this line's rows are checked; the clip test above legitimately
	// reaches the edge.
	for y := 8; y < 8+GlyphHeight; y++ {
		if fb.Get(W-2, y) != 0 || fb.Get(W-1, y) != 0 {
			t.Fatalf("overflow at right edge (y=%d)", y)
		}
	}
}

func TestTruncHelpers(t *testing.T) {
	if got := TruncTail("very_long_filename_settings.ts", 12); got != "…settings.ts" {
		t.Errorf("TruncTail = %q", got)
	}
	if got := TruncTail("short.ts", 12); got != "short.ts" {
		t.Errorf("TruncTail = %q", got)
	}
	if got := TruncHead("npm run test:coverage -- --watch", 10); len([]rune(got)) != 10 || got[len(got)-3:] != "…" {
		t.Errorf("TruncHead = %q", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("Łączenie settings"); got != "LACZENIE SETTINGS" {
		t.Errorf("Sanitize = %q", got)
	}
	if got := Sanitize("spać ✓ …"); got != "SPAC ✓ …" {
		t.Errorf("Sanitize = %q", got)
	}
}

func TestHashChangesWithContent(t *testing.T) {
	a := &FB{}
	b := &FB{}
	b.Set(0, 0)
	if a.Hash() == b.Hash() {
		t.Errorf("hash ignores content")
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                  "00:00",
		65 * time.Second:   "01:05",
		3702 * time.Second: "1:01:42",
		-5 * time.Second:   "00:00",
	}
	for in, want := range cases {
		if got := FormatElapsed(in); got != want {
			t.Errorf("FormatElapsed(%v) = %q, want %q", in, got, want)
		}
	}
}

// Golden frames pin the exact framebuffer output of representative states.
// Regenerate with: GOLDEN_UPDATE=1 go test ./internal/render/
func TestGoldenFrames(t *testing.T) {
	now := time.Date(2026, 9, 17, 11, 42, 5, 0, time.UTC)
	cases := goldenStates(now)
	for name, st := range cases {
		fb := &FB{}
		opts := Opts{ShowClock: true}
		if name == "idleblank" {
			opts.Blanked = true
		}
		Layout(fb, st, now, opts)
		packed := fb.Pack640()
		path := filepath.Join("testdata", name+".golden")
		if os.Getenv("GOLDEN_UPDATE") == "1" {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(encodeGolden(packed)), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("golden %s missing (run with GOLDEN_UPDATE=1): %v", name, err)
		}
		if got := encodeGolden(packed); got != string(data) {
			t.Errorf("golden %s mismatch", name)
		}
	}
}

func encodeGolden(b [FrameBytes]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hexdigits[v>>4], hexdigits[v&0xf])
	}
	return string(out)
}

func goldenStates(now time.Time) map[string]state.UIState {
	return map[string]state.UIState{
		"working": {
			Status: state.StatusWorking, ActionWord: "WORK", Project: "vast-browser",
			Action: "refactor settings loader", Elapsed: 102 * time.Second,
			Plan: state.Plan{Done: 2, Total: 5}, Animated: true, HasTurn: true,
		},
		"exec": {
			Status: state.StatusExecuting, ActionWord: "EXEC", Project: "codexconnector",
			Action: "go build ./...", Elapsed: 28 * time.Second,
			Animated: true, HasTurn: true,
		},
		"input": {
			Status: state.StatusWaitingUser, ActionWord: "WAIT", Project: "vast-browser",
			Elapsed: 91 * time.Second, Attention: true, HasTurn: true,
		},
		"done": {
			Status: state.StatusDone, ActionWord: "EXEC", Project: "vast-browser",
			Elapsed: 222 * time.Second, Plan: state.Plan{Done: 5, Total: 5},
		},
		"failed": {
			Status: state.StatusFailed, ActionWord: "TEST", Project: "vast-browser",
			Action: "npm run test", Detail: "exit 1", Elapsed: 154 * time.Second,
			Attention: true,
		},
		"idle":      {Status: state.StatusIdle},
		"idleblank": {Status: state.StatusIdle},
	}
}
