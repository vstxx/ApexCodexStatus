package render

import (
	"fmt"
	"time"

	"codexconnector/internal/state"
)

// Opts tunes layout output.
type Opts struct {
	ShowClock bool // show wall-clock on the idle frame
	Blanked   bool // render nothing (idle blanking)
}

// Row-1 state words: the big task-level state (working / done / …).
var stateWords = map[state.Status]string{
	state.StatusIdle:        "IDLE",
	state.StatusThinking:    "WORKING",
	state.StatusWorking:     "WORKING",
	state.StatusSearching:   "WORKING",
	state.StatusReading:     "WORKING",
	state.StatusEditing:     "WORKING",
	state.StatusExecuting:   "WORKING",
	state.StatusTesting:     "WORKING",
	state.StatusWaitingUser: "INPUT",
	state.StatusDone:        "DONE",
	state.StatusFailed:      "FAIL",
	state.StatusInterrupted: "STOP",
}

// Geometry: three rows on the 40px display — big status row, action row,
// usage row — matching the annotated reference design.
const (
	yStatus = 1  // double-height row, spans y=1..14
	yAction = 19 // 7px row
	yUsage  = 30 // 7px row
)

// Layout renders st into fb:
//
//	WORKING   31:52      <- big state word, elapsed right
//	VAST · EXEC          <- repository · what he is doing
//	5H: 28%      W: 72%  <- plan usage left
func Layout(fb *FB, st state.UIState, now time.Time, opts Opts) {
	fb.Clear()
	if opts.Blanked {
		return
	}
	layoutRows(fb, st, now, opts)
}

func layoutRows(fb *FB, st state.UIState, now time.Time, opts Opts) {
	word := stateWords[st.Status]

	// Row 1: big state word; elapsed (or clock) on the right, baseline-aligned.
	fb.DrawTextTall(1, yStatus, word)
	right := ""
	switch {
	case st.Status == state.StatusIdle && opts.ShowClock:
		right = now.Format("15:04")
	case st.Elapsed > 0:
		right = FormatElapsed(st.Elapsed)
	}
	if right != "" {
		fb.DrawTextTall(W-1-TextWidthTall(right), yStatus, right)
	}
	if st.Attention {
		fb.InvertRect(0, 0, W, GlyphTallHeight+2)
	}

	// Row 2: repository · what he is doing (the action word is frozen for
	// terminal frames so DONE keeps the full picture).
	row2 := st.Project
	if st.ActionWord != "" {
		if row2 != "" {
			row2 += " · "
		}
		row2 += st.ActionWord
	}
	if st.Detail != "" {
		if row2 != "" {
			row2 += " · "
		}
		row2 += st.Detail
	}
	if row2 != "" {
		fb.DrawTextTrunc(1, yAction, row2, W-2)
	}

	// Row 3: plan usage left.
	if st.Rates.Valid {
		fb.DrawText(1, yUsage, fmt.Sprintf("5H: %d%%", st.Rates.FiveHourLeft))
		fb.DrawText(W-1-TextWidth(fmt.Sprintf("W: %d%%", st.Rates.WeeklyLeft)), yUsage,
			fmt.Sprintf("W: %d%%", st.Rates.WeeklyLeft))
	}
}

// FormatElapsed renders durations compactly: MM:SS below an hour,
// H:MM:SS above.
func FormatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	if s < 3600 {
		return fmt.Sprintf("%02d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}
