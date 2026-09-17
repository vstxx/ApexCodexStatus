package codex

import (
	"strings"
	"testing"
	"time"
)

const fixtureMeta = `{"timestamp":"2026-09-17T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"session_id":"12345678-1234-1234-1234-123456789abc","id":"12345678-1234-1234-1234-123456789abc","timestamp":"2026-09-17T10:00:00.000Z","cwd":"D:\\Proj\\Demo","originator":"codex_vscode","cli_version":"0.154.0","source":"vscode","git":{"branch":"main","repository_url":"https://github.com/example/demo.git"}}}`

const fixtureTaskStarted = `{"timestamp":"2026-09-17T10:00:05.000Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1789000005,"model_context_window":258400,"collaboration_mode_kind":"default"}}`

const fixtureExecCall = `{"timestamp":"2026-09-17T10:00:10.000Z","ordinal":2,"type":"response_item","payload":{"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":"c1","name":"exec","input":"const r = await Promise.allSettled([\n  tools.exec_command({\n    cmd: \"npm test\",\n    workdir: \"D:\\\\Proj\\\\Demo\",\n    yield_time_ms: 10000\n  })\n]);"}}`

const fixtureToolOutput = `{"timestamp":"2026-09-17T10:00:20.000Z","ordinal":3,"type":"response_item","payload":{"type":"custom_tool_call_output","id":"ctco_1","call_id":"c1","output":[{"type":"input_text","text":"ok"}]}}`

const fixtureItemCommand = `{"timestamp":"2026-09-17T10:00:20.100Z","ordinal":4,"type":"event_msg","payload":{"type":"item_completed","thread_id":"x","turn_id":"t1","item":{"type":"CommandExecution","id":"e1","command":"npm test","status":"completed","exit_code":0,"aggregated_output":""},"completed_at_ms":1789000020100}}`

const fixtureTaskComplete = `{"timestamp":"2026-09-17T10:02:00.000Z","ordinal":5,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"Done.","started_at":1789000005,"completed_at":1789000120,"duration_ms":115000}}`

const fixturePatchCall = `{"timestamp":"2026-09-17T10:01:00.000Z","ordinal":6,"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"const patch = \"*** Begin Patch\\n*** Update File: D:\\\\Proj\\\\Demo\\\\src\\\\settings.ts\\n@@\\n-old\\n+new\\n*** Add File: other.txt\\n*** End Patch\"; tools.apply_patch(patch);"}}`

const fixturePlanCall = `{"timestamp":"2026-09-17T10:00:06.000Z","ordinal":7,"type":"response_item","payload":{"type":"function_call","id":"fc1","name":"update_plan","arguments":"{\"plan\":[{\"step\":\"a\",\"status\":\"completed\"},{\"step\":\"b\",\"status\":\"in_progress\"},{\"step\":\"c\",\"status\":\"pending\"}]}","call_id":"c2"}}`

const fixtureUserInput = `{"timestamp":"2026-09-17T10:00:07.000Z","ordinal":8,"type":"response_item","payload":{"type":"function_call","name":"request_user_input","arguments":"{\"questions\":[{\"header\":\"q\"}]}","call_id":"c3"}}`

const fixtureAborted = `{"timestamp":"2026-09-17T10:03:00.000Z","ordinal":9,"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}`

func TestParseMeta(t *testing.T) {
	evs := ParseLine([]byte(fixtureMeta))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Kind != EvSessionMeta || ev.Meta == nil {
		t.Fatalf("wrong kind %+v", ev)
	}
	m := ev.Meta
	if m.ID != "12345678-1234-1234-1234-123456789abc" {
		t.Errorf("id = %q", m.ID)
	}
	if !m.VSCode {
		t.Errorf("VSCode = false, want true")
	}
	if m.Repo != "demo" {
		t.Errorf("repo = %q, want demo", m.Repo)
	}
	if m.Branch != "main" {
		t.Errorf("branch = %q", m.Branch)
	}
}

