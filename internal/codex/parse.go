package codex

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// maxField caps extracted operational strings; display code truncates further.
const maxField = 256

// line is the raw JSONL envelope.
type line struct {
	Timestamp time.Time       `json:"timestamp"`
	Ordinal   int64           `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type metaPayload struct {
	SessionID  string `json:"session_id"`
	ID         string `json:"id"`
	CWD        string `json:"cwd"`
	Originator string `json:"originator"`
	Source     string `json:"source"`
	Version    string `json:"cli_version"`
	Timestamp  string `json:"timestamp"`
	Git        *struct {
		Branch  string `json:"branch"`
		RepoURL string `json:"repository_url"`
	} `json:"git"`
}

type eventPayload struct {
	Type       string          `json:"type"`
	TurnID     string          `json:"turn_id"`
	DurationMS *int64          `json:"duration_ms"`
	Item       json.RawMessage `json:"item"`
}

type toolCallPayload struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Input     string `json:"input"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type itemEnvelope struct {
	Type   string          `json:"type"`
	Status string          `json:"status"`
	Item   json.RawMessage `json:"item"`
}

type commandItem struct {
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	ExitCode  *int            `json:"exit_code"`
	Command   json.RawMessage `json:"command"` // string OR array of strings
	ParsedCmd []struct {
		Type string `json:"type"`
		Cmd  string `json:"cmd"`
	} `json:"parsed_cmd"`
}

type fileChangeItem struct {
	Type    string `json:"type"`
	Changes map[string]struct {
		Type string `json:"type"`
	} `json:"changes"`
}

type mcpItem struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

type planItem struct {
	Type string `json:"type"`
}

// ParseLine converts one JSONL line into zero or more normalized events.
// Unknown or uninteresting line types yield no events.
func ParseLine(data []byte) []Event {
	var l line
	if err := json.Unmarshal(data, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "session_meta":
		return parseSessionMeta(l)
	case "event_msg":
		return parseEventMsg(l)
	case "response_item":
		return parseResponseItem(l)
	}
	return nil
}

func parseSessionMeta(l line) []Event {
	var p metaPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil
	}
	m := &SessionMeta{
		ID:      p.SessionID,
		CWD:     bound(p.CWD),
		VSCode:  p.Originator == "codex_vscode" || p.Source == "vscode",
		Version: bound(p.Version),
	}
	if m.ID == "" {
		m.ID = p.ID
	}
	if ts, err := time.Parse(time.RFC3339Nano, p.Timestamp); err == nil {
		m.Started = ts
	} else {
		m.Started = l.Timestamp
	}
	if p.Git != nil {
		m.Branch = bound(p.Git.Branch)
		m.Repo = bound(repoName(p.Git.RepoURL))
	}
	if m.Repo == "" {
		m.Repo = baseName(m.CWD)
	}
	return []Event{{SessionID: m.ID, Time: l.Timestamp, Kind: EvSessionMeta, Meta: m}}
}

func parseEventMsg(l line) []Event {
	var p eventPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil
	}
	base := Event{Time: l.Timestamp}
	switch p.Type {
	case "task_started":
		base.Kind = EvTaskStarted
	case "task_complete":
		base.Kind = EvTaskComplete
		if p.DurationMS != nil && *p.DurationMS > 0 {
			base.Duration = time.Duration(*p.DurationMS) * time.Millisecond
		}
	case "turn_aborted":
		base.Kind = EvTurnAborted
		if p.DurationMS != nil && *p.DurationMS > 0 {
			base.Duration = time.Duration(*p.DurationMS) * time.Millisecond
		}
	case "item_completed", "item_started", "item_updated":
		if p.Item == nil {
			return nil
		}
		return parseItem(l, p.Item)
	case "error", "stream_error", "turn_failed":
		base.Kind = EvError
	case "token_count":
		return parseTokenCount(l)
	default:
		return nil // thread_settings_applied, ...
	}
	return []Event{base}
}

type rateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
}

type rateLimitsPayload struct {
	Primary   *rateWindow `json:"primary"`
	Secondary *rateWindow `json:"secondary"`
}

type tokenCountPayload struct {
	Type       string             `json:"type"`
	RateLimits *rateLimitsPayload `json:"rate_limits"`
}

// parseTokenCount converts a token_count event into percent-left values for
// the two Codex plan windows (primary = 5-hour, secondary = weekly).
// Returns Valid=false when no usable rate-limit data is present.
func parseTokenCount(l line) []Event {
	var p tokenCountPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil || p.RateLimits == nil {
		return nil
	}
	var rates RateLimits
	if w := p.RateLimits.Primary; w != nil && w.WindowMinutes > 0 {
		rates.FiveHourLeft = percentLeft(w.UsedPercent)
		rates.Valid = true
	}
	if w := p.RateLimits.Secondary; w != nil && w.WindowMinutes > 0 {
		rates.WeeklyLeft = percentLeft(w.UsedPercent)
		rates.Valid = true
	}
	if !rates.Valid {
		return nil
	}
	return []Event{{Time: l.Timestamp, Kind: EvRates, Rates: rates}}
}

