package state

import (
	"strings"
	"time"

	"codexconnector/internal/codex"
)

// Machine folds the Codex event stream into one displayable UIState.
// It is not safe for concurrent use; the owner serializes calls.
type Machine struct {
	cfg   MachineConfig
	rates codex.RateLimits // account-wide, updated by any session's token_count

	sessions map[string]*sessionState
	current  string // selected session id

	shown    UIState // last returned snapshot
	shownSet bool
	pendStat Status
	pendSet  bool
	pendAt   time.Time
}

type sessionState struct {
	meta      *codex.SessionMeta
	status    Status
	action    string
	detail    string
	plan      Plan
	turnOpen  bool
	turnStart time.Time
	lastEvent time.Time
	pendTool  codex.Tool
	lastFail  string // reason of last failed item/command, "" if none
	doneDur   time.Duration

	lastActionWord string // frozen "what he was doing" word for terminal frames
}

// NewMachine creates a machine with the given timings.
func NewMachine(cfg MachineConfig) *Machine {
	return &Machine{
		cfg:      cfg,
		sessions: make(map[string]*sessionState),
	}
}

// Feed ingests one Codex event.
func (m *Machine) Feed(ev codex.Event, now time.Time) {
	if ev.Kind == codex.EvRates {
		// Account-wide values: not session activity, never touches state.
		m.rates = ev.Rates
		return
	}
	s := m.sessions[ev.SessionID]
	if s == nil {
		s = &sessionState{status: StatusIdle}
		m.sessions[ev.SessionID] = s
	}
	if !ev.Time.IsZero() {
		now = ev.Time
	}
	s.lastEvent = now

	switch ev.Kind {
	case codex.EvSessionMeta:
		s.meta = ev.Meta

	case codex.EvTaskStarted:
		s.turnOpen = true
		s.turnStart = now
		s.detail = ""
		s.lastFail = ""
		s.plan = Plan{} // new turn: old todo progress no longer applies
		s.pendTool = codex.ToolNone
		s.status = StatusThinking

	case codex.EvToolStart:
		m.feedToolStart(s, ev, now)

	case codex.EvToolOutput:
		s.pendTool = codex.ToolNone
		if s.status.Active() && s.status != StatusWaitingUser {
			s.status = StatusThinking
		}

	case codex.EvItem:
		m.feedItem(s, ev)

	case codex.EvTaskComplete:
		s.lastActionWord = s.status.ActionWord() // freeze what he was doing
		s.turnOpen = false
		s.pendTool = codex.ToolNone
		s.status = StatusDone
		if ev.Duration > 0 {
			s.doneDur = ev.Duration
		}

	case codex.EvTurnAborted:
		s.lastActionWord = s.status.ActionWord()
		s.turnOpen = false
		s.pendTool = codex.ToolNone
		s.status = StatusInterrupted
		if ev.Duration > 0 {
			s.doneDur = ev.Duration // frozen time shown on the STOP frame
		}

	case codex.EvError:
		s.lastActionWord = s.status.ActionWord()
		s.turnOpen = false
		s.pendTool = codex.ToolNone
		s.status = StatusFailed
		s.detail = "error"
	}
}

func (m *Machine) feedToolStart(s *sessionState, ev codex.Event, now time.Time) {
	if !s.turnOpen {
		s.turnOpen = true
		if s.turnStart.IsZero() {
			s.turnStart = now
		}
	}
	s.pendTool = ev.Tool
	switch ev.Tool {
	case codex.ToolExec, codex.ToolRead, codex.ToolTest:
		s.action = simplifyCommand(ev.Command)
		switch ev.Tool {
		case codex.ToolRead:
			s.status = StatusReading
		case codex.ToolTest:
			s.status = StatusTesting
		default:
			s.status = StatusExecuting
		}
	case codex.ToolPatch:
		s.action = simplifyFiles(ev.Files)
		s.status = StatusEditing
	case codex.ToolSearch:
		s.action = "web search"
		s.status = StatusSearching
	case codex.ToolPlan:
		if pl := planProgress(ev.Plan); pl.Total > 0 {
			s.plan = pl
		}
		s.pendTool = codex.ToolNone // plan updates complete instantly
		s.status = StatusWorking
	case codex.ToolUserInput:
		s.status = StatusWaitingUser
		s.pendTool = codex.ToolNone // the question is out; nothing is running
	case codex.ToolWait:
		s.pendTool = codex.ToolNone
		if s.status.Active() && s.status != StatusWaitingUser {
			s.status = StatusThinking
		}
	default:
		s.status = StatusWorking
	}
}

