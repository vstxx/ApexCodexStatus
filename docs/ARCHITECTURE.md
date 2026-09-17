# ACS (ApexCodexStatus) — Architecture

One small Go binary. Data flows strictly left → right; no layer skips a boundary.

```
codex JSONL rollouts (%USERPROFILE%\.codex\sessions)
        │  incremental tail (ReadDirectoryChangesW + size-poll fallback)
        ▼
internal/codex      raw events → parsed codex.Event (untrusted input, content never retained)
        ▼
internal/state      event stream → normalized UIState (status machine + debouncer)
        ▼
internal/render     UIState → 128×40 1-bit framebuffer (font, icons, layouts, truncation)
        ▼
internal/steelseries  framebuffer → 640-byte image-data-128x40 → loopback POST (reconnect/backoff)
        ▲
internal/tray       pause / preview / diagnostics / autostart / exit
internal/config     tiny JSON config, hot-reloaded
internal/app        orchestrator: one event loop, 1 Hz tick, frame hash, send-on-change
```

## Packages

| Package | Responsibility | Notes |
|---------|----------------|-------|
| `cmd/codexconnector` | flag parsing, mode dispatch (tray / console / doctor / preview / testframe / version) | thin |
| `internal/codex` | `Monitor` interface; JSONL implementation: discovery, recursive dir watcher, byte-offset tailer, line parser, tool-call classification, session selection | source-agnostic upstream edge |
| `internal/state` | `Status` enum, `UIState` snapshot, transition + debounce rules, real plan progress, elapsed accounting | pure, deterministic, fully unit-tested |
| `internal/render` | 128×40 `FB`, hand-drawn 5×7 font + icons, per-state layouts, smart truncation, hashing | pure, golden-frame tests |
| `internal/steelseries` | endpoint discovery from coreProps.json, register/bind/send, reconnect with bounded backoff, heartbeat | transport edge |
| `internal/tray` | Win32 Shell_NotifyIconW + popup menu on x/sys/windows; no cgo | Windows-only by design |
| `internal/config` | load/save/validate, `%APPDATA%\CodexConnector\config.json`, change notification | |
| `internal/diagnostics` | `--doctor` checks: GG endpoint, registration, codex dirs, sessions, last activity | |
| `internal/preview` | framebuffer → PNG (scale ×1..×8) for `--preview` and golden tests | |

## Threading model

- One orchestrator goroutine owns all state (single-writer). Inputs are channels:
  codex events, GameSense connection events, tray commands, 1 Hz ticker.
- `codex.Monitor` runs one reader goroutine fed by the OS notification queue.
- `steelseries.Client` serializes HTTP posts; failure → reconnect goroutine with
  exponential backoff (1 s → 60 s cap, jitter) that re-reads coreProps.json.
- No locks in the hot path; everything else is channels.

## Update strategy

Event-driven. Render happens on state change and once per second (elapsed time), the
framebuffer is hashed (FNV-1a over 640 bytes), and identical frames are not transmitted.
No high-frequency polling anywhere; the size-poll fallback runs at 2 s and stats only.

## Windows specifics

- Tray: message-only window + `Shell_NotifyIconW`; menu items map to orchestrator commands.
- Autostart: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` (per-user, no elevation).
- File watching: one recursive `ReadDirectoryChangesW` on the sessions root.
- No admin rights required anywhere.