func TestParseTaskLifecycle(t *testing.T) {
	evs := ParseLine([]byte(fixtureTaskStarted))
	if len(evs) != 1 || evs[0].Kind != EvTaskStarted {
		t.Fatalf("task_started: %+v", evs)
	}
	evs = ParseLine([]byte(fixtureTaskComplete))
	if len(evs) != 1 || evs[0].Kind != EvTaskComplete {
		t.Fatalf("task_complete: %+v", evs)
	}
	if d := evs[0].Duration.String(); d != "1m55s" {
		t.Errorf("duration = %s, want 1m55s", d)
	}
	evs = ParseLine([]byte(fixtureAborted))
	if len(evs) != 1 || evs[0].Kind != EvTurnAborted {
		t.Fatalf("turn_aborted: %+v", evs)
	}
}

func TestParseExecClassification(t *testing.T) {
	evs := ParseLine([]byte(fixtureExecCall))
	if len(evs) != 1 || evs[0].Kind != EvToolStart {
		t.Fatalf("exec call: %+v", evs)
	}
	if evs[0].Tool != ToolTest {
		t.Errorf("tool = %v, want test (npm test)", evs[0].Tool)
	}
	if evs[0].Command != "npm test" {
		t.Errorf("command = %q", evs[0].Command)
	}
}

func TestParsePatchFiles(t *testing.T) {
	evs := ParseLine([]byte(fixturePatchCall))
	if len(evs) != 1 {
		t.Fatalf("patch call: %+v", evs)
	}
	ev := evs[0]
	if ev.Tool != ToolPatch {
		t.Errorf("tool = %v, want patch", ev.Tool)
	}
	if len(ev.Files) != 2 || !strings.HasSuffix(ev.Files[0], "settings.ts") || ev.Files[1] != "other.txt" {
		t.Errorf("files = %v", ev.Files)
	}
}

func TestParsePlanProgress(t *testing.T) {
	evs := ParseLine([]byte(fixturePlanCall))
	if len(evs) != 1 || evs[0].Tool != ToolPlan {
		t.Fatalf("plan call: %+v", evs)
	}
	if len(evs[0].Plan) != 3 {
		t.Fatalf("plan len = %d", len(evs[0].Plan))
	}
	if evs[0].Plan[0].Status != "completed" || evs[0].Plan[2].Status != "pending" {
		t.Errorf("plan statuses = %+v", evs[0].Plan)
	}
}

func TestParseUserInput(t *testing.T) {
	evs := ParseLine([]byte(fixtureUserInput))
	if len(evs) != 1 || evs[0].Tool != ToolUserInput {
		t.Fatalf("user input: %+v", evs)
	}
}

func TestParseIgnoredTypes(t *testing.T) {
	for _, line := range []string{
		fixtureToolOutput, // produces EvToolOutput, no state signal
		`{"timestamp":"2026-09-17T10:00:00.000Z","ordinal":11,"type":"event_msg","payload":{"type":"token_count","info":null}}`,
		`{"timestamp":"2026-09-17T10:00:00.000Z","ordinal":12,"type":"world_state","payload":{"full":true}}`,
		`{"timestamp":"2026-09-17T10:00:00.000Z","ordinal":13,"type":"compacted","payload":{"message":""}}`,
		`{"timestamp":"2026-09-17T10:00:00.000Z","ordinal":14,"type":"response_item","payload":{"type":"reasoning"}}`,
	} {
		evs := ParseLine([]byte(line))
		for _, ev := range evs {
			switch ev.Kind {
			case EvToolOutput, EvItem:
			default:
				t.Errorf("unexpected event %v from %s", ev.Kind, line[:60])
			}
		}
	}
}

func TestParseMalformedLines(t *testing.T) {
	for _, line := range []string{
		``,
		`not json at all`,
		`{"timestamp":"2026-09-17T10:00:00.00`,
		`{"type":"event_msg","payload":{"type":"task_started"`, // truncated
		`[1,2,3]`,
	} {
		if evs := ParseLine([]byte(line)); len(evs) != 0 {
			t.Errorf("malformed %q produced %d events", line, len(evs))
		}
	}
}