func percentLeft(used float64) int {
	left := 100 - int(used+0.5)
	if left < 0 {
		left = 0
	}
	if left > 100 {
		left = 100
	}
	return left
}

func parseItem(l line, raw json.RawMessage) []Event {
	var env itemEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	ev := Event{Time: l.Timestamp, Kind: EvItem, Item: env.Type, ExitCode: -1}
	switch env.Type {
	case "CommandExecution":
		// Decode status fields first: `command` can be a string OR an array
		// (PowerShell invocations use arrays), which would fail a strict
		// unmarshal of the whole item and lose the exit code with it.
		var head struct {
			Status   string `json:"status"`
			ExitCode *int   `json:"exit_code"`
		}
		if json.Unmarshal(raw, &head) == nil && head.ExitCode != nil {
			ev.ExitCode = *head.ExitCode
			ev.ItemFailed = *head.ExitCode != 0
		}
		if cmd := extractCommandItem(raw); cmd != "" {
			ev.Command = cmd
		}
	case "FileChange":
		var fi fileChangeItem
		if json.Unmarshal(raw, &fi) == nil {
			for path := range fi.Changes {
				ev.Files = append(ev.Files, bound(path))
			}
		}
	case "McpToolCall":
		var mi mcpItem
		if json.Unmarshal(raw, &mi) == nil {
			ev.ItemFailed = strings.EqualFold(mi.Status, "failed")
		}
	case "Plan":
		// plan items have no extra signal for us
	}
	return []Event{ev}
}

func parseResponseItem(l line) []Event {
	var p toolCallPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil
	}
	switch p.Type {
	case "custom_tool_call", "function_call":
		ev := Event{Time: l.Timestamp, Kind: EvToolStart, ExitCode: -1}
		classifyToolCall(&ev, p.Name, p.Input, p.Arguments)
		return []Event{ev}
	case "custom_tool_call_output", "function_call_output":
		return []Event{{Time: l.Timestamp, Kind: EvToolOutput}}
	}
	return nil // reasoning, message, ... have no distinct state effect
}

// classifyToolCall fills ev.Tool plus command/file/plan payloads from the
// raw tool-call body. exec inputs are JS sources invoking tools.*; function
// calls carry JSON arguments.
func classifyToolCall(ev *Event, name, input, args string) {
	switch name {
	case "apply_patch":
		ev.Tool = ToolPatch
		ev.Files = patchFiles(input)
		return
	case "update_plan":
		ev.Tool = ToolPlan
		ev.Plan = parsePlan(args)
		return
	case "request_user_input", "request_user_input_async":
		ev.Tool = ToolUserInput
		return
	case "wait", "sleep":
		ev.Tool = ToolWait
		return
	case "view_image":
		ev.Tool = ToolRead
		return
	case "shell_command", "exec_command", "run":
		ev.Tool = classifyCommand(firstJSONString(args, "cmd"))
		ev.Command = bound(firstJSONString(args, "cmd"))
		return
	}
	if name == "exec" || strings.Contains(input, "tools.") {
		parseExecInput(ev, input)
	}
}

// parseExecInput inspects the JS body of an `exec` code-mode call.
func parseExecInput(ev *Event, input string) {
	if i := strings.Index(input, "tools.apply_patch"); i >= 0 {
		ev.Tool = ToolPatch
		ev.Files = patchFiles(input)
		return
	}
	if i := strings.Index(input, "tools.update_plan"); i >= 0 {
		ev.Tool = ToolPlan
		if obj := balancedObject(input[i+len("tools.update_plan"):]); obj != "" {
			ev.Plan = parsePlan(obj)
		}
		return
	}
	if strings.Contains(input, "web__run") || strings.Contains(input, "web_search") {
		ev.Tool = ToolSearch
		return
	}
	if ev.Command = extractExecCommand(input); ev.Command != "" {
		ev.Tool = classifyCommand(ev.Command)
		return
	}
	if strings.Contains(input, "view_image") {
		ev.Tool = ToolRead
		return
	}
	// Unclassified exec body: treat as generic work.
	ev.Tool = ToolExec
}

var cmdRe = regexp.MustCompile(`tools\.(?:exec_command|shell_command|run)\s*\(`)

// extractExecCommand pulls the first cmd: "..." out of an exec JS body.
func extractExecCommand(input string) string {
	loc := cmdRe.FindStringIndex(input)
	if loc == nil {
		return ""
	}
	rest := input[loc[1]:]
	i := strings.Index(rest, "cmd:")
	if i < 0 || i > 200 {
		return ""
	}
	return jsString(rest[i+4:])
}