func (m *Machine) feedItem(s *sessionState, ev codex.Event) {
	switch ev.Item {
	case "Reasoning":
		if lowLevel(s.status) {
			s.status = StatusThinking
		}
	case "AgentMessage", "ContextCompaction":
		if lowLevel(s.status) {
			s.status = StatusWorking
		}
	case "FileChange":
		if lowLevel(s.status) {
			s.status = StatusEditing
			if a := simplifyFiles(ev.Files); a != "" {
				s.action = a
			}
		}
	case "WebSearch":
		if lowLevel(s.status) {
			s.status = StatusSearching
		}
	case "CommandExecution":
		if ev.ItemFailed {
			s.lastFail = "exit " + itoa(max(ev.ExitCode, 1))
			s.detail = s.lastFail
		} else if s.detail != "" {
			s.detail = ""
			s.lastFail = ""
		}
	case "McpToolCall":
		if ev.ItemFailed {
			s.lastFail = "tool failed"
			s.detail = s.lastFail
		}
	}
}

// lowLevel reports whether a status yields to completed-item evidence.
func lowLevel(s Status) bool {
	return s.Active() && s != StatusWaitingUser
}

// Tick advances time-derived behaviour (stall detection) and returns the
// current display snapshot.
func (m *Machine) Tick(now time.Time) UIState {
	for _, s := range m.sessions {
		if !s.turnOpen || s.pendTool != codex.ToolNone || s.status == StatusWaitingUser {
			continue
		}
		quiet := now.Sub(s.lastEvent)
		if s.lastFail != "" && quiet >= m.cfg.FailQuiet {
			s.turnOpen = false
			s.status = StatusFailed
		} else if s.lastFail == "" && quiet >= m.cfg.StaleTurn {
			// Turn never completed (crash, editor closed): give up quietly.
			s.turnOpen = false
			s.status = StatusIdle
			s.doneDur = 0
		}
	}
	return m.Snapshot(now)
}

// Snapshot returns the state to display right now, applying session selection
// and the low-priority transition debouncer.
func (m *Machine) Snapshot(now time.Time) UIState {
	sel := m.selectSession(now)
	var st UIState
	if sel == nil {
		st = UIState{Status: StatusIdle, UpdatedAt: now}
	} else {
		st = m.build(sel, now)
	}
	st.Rates = m.rates

	// Debounce low-priority flips between active substates (EXEC->READ->THINK
	// churn); priority transitions and terminal fallbacks pass immediately.
	if m.shownSet && st.Status != m.shown.Status && !st.Status.Priority() &&
		st.Status.Active() && m.shown.Status.Active() {
		if !m.pendSet || st.Status != m.pendStat {
			m.pendStat = st.Status
			m.pendAt = now
			m.pendSet = true
		}
		if now.Sub(m.pendAt) < m.cfg.LowDebounce {
			st.Status = m.shown.Status
		}
	} else {
		m.pendSet = false
	}

	m.shown = st
	m.shownSet = true
	return st
}

