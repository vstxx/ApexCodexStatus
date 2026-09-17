package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startTestMonitor(t *testing.T) (Monitor, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := NewTailer(MonitorOptions{Root: dir, PollInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	t.Cleanup(m.Stop)
	return m, dir
}

func collect(m Monitor, n int, wait time.Duration) []Event {
	var evs []Event
	deadline := time.After(wait)
	for len(evs) < n {
		select {
		case ev := <-m.Events():
			evs = append(evs, ev)
		case <-deadline:
			return evs
		}
	}
	return evs
}

func collectKind(m Monitor, want Kind, wait time.Duration) *Event {
	deadline := time.After(wait)
	for {
		select {
		case ev := <-m.Events():
			if ev.Kind == want {
				ev := ev
				return &ev
			}
		case <-deadline:
			return nil
		}
	}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTailerNewFileLifecycle(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-1.jsonl")

	// Meta arrives as soon as the file appears.
	writeLines(t, path, fixtureMeta)
	if ev := collectKind(m, EvSessionMeta, 5*time.Second); ev == nil {
		t.Fatal("no session_meta event for new file")
	} else if !ev.Meta.VSCode {
		t.Errorf("meta lost VSCode flag")
	}

	writeLines(t, path, fixtureTaskStarted, fixtureExecCall)
	if ev := collectKind(m, EvToolStart, 5*time.Second); ev == nil {
		t.Fatal("no tool start after append")
	}

	writeLines(t, path, fixtureTaskComplete)
	if ev := collectKind(m, EvTaskComplete, 5*time.Second); ev == nil {
		t.Fatal("no task_complete after append")
	}
}

func TestTailerPartialLine(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-2.jsonl")
	writeLines(t, path, fixtureMeta)
	collectKind(m, EvSessionMeta, 5*time.Second)

	// Append a line in two pieces without a trailing newline: the event must
	// appear only once the line is complete.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	half := len(fixtureTaskStarted) / 2
	f.WriteString(fixtureTaskStarted[:half])
	f.Close()
	time.Sleep(600 * time.Millisecond)
	if ev := collectKind(m, EvTaskStarted, 200*time.Millisecond); ev != nil {
		t.Fatal("partial line produced an event")
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(fixtureTaskStarted[half:] + "\n")
	f.Close()
	if ev := collectKind(m, EvTaskStarted, 5*time.Second); ev == nil {
		t.Fatal("completed line was not parsed")
	}
}

func TestTailerMalformedLineSkipped(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-3.jsonl")
	writeLines(t, path, fixtureMeta, "{broken json", fixtureTaskComplete)
	collectKind(m, EvSessionMeta, 5*time.Second)
	if ev := collectKind(m, EvTaskComplete, 5*time.Second); ev == nil {
		t.Fatal("malformed line broke subsequent parsing")
	}
}

func TestTailerTruncatedFileRestart(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-4.jsonl")
	writeLines(t, path, fixtureMeta, fixtureTaskStarted, fixtureTaskComplete)
	collectKind(m, EvTaskComplete, 5*time.Second)

	// File rewritten from scratch (e.g. restore): offsets must reset.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, fixtureMeta, fixtureAborted)
	if ev := collectKind(m, EvTurnAborted, 5*time.Second); ev == nil {
		t.Fatal("truncation reset was not handled")
	}
}

func TestTailerSeedsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-5.jsonl")
	writeLines(t, path, fixtureMeta, fixtureTaskStarted, fixtureTaskComplete)

	m, err := NewTailer(MonitorOptions{Root: dir, PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	var gotMeta, gotComplete bool
	deadline := time.After(5 * time.Second)
	for !(gotMeta && gotComplete) {
		select {
		case ev := <-m.Events():
			switch ev.Kind {
			case EvSessionMeta:
				gotMeta = true
			case EvTaskComplete:
				gotComplete = true
			}
		case <-deadline:
			t.Fatalf("seed incomplete: meta=%v complete=%v", gotMeta, gotComplete)
		}
	}
}

func TestSummarize(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "a.jsonl"), fixtureMeta, fixtureTaskComplete)
	sum := Summarize(dir)
	if sum.Count != 1 {
		t.Fatalf("count = %d", sum.Count)
	}
	if sum.Newest == nil || !sum.Newest.VSCode || sum.Newest.Repo != "demo" {
		t.Fatalf("newest meta = %+v", sum.Newest)
	}
}

func TestTailerRatesDeduped(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-rates.jsonl")
	writeLines(t, path, fixtureMeta, fixtureTokenCount)
	if ev := collectKind(m, EvRates, 5*time.Second); ev == nil {
		t.Fatal("first rates event missing")
	}
	// Same values again (new file with same payload): deduped away.
	path2 := filepath.Join(dir, "rollout-rates2.jsonl")
	writeLines(t, path2, fixtureMeta, fixtureTokenCount)
	time.Sleep(700 * time.Millisecond)

	// Changed values: must come through.
	changed := strings.Replace(fixtureTokenCount, `"used_percent":28.0`, `"used_percent":30.0`, 1)
	writeLines(t, path, changed)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-m.Events():
			if ev.Kind == EvRates && ev.Rates.WeeklyLeft == 70 {
				return // observed the update
			}
		case <-deadline:
			t.Fatal("changed rates never arrived")
		}
	}
}

func TestTailerLongTurnBoundarySynthesis(t *testing.T) {
	m, dir := startTestMonitor(t)
	path := filepath.Join(dir, "rollout-long.jsonl")

	// meta + task_started, then >256KB of filler so the boundary lies
	// beyond the seed window, then a live tail line.
	filler := `{"timestamp":"2026-09-17T09:00:00.000Z","ordinal":1,"type":"response_item","payload":{"type":"reasoning"}}`
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := func(s string) { f.WriteString(s + "\n") }
	w(fixtureMeta)
	w(fixtureTaskStarted)
	for i := 0; i < 3000; i++ { // ~300KB of unparseable-for-state filler
		w(filler)
	}
	w(fixtureToolOutput)
	f.Close()

	// Expect: meta, synthesized task_started (boundary beyond window), and
	// the tail tool output.
	var gotStarted, gotOutput bool
	deadline := time.After(8 * time.Second)
	for !(gotStarted && gotOutput) {
		select {
		case ev := <-m.Events():
			switch ev.Kind {
			case EvTaskStarted:
				gotStarted = true
			case EvToolOutput:
				gotOutput = true
			}
		case <-deadline:
			t.Fatalf("seed incomplete: started=%v output=%v", gotStarted, gotOutput)
		}
	}
}