var patchFileRe = regexp.MustCompile(`(?:Update|Add) File: ([^\n"]+)`)

// patchFiles extracts target paths from a "*** Begin Patch" body. The body
// is a JS string literal, so \n and \\ escapes are decoded first.
func patchFiles(input string) []string {
	text := strings.ReplaceAll(input, `\n`, "\n")
	text = strings.ReplaceAll(text, `\\`, `\`)
	matches := patchFileRe.FindAllStringSubmatch(text, 8)
	files := make([]string, 0, len(matches))
	for _, m := range matches {
		files = append(files, bound(strings.TrimSpace(m[1])))
	}
	return files
}

func parsePlan(obj string) []PlanStep {
	var p struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(obj), &p); err != nil {
		return nil
	}
	steps := make([]PlanStep, 0, len(p.Plan))
	for _, s := range p.Plan {
		steps = append(steps, PlanStep{Text: bound(s.Step), Status: s.Status})
	}
	return steps
}

// balancedObject returns the first {...} at the head of s (after skipping
// whitespace), or "".
func balancedObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s) && i < start+8192; i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// jsString decodes a JS string literal starting at (or after) the opening
// quote of s.
func jsString(s string) string {
	q := strings.IndexAny(s, "\"'")
	if q < 0 {
		return ""
	}
	quote := s[q]
	var b strings.Builder
	for i := q + 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		if c == quote {
			out := b.String()
			return bound(out)
		}
		b.WriteByte(c)
	}
	return bound(b.String())
}

func firstJSONString(js, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &obj); err != nil {
		return ""
	}
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return bound(s)
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return bound(strings.Join(arr, " "))
	}
	return ""
}

// classifyCommand distinguishes read-only and test commands from generic
// execution. Confidence-driven: only unambiguous reads are classified READ.
func classifyCommand(cmd string) Tool {
	if cmd == "" {
		return ToolExec
	}
	lower := strings.ToLower(cmd)
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return ToolExec
	}
	first := fields[0]
	if i := strings.LastIndex(first, "/"); i >= 0 {
		first = first[i+1:]
	}
	if first == "git" {
		for _, sub := range fields[1:] {
			switch sub {
			case "status", "log", "diff", "show", "branch", "remote", "blame",
				"shortlog", "describe", "rev-parse", "ls-files", "config", "tag":
				return ToolRead
			case "commit", "push", "pull", "checkout", "merge", "rebase", "reset", "clean", "add":
				return ToolExec
			}
		}
	}
	switch first {
	case "rg", "grep", "cat", "head", "tail", "ls", "dir", "find", "fd", "tree",
		"wc", "file", "stat", "du", "df", "which", "where", "type", "pwd",
		"get-content", "get-childitem", "select-string", "test-path",
		"measure-object", "resolve-path":
		return ToolRead
	}
	for _, t := range fields {
		if t == "test" || t == "tests" {
			return ToolTest
		}
	}
	for _, runner := range []string{"pytest", "vitest", "jest", "playwright", "phpunit", "rspec"} {
		if strings.Contains(lower, runner) {
			return ToolTest
		}
	}
	for _, pair := range [][2]string{
		{"go ", "test"}, {"cargo ", "test"}, {"npm ", "test"}, {"pnpm ", "test"},
		{"yarn ", "test"}, {"dotnet ", "test"}, {"mvn ", "test"}, {"gradle ", "test"},
	} {
		if strings.HasPrefix(lower, pair[0]) && strings.Contains(lower, pair[1]) {
			return ToolTest
		}
	}
	return ToolExec
}

// extractCommandItem pulls a displayable command from a CommandExecution
// item: parsed_cmd first, then `command` (string or array form).
func extractCommandItem(raw json.RawMessage) string {
	var ci commandItem
	if json.Unmarshal(raw, &ci) != nil {
		return ""
	}
	if len(ci.ParsedCmd) > 0 && ci.ParsedCmd[0].Cmd != "" {
		return bound(ci.ParsedCmd[0].Cmd)
	}
	if len(ci.Command) > 0 {
		var s string
		if json.Unmarshal(ci.Command, &s) == nil && strings.TrimSpace(s) != "" {
			return bound(strings.TrimSpace(s))
		}
		var arr []string
		if json.Unmarshal(ci.Command, &arr) == nil && len(arr) > 0 {
			return bound(strings.Join(arr, " "))
		}
	}
	return ""
}

func bound(s string) string {
	if len(s) > maxField {
		r := []rune(s)
		if len(r) > maxField {
			return string(r[:maxField])
		}
	}
	return s
}

func repoName(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if u == "" {
		return ""
	}
	if i := strings.LastIndexAny(u, "/\\:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

func baseName(path string) string {
	p := strings.TrimSuffix(strings.ReplaceAll(path, "\\", "/"), "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return p
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
