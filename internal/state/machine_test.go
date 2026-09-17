package state

import (
	"testing"
	"time"

	"codexconnector/internal/codex"
)

var base = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

func newMachine() *Machine {
	return NewMachine(DefaultMachineConfig())
}

func metaEvent(id string, vscode bool) codex.Event {
	return codex.Event{
		SessionID: id,
		Time:      base,
		Kind:      codex.EvSessionMeta,
		Meta:      &codex.SessionMeta{ID: id, Repo: "demo", VSCode: vscode},
	}
}

func ev(id string, kind codex.Kind, sec int) codex.Event {
	return codex.Event{SessionID: id, Time: at(sec), Kind: kind}
}

func toolEv(id string, tool codex.Tool, sec int) codex.Event {
	return codex.Event{SessionID: id, Time: at(sec), Kind: codex.EvToolStart, Tool: tool, Command: "npm run test"}
}

func TestTurnLifecycle(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 5), at(5))
	st := m.Snapshot(at(6))
	if st.Status != StatusThinking || !st.HasTurn {
		t.Fatalf("after start: %+v", st)
	}
	if st.Project != "demo" {
		t.Errorf("project = %q", st.Project)
	}
	m.Feed(codex.Event{SessionID: "s1", Time: at(60), Kind: codex.EvToolStart, Tool: codex.ToolExec, Command: "make"}, at(60))
	complete := ev("s1", codex.EvTaskComplete, 65)
	complete.Duration = time.Minute // real duration reported by Codex
	m.Feed(complete, at(65))
	st = m.Snapshot(at(66))
	if st.Status != StatusDone {
		t.Fatalf("after complete: %+v", st)
	}
	if st.Elapsed != time.Minute {
		t.Errorf("elapsed = %s, want 1m (real duration)", st.Elapsed)
	}
	if st.ActionWord != "EXEC" {
		t.Errorf("frozen action word = %q, want EXEC", st.ActionWord)
	}
	// DONE persists far beyond any hold while nothing new happens.
	st = m.Tick(at(66 + 3600))
	if st.Status != StatusDone || st.Project != "demo" {
		t.Fatalf("done did not persist: %+v", st)
	}
	if st.Elapsed != time.Minute {
		t.Errorf("persisted elapsed changed: %+v", st)
	}
	if st.ActionWord != "EXEC" {
		t.Errorf("persisted action word = %q", st.ActionWord)
	}
	// A new task in the same session flips it back to WORKING.
	m.Feed(ev("s1", codex.EvTaskStarted, 4000), at(4000))
	st = m.Snapshot(at(4001))
	if st.Status != StatusThinking {
		t.Errorf("new turn after done: %+v", st)
	}
}

func TestExecutingAndTesting(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", false), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	m.Feed(codex.Event{SessionID: "s1", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolExec, Command: "go build ./..."}, at(2))
	st := m.Snapshot(at(3))
	if st.Status != StatusExecuting || st.Action != "go build ./..." {
		t.Fatalf("exec: %+v", st)
	}
	// A read-classified command switches to READING.
	m.Feed(codex.Event{SessionID: "s1", Time: at(10), Kind: codex.EvToolStart, Tool: codex.ToolRead, Command: "git status"}, at(10))
	m.Snapshot(at(10)) // first sight: held by the debouncer
	st = m.Tick(at(11))
	if st.Status != StatusReading {
		t.Errorf("read: %+v", st)
	}
	// Test command -> TESTING.
	m.Feed(codex.Event{SessionID: "s1", Time: at(20), Kind: codex.EvToolStart, Tool: codex.ToolTest, Command: "npm test"}, at(20))
	m.Snapshot(at(20)) // held
	st = m.Tick(at(21))
	if st.Status != StatusTesting {
		t.Errorf("test: %+v", st)
	}
}

func TestWaitingUserPersists(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	m.Feed(codex.Event{SessionID: "s1", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolUserInput}, at(2))
	st := m.Snapshot(at(2))
	if st.Status != StatusWaitingUser || !st.Attention {
		t.Fatalf("waiting: %+v", st)
	}
	// Long quiet period must not kill WAITING_USER.
	st = m.Tick(at(600))
	if st.Status != StatusWaitingUser {
		t.Errorf("waiting lost after quiet: %+v", st)
	}
}

func TestAbortedAndInterrupted(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", false), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	m.Feed(ev("s1", codex.EvTurnAborted, 30), at(30))
	st := m.Snapshot(at(31))
	if st.Status != StatusInterrupted {
		t.Fatalf("aborted: %+v", st)
	}
	// STOPPED persists as well.
	st = m.Tick(at(31 + 3600))
	if st.Status != StatusInterrupted {
		t.Errorf("interrupted did not persist: %+v", st)
	}
}

