package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"codexconnector/internal/codex"
	"codexconnector/internal/state"
)

// runInspect tails live Codex sessions and prints raw events plus the
// derived display state. Ctrl+C stops it.
func runInspect(debug bool) {
	if file := flagValue(os.Args[1:], "--replay"); file != "" {
		replayFile(file)
		return
	}
	mon, err := codex.NewTailer(codex.MonitorOptions{
		Debugf: debugf(debug, "monitor"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "inspect:", err)
		os.Exit(1)
	}
	defer mon.Stop()

	fmt.Println("Tailing Codex sessions. Waiting for activity (Ctrl+C to stop)...")
	fmt.Println()

	machine := state.NewMachine(state.DefaultMachineConfig())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	var lastShown string
	printIfChanged := func(st state.UIState, cause string) {
		key := fmt.Sprintf("%d|%s|%s|%d|%d", st.Status, st.Action, st.Detail, st.Plan.Done, int(st.Elapsed.Seconds()))
		if key == lastShown {
			return
		}
		lastShown = key
		printState2(st, cause)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var deadline <-chan time.Time
	if dur := durationFlag(os.Args[1:], "--duration", 0); dur > 0 {
		timer := time.NewTimer(dur)
		defer timer.Stop()
		deadline = timer.C
	}

	for {
		select {
		case <-sig:
			fmt.Println("\nstopped")
			return
		case <-deadline:
			fmt.Println("\nduration elapsed")
			return
		case ev := <-mon.Events():
			fmt.Println(formatEvent(ev, time.Now()))
			machine.Feed(ev, ev.Time)
			printIfChanged(machine.Snapshot(ev.Time), "event")
		case now := <-ticker.C:
			after := machine.Tick(now)
			printIfChanged(after, "tick")
		}
	}
}

func durationFlag(args []string, name string, def time.Duration) time.Duration {
	v := flagValue(args, name)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

func formatEvent(ev codex.Event, now time.Time) string {
	var b strings.Builder
	if now.Sub(ev.Time) > time.Minute {
		b.WriteString("[replay] ")
	}
	ts := ev.Time.Format("15:04:05")
	b.WriteString(ts)
	b.WriteString("  ")
	switch ev.Kind {
	case codex.EvSessionMeta:
		m := ev.Meta
		origin := "cli"
		if m.VSCode {
			origin = "vscode"
		}
		fmt.Fprintf(&b, "SESSION   %s  origin=%s repo=%q branch=%q", shortID(m.ID), origin, m.Repo, m.Branch)
	case codex.EvTaskStarted:
		b.WriteString("TASK      started")
	case codex.EvTaskComplete:
		fmt.Fprintf(&b, "TASK      complete (%s)", ev.Duration)
	case codex.EvTurnAborted:
		b.WriteString("TASK      aborted")
	case codex.EvError:
		b.WriteString("TASK      error")
	case codex.EvToolStart:
		fmt.Fprintf(&b, "TOOL      %s", ev.Tool)
		if ev.Command != "" {
			fmt.Fprintf(&b, " %q", clip(ev.Command, 70))
		}
		if len(ev.Files) > 0 {
			fmt.Fprintf(&b, " files=%v", shortFiles(ev.Files, 3))
		}
		if len(ev.Plan) > 0 {
			fmt.Fprintf(&b, " plan=%s", planCounts(ev.Plan))
		}
	case codex.EvToolOutput:
		b.WriteString("TOOL      output")
	case codex.EvItem:
		fmt.Fprintf(&b, "ITEM      %s", ev.Item)
		if ev.ItemFailed {
			fmt.Fprintf(&b, " FAILED exit=%d", ev.ExitCode)
		}
		if len(ev.Files) > 0 {
			fmt.Fprintf(&b, " files=%v", shortFiles(ev.Files, 3))
		}
	}
	return b.String()
}

func printState2(st state.UIState, cause string) {
	parts := []string{st.Status.String()}
	if st.Project != "" {
		parts = append(parts, st.Project)
	}
	if st.Action != "" {
		parts = append(parts, clip(st.Action, 40))
	}
	if st.Detail != "" {
		parts = append(parts, st.Detail)
	}
	if st.Plan.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", st.Plan.Done, st.Plan.Total))
	}
	if st.Elapsed > 0 {
		parts = append(parts, renderElapsed(st.Elapsed))
	}
	fmt.Printf("          ==> STATE [%s]: %s\n", cause, strings.Join(parts, " | "))
}

func renderElapsed(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// LF is the JSONL line separator as a Go constant.
const LF = "\n"

// replayFile prints the event + state timeline of an existing session file.
func replayFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
	machine := state.NewMachine(state.DefaultMachineConfig())
	var lastShown string
	show := func(st state.UIState, note string) {
		key := fmt.Sprintf("%d|%s|%s|%s|%d", st.Status, st.Action, st.Detail, st.Project, st.Plan.Done)
		if key == lastShown {
			return
		}
		lastShown = key
		printState2(st, note)
	}
	for _, line := range strings.Split(string(data), LF) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, ev := range codex.ParseLine([]byte(line)) {
			fmt.Println(formatEvent(ev, ev.Time))
			machine.Feed(ev, ev.Time)
			show(machine.Snapshot(ev.Time), "replay")
		}
	}
}

func debugf(enabled bool, prefix string) func(string, ...any) {
	if !enabled {
		return func(string, ...any) {}
	}
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "["+prefix+"] "+format+"\n", args...)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func shortFiles(files []string, n int) []string {
	out := make([]string, 0, len(files))
	for i, f := range files {
		if i >= n {
			out = append(out, fmt.Sprintf("+%d more", len(files)-n))
			break
		}
		if j := strings.LastIndexAny(f, `/\`); j >= 0 {
			f = f[j+1:]
		}
		out = append(out, f)
	}
	return out
}

func planCounts(steps []codex.PlanStep) string {
	done := 0
	for _, s := range steps {
		if strings.EqualFold(s.Status, "completed") {
			done++
		}
	}
	return fmt.Sprintf("%d/%d", done, len(steps))
}

// sortStringsCopy is retained for future use.
