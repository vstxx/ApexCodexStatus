// Package state normalizes raw Codex events into a small UI state model.
// It is pure: no I/O, fully deterministic given events and clock values.
package state

import (
	"time"

	"codexconnector/internal/codex"
)

// Status is the normalized Codex activity state.
// Only states with a reliable empirical signal exist (see docs/FINDINGS.md).
type Status int

const (
	StatusIdle Status = iota
	StatusThinking
	StatusWorking
	StatusSearching
	StatusReading
	StatusEditing
	StatusExecuting
	StatusTesting
	StatusWaitingUser
	StatusDone
	StatusFailed
	StatusInterrupted
)

var statusNames = [...]string{
	"IDLE", "THINKING", "WORKING", "SEARCHING", "READING",
	"EDITING", "EXECUTING", "TESTING", "WAITING USER",
	"DONE", "FAILED", "STOPPED",
}

// String returns the display word for the status.
func (s Status) String() string {
	if s < 0 || int(s) >= len(statusNames) {
		return "?"
	}
	return statusNames[s]
}

// Priority marks statuses that must bypass the transition debouncer.
func (s Status) Priority() bool {
	switch s {
	case StatusWaitingUser, StatusDone, StatusFailed, StatusInterrupted:
		return true
	}
	return false
}

// Active reports whether the status means Codex is busy right now.
func (s Status) Active() bool {
	switch s {
	case StatusThinking, StatusWorking, StatusSearching, StatusReading,
		StatusEditing, StatusExecuting, StatusTesting, StatusWaitingUser:
		return true
	}
	return false
}

// ActionWord returns the short "what he is doing" word for active states
// (EXEC, EDIT, READ, TEST, SEARCH, THINK, WORK, WAIT).
func (s Status) ActionWord() string {
	switch s {
	case StatusThinking:
		return "THINK"
	case StatusWorking:
		return "WORK"
	case StatusSearching:
		return "SEARCH"
	case StatusReading:
		return "READ"
	case StatusEditing:
		return "EDIT"
	case StatusExecuting:
		return "EXEC"
	case StatusTesting:
		return "TEST"
	case StatusWaitingUser:
		return "WAIT"
	}
	return ""
}

// Plan is real todo/plan progress reported by Codex itself.
type Plan struct {
	Done  int
	Total int
}

// UIState is one renderable snapshot.
type UIState struct {
	Status     Status
	Project    string           // repository or folder name
	Action     string           // short current-action line (command / file)
	Detail     string           // secondary note, e.g. "exit 1"
	ActionWord string           // "EXEC"/"EDIT"/… frozen at completion for terminal states
	Elapsed    time.Duration    // active: time since turn start; terminal: task duration
	Plan       Plan             // zero Plan when Codex reported none
	Rates      codex.RateLimits // account usage left (5H / weekly)
	Attention  bool             // render header inverted
	Animated   bool             // show the subtle activity dot
	HasTurn    bool             // a turn is or was recently active
	UpdatedAt  time.Time        // last state-changing event
}

// MachineConfig holds the tunable timings.
type MachineConfig struct {
	LowDebounce time.Duration // min interval between low-priority status flips
	FailQuiet   time.Duration // open turn + quiet + last command failed -> FAILED
	StaleTurn   time.Duration // open turn, nothing pending, no events -> give up
}

// DefaultMachineConfig returns the shipped defaults.
func DefaultMachineConfig() MachineConfig {
	return MachineConfig{
		LowDebounce: 250 * time.Millisecond,
		FailQuiet:   20 * time.Second,
		StaleTurn:   3 * time.Minute,
	}
}
