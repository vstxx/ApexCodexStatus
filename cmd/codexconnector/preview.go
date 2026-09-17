package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codexconnector/internal/codex"
	"codexconnector/internal/preview"
	"codexconnector/internal/render"
	"codexconnector/internal/state"
)

// runPreview renders representative frames (or one --state) as scaled PNGs.
func runPreview(outDir, only string) {
	if outDir == "" {
		outDir = "preview_out"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "preview:", err)
		os.Exit(1)
	}
	now := time.Date(2026, 9, 17, 11, 42, 5, 0, time.Local)
	rates := codex.RateLimits{Valid: true, FiveHourLeft: 28, WeeklyLeft: 72}

	cases := representativeStates(now, rates)
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sortStrings(names)

	count := 0
	for _, name := range names {
		if only != "" && name != only {
			continue
		}
		fb := &render.FB{}
		opts := render.Opts{ShowClock: true}
		if name == "idleblank" {
			opts.Blanked = true // this case demonstrates the blanked frame
		}
		render.Layout(fb, cases[name], now, opts)
		path := filepath.Join(outDir, name+".png")
		if err := writePreviewPNG(path, fb, 6); err != nil {
			fmt.Fprintln(os.Stderr, "preview:", err)
			os.Exit(1)
		}
		fmt.Println(path)
		count++
	}

	if only == "" {
		writeFontSheet(outDir)
	}
	if count == 0 {
		fmt.Fprintln(os.Stderr, "preview: no states matched", only)
		os.Exit(1)
	}
}

func writePreviewPNG(path string, fb *render.FB, scale int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return preview.WritePNG(f, fb, scale)
}

// representativeStates builds one UIState per displayable situation.
func representativeStates(now time.Time, rates codex.RateLimits) map[string]state.UIState {
	return map[string]state.UIState{
		"working": {
			Status: state.StatusWorking, ActionWord: "WORK", Project: "vast",
			Action: "refactor settings loader", Elapsed: 1912 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"exec": {
			Status: state.StatusExecuting, ActionWord: "EXEC", Project: "vast",
			Action: "go build ./...", Elapsed: 28 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"thinking": {
			Status: state.StatusThinking, ActionWord: "THINK", Project: "vast",
			Elapsed: 37 * time.Second, Rates: rates, Animated: true, HasTurn: true,
		},
		"edit": {
			Status: state.StatusEditing, ActionWord: "EDIT", Project: "vast",
			Action: "settings.ts", Elapsed: 45 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"read": {
			Status: state.StatusReading, ActionWord: "READ", Project: "idu",
			Action: "rg -n StateContext src", Elapsed: 12 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"test": {
			Status: state.StatusTesting, ActionWord: "TEST", Project: "vast",
			Action: "npm run test", Elapsed: 72 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"search": {
			Status: state.StatusSearching, ActionWord: "SEARCH", Project: "vast",
			Elapsed: 20 * time.Second, Rates: rates, Animated: true, HasTurn: true,
		},
		"input": {
			Status: state.StatusWaitingUser, ActionWord: "WAIT", Project: "vast",
			Elapsed: 91 * time.Second, Rates: rates, Attention: true, HasTurn: true,
		},
		"done": {
			Status: state.StatusDone, ActionWord: "EXEC", Project: "vast",
			Elapsed: 222 * time.Second, Rates: rates,
		},
		"failed": {
			Status: state.StatusFailed, ActionWord: "TEST", Project: "vast",
			Action: "npm run test", Detail: "exit 1", Elapsed: 154 * time.Second,
			Rates: rates, Attention: true,
		},
		"stopped": {
			Status: state.StatusInterrupted, ActionWord: "EXEC", Project: "codexconnector",
			Elapsed: 61 * time.Second, Rates: rates,
		},
		"idle":      {Status: state.StatusIdle, Rates: rates},
		"idleblank": {Status: state.StatusIdle, Rates: rates},
		"long-project": {
			Status: state.StatusExecuting, ActionWord: "EXEC", Project: "super-long-project-name-here",
			Action: "very_long_filename_settings.ts", Elapsed: 3702 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
		"unicode": {
			Status: state.StatusEditing, ActionWord: "EDIT", Project: "Laczenie z Vastem",
			Action: "main_ustawienia.ts", Elapsed: 15 * time.Second,
			Rates: rates, Animated: true, HasTurn: true,
		},
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
