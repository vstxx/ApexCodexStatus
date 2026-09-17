// Package codex turns raw Codex session telemetry (JSONL rollouts) into a
// small stream of normalized events. It is read-only: nothing here writes to
// Codex files, executes log content, or retains conversation text.
package codex

import "time"

// Kind classifies a normalized event.
type Kind int

const (
	EvSessionMeta Kind = iota // Meta is set
	EvTaskStarted
	EvTaskComplete // Duration holds the real task duration
	EvTurnAborted
	EvItem       // a completed item: Item holds its type name
	EvToolStart  // in-flight tool call (live signal)
	EvToolOutput // pending tool call finished
	EvError      // explicit error event (none in the current format; reserved)
	EvRates      // account rate-limit update (token_count event)
)

// RateLimits holds Codex plan usage expressed as "percent left" per window.
// Both fields come from the account-wide token_count event.
type RateLimits struct {
	Valid        bool
	FiveHourLeft int // 5-hour window (primary), percent remaining
	WeeklyLeft   int // weekly window (secondary), percent remaining
}

// Tool classifies the tool behind a live call.
type Tool int

const (
	ToolNone      Tool = iota
	ToolExec           // command execution
	ToolPatch          // file modification
	ToolRead           // unambiguous read-only command
	ToolTest           // test-runner invocation
	ToolSearch         // web search
	ToolPlan           // todo/plan update
	ToolUserInput      // asking the user a question
	ToolWait           // wait/sleep/yield
)

func (t Tool) String() string {
	switch t {
	case ToolExec:
		return "exec"
	case ToolPatch:
		return "patch"
	case ToolRead:
		return "read"
	case ToolTest:
		return "test"
	case ToolSearch:
		return "search"
	case ToolPlan:
		return "plan"
	case ToolUserInput:
		return "user-input"
	case ToolWait:
		return "wait"
	}
	return "other"
}

// SessionMeta carries operational session identity only.
type SessionMeta struct {
	ID      string
	CWD     string
	Repo    string // repository name (from git URL, else cwd basename)
	Branch  string
	VSCode  bool
	Started time.Time
	Version string
}

// PlanStep is one entry of a Codex-reported plan.
type PlanStep struct {
	Text   string
	Status string // "pending" | "in_progress" | "completed"
}

// Event is one normalized update. Fields not relevant to Kind stay zero.
// All string content is operational metadata, bounded in length, and safe
// for display after renderer sanitization.
type Event struct {
	SessionID  string
	Time       time.Time
	Kind       Kind
	Meta       *SessionMeta
	Tool       Tool
	Command    string // current command (bounded)
	Files      []string
	Plan       []PlanStep
	ExitCode   int    // -1 when not applicable
	Item       string // item type for EvItem
	ItemFailed bool   // EvItem: item reported failure
	Duration   time.Duration
	Rates      RateLimits // EvRates
}