func TestParseItemCommandFailure(t *testing.T) {
	line := strings.Replace(fixtureItemCommand, `"exit_code":0`, `"exit_code":1`, 1)
	evs := ParseLine([]byte(line))
	if len(evs) != 1 {
		t.Fatalf("no events")
	}
	if !evs[0].ItemFailed || evs[0].ExitCode != 1 {
		t.Errorf("failed=%v exit=%d", evs[0].ItemFailed, evs[0].ExitCode)
	}
}

func TestClassifyCommands(t *testing.T) {
	cases := []struct {
		cmd  string
		want Tool
	}{
		{"git status --short", ToolRead},
		{"git commit -m x", ToolExec},
		{"rg -n TODO src", ToolRead},
		{"Get-Content foo.txt", ToolRead},
		{"npm run test", ToolTest},
		{"go test ./...", ToolTest},
		{"pytest -k auth", ToolTest},
		{"cargo build --release", ToolExec},
		{"node build.js", ToolExec},
		{"rm -rf node_modules", ToolExec},
		{"", ToolExec},
	}
	for _, c := range cases {
		if got := classifyCommand(c.cmd); got != c.want {
			t.Errorf("classify(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestRepoNameAndBase(t *testing.T) {
	if got := repoName("https://github.com/example/demo.git"); got != "demo" {
		t.Errorf("repoName = %q", got)
	}
	if got := baseName(`D:\Proj\Demo App`); got != "Demo App" {
		t.Errorf("baseName = %q", got)
	}
}

const fixtureTokenCount = `{"timestamp":"2026-09-17T10:01:00.000Z","ordinal":15,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":72.4,"window_minutes":300,"resets_at":1789002000},"secondary":{"used_percent":28.0,"window_minutes":10080,"resets_at":1789500000},"plan_type":"plus"}}}`

const fixtureTokenCountNull = `{"timestamp":"2026-09-17T10:01:00.000Z","ordinal":16,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":null}}`

func TestParseTokenCountRates(t *testing.T) {
	evs := ParseLine([]byte(fixtureTokenCount))
	if len(evs) != 1 || evs[0].Kind != EvRates {
		t.Fatalf("rates: %+v", evs)
	}
	r := evs[0].Rates
	if !r.Valid || r.FiveHourLeft != 28 || r.WeeklyLeft != 72 {
		t.Errorf("rates = %+v", r)
	}
	if evs2 := ParseLine([]byte(fixtureTokenCountNull)); len(evs2) != 0 {
		t.Errorf("null rate_limits should yield no event: %+v", evs2)
	}
}

const fixtureCommandArrayFailed = `{"timestamp":"2026-09-17T10:05:00.000Z","ordinal":20,"type":"event_msg","payload":{"type":"item_completed","thread_id":"x","turn_id":"t1","item":{"type":"CommandExecution","id":"e2","command":["bash","-c","exit 2"],"status":"failed","exit_code":2,"aggregated_output":""},"completed_at_ms":1789000300000}}`

func TestParseCommandArrayExitCode(t *testing.T) {
	// `command` as an array must not break exit-code extraction.
	evs := ParseLine([]byte(fixtureCommandArrayFailed))
	if len(evs) != 1 {
		t.Fatalf("no events")
	}
	ev := evs[0]
	if !ev.ItemFailed || ev.ExitCode != 2 {
		t.Errorf("failed=%v exit=%d", ev.ItemFailed, ev.ExitCode)
	}
	if ev.Command != "bash -c exit 2" {
		t.Errorf("command = %q", ev.Command)
	}
}

func TestParseAbortedDuration(t *testing.T) {
	// turn_aborted carries duration_ms in the current format.
	line := `{"timestamp":"2026-09-17T10:06:00.000Z","ordinal":21,"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted","started_at":1789000005,"completed_at":1789000065,"duration_ms":60000}}`
	evs := ParseLine([]byte(line))
	if len(evs) != 1 || evs[0].Kind != EvTurnAborted || evs[0].Duration != time.Minute {
		t.Fatalf("aborted duration: %+v", evs)
	}
}
