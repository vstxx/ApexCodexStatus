# ACS (ApexCodexStatus) — Research Findings

Empirically established on this machine (Windows 11, 2026-09-17) and from primary upstream sources.
Every claim below was verified against real local data or official documentation — nothing assumed.

## 1. Codex state source (decision: JSONL session tail)

### A. Official live interface (app-server) — ruled out for observation

The Codex app-server (`codex app-server`) speaks bidirectional JSON-RPC 2.0, but **over stdio only**.
It is spawned *per client* (the VS Code extension launches its own instance; `codex.exe` and
`codex-code-mode-host.exe` were observed running as children of the extension). There is no local
network listener, so a second process cannot observe the VS Code session through it without
spawning a separate Codex instance or hijacking the extension's stdio — both explicitly forbidden
by the project constraints. A network transport for app-server is only a feature request
(openai/codex#11166), not a shipped capability.

**Decision:** tail the real rollout session files. They are the sanctioned, on-disk, read-only
mirror of the exact same events the app-server streams. The `codex.Monitor` interface keeps the
door open for a future app-server-based source.

### B. VS Code extension behaviour — verified

- Extension: `openai.chatgpt-26.908.40401-win32-x64` (bundles Codex core, observed `cli_version`
  `0.154.0-alpha.6.2` in session metadata).
- Sessions created in VS Code are written to the **same** rollout store as CLI sessions and are
  identifiable: `session_meta.payload.originator == "codex_vscode"`, `payload.source == "vscode"`.

### C. Rollout file format (inspected, not assumed)

Location: `%USERPROFILE%\.codex\sessions\YYYY\MM\DD\rollout-<RFC3339 compact>-<thread-uuid>.jsonl`

Each line: `{"timestamp": "...", "ordinal": N, "type": "...", "payload": {...}}`.
Types observed across all 35 local sessions (total ~100k lines):

| line type        | payload.type (event_msg)                                                                 |
|------------------|------------------------------------------------------------------------------------------|
| `session_meta`   | — (session_id, cwd, originator, source, cli_version, git{branch, repository_url})         |
| `turn_context`   | — (cwd, workspace_roots, approval_policy, model, sandbox_policy)                          |
| `event_msg`      | `task_started`, `task_complete`, `turn_aborted`, `item_completed`, `token_count`, `thread_settings_applied` |
| `response_item`  | `reasoning`, `custom_tool_call`, `custom_tool_call_output`, `message`, `function_call`, `function_call_output` |
| others           | `compacted`, `world_state`, `token_usage_record`                                          |

Key facts that drive the state model:

- **Live append**: events are appended *during* a turn, not at its end. Proof: a 4m29s turn shows
  `custom_tool_call` (20:34:35) → `custom_tool_call_output` (20:34:46) → reasoning/item events
  interleaved at second resolution between `task_started` and `task_complete`.
- `task_complete` carries **real** `duration_ms`, `completed_at`, `last_agent_message`.
- `turn_aborted` carries `reason: "interrupted"` (only reason observed; 32 occurrences).
- **No task-level error/failure event exists** in the current format. `item_completed` item types:
  `UserMessage, AgentMessage, FileChange, CommandExecution, McpToolCall, WebSearch, Reasoning,
  ContextCompaction, Extension, Plan, ImageView, FunctionCallOutput`.
  - `CommandExecution` items carry `command[]`, `parsed_cmd[]` (type `read` vs others), `status`,
    `exit_code`, `duration`.
  - `FileChange` items carry a `changes` map `{path: {type: add|update|delete}}`.
  - `McpToolCall` items carry `status` (may be `"failed"`), `server`, `tool`.
- **Live (in-flight) signals** — a tool call appears *before* its output:
  - `response_item/custom_tool_call`, name `exec`, `input` = JS source invoking
    `tools.exec_command({cmd: "...", workdir: ...})`, `tools.shell_command({...})`,
    `tools.apply_patch(...)`, `tools.update_plan({plan: [...]})`, `tools.view_image`, …
  - `response_item/custom_tool_call` name `apply_patch` (direct patch text, rarer in this data).
  - `response_item/function_call` names: `wait`, `sleep`, `update_plan`, `shell_command`,
    `write_stdin`, `request_user_input`, `request_user_input_async`, `view_image`, `exec_command`.
- **Real progress**: `update_plan` arguments contain
  `{"plan":[{"step","status":"pending|in_progress|completed"}...]}` → legitimate `2/5` progress.
- **User attention**: `request_user_input` / `request_user_input_async` (JSON `arguments` with
  `questions[]`) is the explicit "waiting for user" signal.
- `token_count` events carry rate-limit percentages (plan usage) — used by `--doctor` only.
- `~/.codex/session_index.jsonl` maps thread id → `{thread_name, updated_at}` (diagnostics only;
  thread names are conversation metadata and are never shown on the OLED).

Privacy: the OLED shows only operational metadata (state, project, short command/file, elapsed,
plan count). Never message content, prompts, or thread names.

## 2. SteelSeries GameSense (verified on this machine)

- GG 119.0.0 installed, running (`SteelSeriesGG.exe` et al.).
- Endpoint discovery: read `%PROGRAMDATA%\SteelSeries\GG\coreProps.json`
  → `{"address":"127.0.0.1:<port>", "encryptedAddress": ..., "ggEncryptedAddress": ...}`.
  Use the plain `address`. The port changes when GG restarts → re-read the file on every
  (re)connection. Legacy `%PROGRAMDATA%\SteelSeries\SteelSeries Engine 3\coreProps.json` exists
  as a fallback and is checked second.
- The endpoint answers (POST-only API; GET returns 404 — connectivity confirmed live).

Official frame format (gamesense-sdk `doc/api/json-handlers-screen.md`):

- Bitmaps are **1 bit per pixel, packed per row, row-major, origin upper-left, MSB first**.
  128×40 → 640 bytes, sent as a **JSON array of 640 byte values**.
- Register game: `POST /game_metadata` `{"game", "game_display_name", "developer"}`.
- Bind event: `POST /bind_game_event` with handler
  `{"device-type": "screened-128x40", "zone": "one", "mode": "screen",
    "datas": [{"has-text": false, "image-data": [640 bytes]}]}` and `"value_optional": true`.
- Send frame: `POST /game_event`
  `{"game", "event", "data": {"frame": {"image-data-128x40": [640 bytes]}}}`.
- A frame persists until the next one (`length-millis` 0 default) — a status display needs to
  transmit only on change.
- `POST /game_heartbeat` resets GG's game-deactivation timer: without traffic for the
  deinitialize window (default **15 s**, configurable 1–60 s via game_metadata's
  `deinitialize_timer_length_ms`) GG deactivates the game and the device falls back to its
  own configuration. This app registers with the 60 s maximum and heartbeats every 5 s.
- Game id: unique internal string (`codexconnector.oled`), so unrelated GG apps are untouched.

Not copied from any project: the format above comes from the official SDK docs. Open-source
projects (steelclock-go and friends) were consulted only to confirm that dynamic 128×40 frames
work on Apex keyboards in practice.

## 3. Environment facts

- Apex 5 OLED: 128×40 monochrome. GG is the v1 transport; direct HID explicitly out of scope.
- Go: not preinstalled → Go 1.27.1 (windows-amd64) provisioned locally during development.
- Only third-party dependency: `golang.org/x/sys` (Win32 syscall layer, Go-team maintained).
  Tray, file watching, autostart are hand-rolled on top of it (~small, dependency-free elsewhere).

## 4. Detection confidence table

| State            | Signal                                                        | Confidence |
|------------------|---------------------------------------------------------------|------------|
| IDLE             | no open turn                                                  | certain    |
| WORKING          | open turn, no live tool call (model reasoning/generating)     | high       |
| THINKING         | `reasoning` items streaming, no pending tool call             | high       |
| EXECUTING        | pending `exec_command`/`shell_command`/`run` call             | high       |
| READING          | pending call whose command is an unambiguous read (`rg`, `git status`, `cat`, …) | medium |
| EDITING          | pending `apply_patch` call, or patch text in exec input       | high       |
| TESTING          | pending command matches known test-runner patterns            | medium     |
| SEARCHING        | pending `web__run`/web-search call or fresh WebSearch item    | medium     |
| WAITING_FOR_USER | `request_user_input*` issued, turn not continuing             | high       |
| DONE             | `task_complete` (+ real duration)                             | certain    |
| INTERRUPTED      | `turn_aborted`                                                | certain    |
| FAILED           | **no explicit task-level failure event exists**; v1 shows command-level failures (`exit N`) and treats a turn that ends right after a failed command as failed — documented limitation | medium |
| STARTING         | dropped — indistinguishable from THINKING in the data         | —          |

## 5. Failure/done behaviour

- Terminal states (`DONE`, `FAILED`, `STOPPED`) **persist indefinitely**: the full frame
  (repository, frozen action word, rates, task time) stays on the OLED until new Codex
  activity replaces it. A dead turn (crash mid-task, no completion event) falls back to
  IDLE after 3 minutes of silence; pure IDLE blanks after the configured idle timeout.

## Empirical verification (2026-09-17)

Controlled Codex CLI tasks (`codex exec`, scratch directory) were run while the monitor
tailed the session store live. Observed mapping:

- **Task A** (read a file, explain): session created → `task_started` → reasoning → done.
  State: THINKING → DONE with real duration (2.9 s–3.2 s for trivial turns).
- **Task C/J/L** (run a command): live `custom_tool_call exec` with the command extracted
  (`echo final-probe`) → **EXECUTING | scratch | echo final-probe**, then
  `CommandExecution` item → THINKING → DONE. New session files were detected within a
  second of creation; derived state transitions were emitted in real time.
- **Task D** (create and run a test script): command classification produced the EXEC/TEST
  path (the created script ran through `bash check.sh`).
- **Task G** (deliberately failing command): the model observed `exit 1` — note the CLI
  wrapper normalized the intended `exit 3`; the failure surfaced as a nonzero exit code,
  confirming the FAIL-heuristic input (nonzero `exit_code` on `CommandExecution`).
- A live VS Code session (repo "vast") was captured concurrently with real commands
  (`Get-Content … config.json … ConvertFrom-Json`), showing THINKING↔EXECUTING transitions
  and correct VS Code-over-CLI session selection while both were active.

Latency from session-file creation to first detected event: sub-second (directory watcher);
fallback poll interval 2 s. Startup seeding replays only the last task boundary per file.

## 7. Hardware validation status

The GameSense test sequence (all-white → all-black → checkerboard → test card) was accepted
by GG (HTTP 200 on `/game_metadata`, `/bind_game_event`, four `/game_event` posts) on the
discovered endpoint. Physical verification (border/corners/text orientation on the Apex 5)
and tray-menu interaction remain user eye-checks — see README "First run checklist".

## 8. Open items / limitations

- FAILED detection is heuristic (see table) — re-verify with the empirical test matrix; if a
  future Codex version emits an explicit error event, the parser will surface it (unknown
  payload types are logged in debug mode).
- Sessions are file-identity + append-offset based; truncation/rewrite detected via size
  regression and handled by re-reading from 0.
- OLED burn-in mitigated by idle blanking (configurable, default 10 min).