func (m *Machine) build(s *sessionState, now time.Time) UIState {
	st := UIState{
		Project:    projectName(s.meta),
		Action:     s.action,
		Detail:     s.detail,
		Plan:       s.plan,
		UpdatedAt:  s.lastEvent,
		ActionWord: s.lastActionWord,
	}
	if s.turnOpen {
		st.HasTurn = true
		st.Status = s.status
		st.ActionWord = s.status.ActionWord()
		if !s.turnStart.IsZero() && now.After(s.turnStart) {
			st.Elapsed = now.Sub(s.turnStart)
		}
		st.Attention = s.status == StatusWaitingUser
		st.Animated = s.status.Active()
		return st
	}
	st.Status = terminalStatus(s.status)
	st.HasTurn = st.Status != StatusIdle
	st.Elapsed = s.doneDur
	st.Attention = st.Status == StatusFailed
	if st.Status == StatusIdle {
		st.ActionWord = "" // a dead turn shows plain IDLE, no stale action
	}
	return st
}

// terminalStatus maps the session's last derived status into what should show
// while no turn is open. Terminal states persist until replaced by new
// activity (see eligible).
func terminalStatus(last Status) Status {
	switch last {
	case StatusDone, StatusFailed, StatusInterrupted:
		return last
	}
	return StatusIdle
}

// selectSession picks the session to display: waiting user first, then active
// VS Code sessions, then any active session, then recent terminal ones.
func (m *Machine) selectSession(now time.Time) *sessionState {
	if cur, ok := m.sessions[m.current]; ok && m.eligible(cur, now) {
		if m.betterCandidate(cur, now) == nil {
			return cur
		}
	}
	var best *sessionState
	var bestID string
	for id, s := range m.sessions {
		if !m.eligible(s, now) {
			continue
		}
		if best == nil || m.score(s, now) > m.score(best, now) {
			best = s
			bestID = id
		}
	}
	if best != nil {
		if m.current != bestID {
			m.current = bestID
			m.shownSet = false // no debouncing across a session switch
		}
		return best
	}
	m.current = ""
	return nil
}

func (m *Machine) eligible(s *sessionState, now time.Time) bool {
	if s.meta == nil {
		return false
	}
	if s.turnOpen {
		return true
	}
	// Terminal states persist until new activity replaces them: a finished
	// task keeps showing DONE (with frozen time) for as long as nothing else
	// happens, instead of dropping to IDLE after a hold.
	switch s.status {
	case StatusDone, StatusFailed, StatusInterrupted:
		return true
	}
	return false
}

// betterCandidate reports whether some session outranks the current one.
func (m *Machine) betterCandidate(cur *sessionState, now time.Time) *sessionState {
	curScore := m.score(cur, now)
	for id, s := range m.sessions {
		if id == m.current || !m.eligible(s, now) {
			continue
		}
		if m.score(s, now) > curScore {
			return s
		}
	}
	return nil
}

// score ranks display candidates. Waiting user > active > failed > done;
// VS Code sessions outrank CLI ones; newer activity outranks older.
func (m *Machine) score(s *sessionState, now time.Time) int {
	sc := 0
	switch {
	case s.turnOpen && s.status == StatusWaitingUser:
		sc = 5000
	case s.turnOpen:
		sc = 4000
	case s.status == StatusFailed || s.status == StatusInterrupted:
		sc = 2000
	default:
		sc = 1000
	}
	if s.meta != nil && s.meta.VSCode {
		sc += 500
	}
	sc += 100 - int(now.Sub(s.lastEvent).Seconds()/10) // slight recency bonus
	return sc
}

func projectName(m *codex.SessionMeta) string {
	if m == nil {
		return ""
	}
	return m.Repo
}

// planProgress counts real plan steps.
func planProgress(steps []codex.PlanStep) Plan {
	p := Plan{Total: len(steps)}
	for _, s := range steps {
		if strings.EqualFold(s.Status, "completed") {
			p.Done++
		}
	}
	if p.Total == 0 {
		return Plan{}
	}
	return p
}

// simplifyCommand reduces a command line to its displayable core.
func simplifyCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = strings.TrimSpace(cmd[:i])
	}
	if len(cmd) > 120 {
		cmd = cmd[:120]
	}
	return cmd
}

// simplifyFiles reduces a file list to "name +N" style short text.
func simplifyFiles(files []string) string {
	if len(files) == 0 {
		return ""
	}
	name := files[0]
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(files) > 1 {
		return name + " +" + itoa(len(files)-1)
	}
	return name
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