func TestFailQuietDetection(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	failed := codex.Event{
		SessionID: "s1", Time: at(10), Kind: codex.EvItem,
		Item: "CommandExecution", ItemFailed: true, ExitCode: 1,
	}
	m.Feed(failed, at(10))
	st := m.Snapshot(at(15))
	if st.Status == StatusFailed {
		t.Fatalf("failed too early: %+v", st)
	}
	if st.Detail != "exit 1" {
		t.Errorf("detail = %q", st.Detail)
	}
	// After FailQuiet with nothing running, the turn is marked failed.
	st = m.Tick(at(10 + int(DefaultMachineConfig().FailQuiet/time.Second) + 1))
	if st.Status != StatusFailed || st.Attention != true {
		t.Fatalf("quiet failure: %+v", st)
	}
}

func TestStaleTurnFallsBackToIdle(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	st := m.Tick(at(1 + int(DefaultMachineConfig().StaleTurn/time.Second) + 1))
	if st.Status != StatusIdle {
		t.Errorf("stale turn: %+v", st)
	}
}

func TestDebounceHoldsLowLevelFlips(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	m.Feed(codex.Event{SessionID: "s1", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolExec, Command: "make"}, at(2))
	st := m.Snapshot(at(2).Add(10 * time.Millisecond))
	if st.Status != StatusExecuting {
		t.Fatalf("exec: %+v", st)
	}
	// Rapid flip to THINKING within LowDebounce is held.
	m.Feed(ev("s1", codex.EvToolOutput, 3), at(3))
	st = m.Snapshot(at(3).Add(50 * time.Millisecond))
	if st.Status != StatusExecuting {
		t.Errorf("flip was not held: %+v", st)
	}
	// After the debounce window it passes.
	st = m.Tick(at(4))
	if st.Status != StatusThinking {
		t.Errorf("flip never applied: %+v", st)
	}
}

func TestPriorityBypassesDebounce(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	m.Feed(ev("s1", codex.EvTaskComplete, 2), at(2))
	st := m.Snapshot(at(2))
	if st.Status != StatusDone {
		t.Fatalf("done delayed: %+v", st)
	}
}

func TestPlanProgress(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", false), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	plan := []codex.PlanStep{
		{Status: "completed"}, {Status: "completed"}, {Status: "in_progress"}, {Status: "pending"},
	}
	m.Feed(codex.Event{SessionID: "s1", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolPlan, Plan: plan}, at(2))
	st := m.Snapshot(at(3))
	if st.Plan.Done != 2 || st.Plan.Total != 4 {
		t.Fatalf("plan = %+v", st.Plan)
	}
}

func TestSelectionPrefersVSCodeAndActive(t *testing.T) {
	m := newMachine()
	// CLI session actively working.
	m.Feed(metaEvent("cli", false), base)
	m.Feed(ev("cli", codex.EvTaskStarted, 1), at(1))
	m.Feed(codex.Event{SessionID: "cli", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolExec, Command: "x"}, at(2))
	st := m.Snapshot(at(3))
	if st.Project != "demo" {
		t.Fatalf("cli not selected: %+v", st)
	}
	// A VS Code session starts working: it outranks the CLI one.
	m.Feed(metaEvent("vsc", true), at(4))
	m.Feed(ev("vsc", codex.EvTaskStarted, 5), at(5))
	st = m.Snapshot(at(6))
	_ = st // selection is internal; both are active, VS Code must win
	if cur := m.current; cur != "vsc" {
		t.Errorf("selected %q, want vsc", cur)
	}
}

func TestSelectionWaitingUserBeatsActive(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("a", true), base)
	m.Feed(ev("a", codex.EvTaskStarted, 1), at(1))
	m.Feed(codex.Event{SessionID: "a", Time: at(2), Kind: codex.EvToolStart, Tool: codex.ToolExec, Command: "x"}, at(2))
	m.Snapshot(at(3))

	m.Feed(metaEvent("b", false), at(4))
	m.Feed(ev("b", codex.EvTaskStarted, 5), at(5))
	m.Feed(codex.Event{SessionID: "b", Time: at(6), Kind: codex.EvToolStart, Tool: codex.ToolUserInput}, at(6))
	m.Snapshot(at(7))
	if m.current != "b" {
		t.Errorf("selected %q, want b (waiting user)", m.current)
	}
}

func TestIdleWithoutSessions(t *testing.T) {
	m := newMachine()
	st := m.Snapshot(at(0))
	if st.Status != StatusIdle {
		t.Fatalf("idle: %+v", st)
	}
}

func TestRatesUpdateWithoutTouchingState(t *testing.T) {
	m := newMachine()
	m.Feed(metaEvent("s1", true), base)
	m.Feed(ev("s1", codex.EvTaskStarted, 1), at(1))
	rates := codex.RateLimits{Valid: true, FiveHourLeft: 28, WeeklyLeft: 72}
	// Rates arrive with an OLD timestamp: they must not refresh activity.
	m.Feed(codex.Event{SessionID: "elsewhere", Time: at(-500), Kind: codex.EvRates, Rates: rates}, at(2))
	st := m.Snapshot(at(3))
	if !st.Rates.Valid || st.Rates.FiveHourLeft != 28 {
		t.Fatalf("rates missing: %+v", st)
	}
	if !st.HasTurn {
		t.Errorf("rates event must not alter session state: %+v", st)
	}
}
