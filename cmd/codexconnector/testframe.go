package main

import (
	"fmt"
	"time"

	"codexconnector/internal/render"
	"codexconnector/internal/steelseries"
)

// runTestFrame sends a sequence of known frames to the Apex 5 OLED so the
// hardware path can be verified by eye. Frames persist (GameSense keeps the
// last frame), so the sequence ends on an informative test card.
func runTestFrame(debug bool) {
	client := steelseries.NewClient()
	if _, err := client.EnsureReady(time.Now()); err != nil {
		fmt.Fprintln(osStderr(), "testframe:", err)
		fmt.Fprintln(osStderr(), "Is SteelSeries GG running?")
		osExit(1)
		return
	}
	fmt.Println("GameSense endpoint:", client.Endpoint())
	fmt.Println("Sending test sequence to the Apex 5 OLED...")

	sequence := []struct {
		name string
		fb   *render.FB
		hold time.Duration
	}{
		{"all-white", allWhite(), 2500 * time.Millisecond},
		{"all-black", steelseries.ClearFrame(), 1500 * time.Millisecond},
		{"checkerboard-4px", checkerboard(4), 2500 * time.Millisecond},
		{"test-card", testCard(), 12 * time.Second},
	}
	for _, step := range sequence {
		if err := client.Send(step.fb, time.Now()); err != nil {
			fmt.Fprintln(osStderr(), "testframe:", step.name, "failed:", err)
			osExit(1)
			return
		}
		fmt.Printf("  %-18s shown for %s\n", step.name, step.hold)
		time.Sleep(step.hold)
	}
	_ = client.Heartbeat()

	fmt.Println()
	fmt.Println("Check the keyboard now; the test card stays on screen.")
	fmt.Println("Verify: full border visible, diagonals reach all four corners,")
	fmt.Println("text is readable and not mirrored, checkerboard is even.")
}

func allWhite() *render.FB {
	fb := &render.FB{}
	fb.FillRect(0, 0, render.W, render.H)
	return fb
}

func checkerboard(cell int) *render.FB {
	fb := &render.FB{}
	for y := 0; y < render.H; y++ {
		for x := 0; x < render.W; x++ {
			if ((x/cell)+(y/cell))%2 == 0 {
				fb.Set(x, y)
			}
		}
	}
	return fb
}

// testCard exercises edges, orientation and text in one persistent frame.
func testCard() *render.FB {
	fb := &render.FB{}
	// Border.
	for x := 0; x < render.W; x++ {
		fb.Set(x, 0)
		fb.Set(x, render.H-1)
	}
	for y := 0; y < render.H; y++ {
		fb.Set(0, y)
		fb.Set(render.W-1, y)
	}
	// Corner triangles (6x6) reveal mirroring and byte order instantly.
	for i := 0; i < 6; i++ {
		for j := 0; j <= i; j++ {
			fb.Set(2+i, 2+j)                   // top-left
			fb.Set(render.W-3-i, 2+j)          // top-right
			fb.Set(2+i, render.H-3-j)          // bottom-left
			fb.Set(render.W-3-i, render.H-3-j) // bottom-right
		}
	}
	fb.DrawText(12, 2, "CODEX CONNECTOR")
	fb.DrawText(12, 12, "OLED TEST 128X40")
	fb.DrawText(12, 22, "IF YOU READ THIS")
	fb.DrawText(12, 31, "PIXELS ARE FINE")
	return fb
}
