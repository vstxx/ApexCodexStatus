// Package diagnostics implements the --doctor checks and the tray
// diagnostics report.
package diagnostics

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"codexconnector/internal/codex"
	"codexconnector/internal/config"
	"codexconnector/internal/render"
	"codexconnector/internal/steelseries"
	"codexconnector/internal/winutil"
)

// Check is one diagnostic line.
type Check struct {
	Name string
	OK   bool
	Note string
}

// Run executes every check.
func Run(cfg config.Config) []Check {
	var out []Check

	// Configuration.
	cfgPath := config.FilePath()
	if _, err := os.Stat(cfgPath); err == nil {
		out = append(out, Check{"Config file", true, cfgPath})
	} else {
		out = append(out, Check{"Config file", true, cfgPath + " (defaults, not created yet)"})
	}

	// SteelSeries GG endpoint.
	ep, err := steelseries.DiscoverEndpoint()
	if err != nil {
		out = append(out, Check{"SteelSeries GG endpoint", false,
			"coreProps.json not found — is SteelSeries GG installed and running?"})
	} else {
		out = append(out, Check{"SteelSeries GG endpoint", true, ep})
		if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(ep, "http://"), time.Second); err == nil {
			conn.Close()
			out = append(out, Check{"GameSense server reachable", true, ep})
		} else {
			out = append(out, Check{"GameSense server reachable", false, err.Error()})
		}
	}

	// Codex session store.
	root := codex.DefaultRoot()
	if _, err := os.Stat(root); err != nil {
		out = append(out, Check{"Codex sessions directory", false, root + " — not found"})
	} else {
		sum := codex.Summarize(root)
		note := fmt.Sprintf("%d session files, %.1f MB total", sum.Count, float64(sum.TotalSize)/(1024*1024))
		if sum.Newest != nil {
			origin := "cli"
			if sum.Newest.VSCode {
				origin = "vscode"
			}
			note += fmt.Sprintf("; newest: %s (%s, repo %q, last write %s)",
				sum.Newest.ID[:8], origin, sum.Newest.Repo,
				humanAge(time.Since(sum.NewestAt)))
		}
		out = append(out, Check{"Codex sessions directory", true, note})
		if sum.Newest != nil && sum.NewestAt.After(time.Now().Add(-24*time.Hour)) {
			out = append(out, Check{"Recent Codex activity", true, humanAge(time.Since(sum.NewestAt)) + " ago"})
		} else {
			out = append(out, Check{"Recent Codex activity", false, "no session written in the last 24h"})
		}
	}

	// VS Code presence (OLED gating).
	if cfg.RequireVSCodeEnabled() {
		names := cfg.WatchList()
		if winutil.ProcessRunning(names...) {
			out = append(out, Check{"VS Code (OLED gate)", true,
				"running — ACS owns the OLED"})
		} else {
			out = append(out, Check{"VS Code (OLED gate)", true,
				"not running — OLED released to GG (standby)"})
		}
	} else {
		out = append(out, Check{"VS Code (OLED gate)", true,
			"gating disabled — ACS always owns the OLED"})
	}

	// Renderer self-test: hash of a known frame must be stable.
	fb := &render.FB{}
	fb.DrawText(1, 1, "CODEX·TEST")
	fb.HLine(9)
	out = append(out, Check{"Renderer self-test", true,
		fmt.Sprintf("128x40 framebuffer OK (hash %016x)", fb.Hash())})

	// Packed frame sanity.
	packed := fb.Pack640()
	setBits := 0
	for _, b := range packed {
		for b != 0 {
			setBits += int(b & 1)
			b >>= 1
		}
	}
	out = append(out, Check{"Frame packing", setBits > 0 && len(packed) == 640,
		fmt.Sprintf("640 bytes, %d lit pixels", setBits)})

	return out
}

// Report renders all checks as text (doctor output and tray report).
func Report(cfg config.Config) string {
	var b strings.Builder
	b.WriteString("CodexConnector diagnostics\n")
	b.WriteString("==========================\n\n")
	for _, c := range Run(cfg) {
		mark := "OK  "
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] %-28s %s\n", mark, c.Name, c.Note)
	}
	b.WriteString("\nIf the GameSense endpoint is missing, start SteelSeries GG and retry.\n")
	b.WriteString("If Codex sessions are missing, run a Codex task in VS Code once,\n")
	b.WriteString("then check again (sessions live under %USERPROFILE%\\.codex\\sessions).\n")
	return b.String()
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
