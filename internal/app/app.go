// Package app wires the pipeline: Codex events -> state machine ->
// framebuffer renderer -> GameSense, plus tray commands and idle blanking.
// A single goroutine owns all mutable state.
package app

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"codexconnector/internal/codex"
	"codexconnector/internal/config"
	"codexconnector/internal/diagnostics"
	"codexconnector/internal/logging"
	"codexconnector/internal/preview"
	"codexconnector/internal/render"
	"codexconnector/internal/state"
	"codexconnector/internal/steelseries"
	"codexconnector/internal/tray"
	"codexconnector/internal/winutil"
)

// Version is overridden by the build (-X flag).
var Version = "dev"

// Options configures Run.
type Options struct {
	Config     config.Config
	ConfigPath string
	Tray       *tray.Tray // nil in console mode
	Log        *logging.Logger
}

// App is the orchestrator.
type App struct {
	log     *logging.Logger
	machine *state.Machine
	client  *steelseries.Client
	mon     codex.Monitor
	paused  atomic.Bool

	mu   sync.Mutex
	cfg  config.Config
	path string

	lastFrame render.FB
}

// Run starts monitoring and blocks until stop is closed or the tray exits.
func Run(opts Options, stop <-chan struct{}) error {
	a := &App{
		log:     opts.Log,
		machine: state.NewMachine(opts.Config.MachineTimings()),
		client:  steelseries.NewClient(),
		cfg:     opts.Config,
		path:    opts.ConfigPath,
	}
	running.Store(a)
	mon, err := codex.NewTailer(codex.MonitorOptions{
		PollInterval: opts.Config.PollInterval(),
		Debugf:       a.log.Debugf,
	})
	if err != nil {
		return err
	}
	a.mon = mon
	defer mon.Stop()

	if opts.Tray != nil {
		opts.Tray.SetPaused(false)
		opts.Tray.SetStatus("ACS: starting", "idle")
		opts.Tray.SetAutostart(winutil.AutostartEnabled())
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	beats := 0       // seconds since the last GameSense heartbeat
	sinceResend := 0 // seconds since the frame was last force-refreshed
	gateSec := 0     // seconds since the last VS Code presence check
	idleSince := time.Now()
	blanked := false
	// Gate check runs before the first frame so a VS Code-less start never
	// touches the OLED.
	vscodeFound := winutil.ProcessRunning(opts.Config.WatchList()...)
	engaged := !opts.Config.RequireVSCodeEnabled() || vscodeFound
	if !engaged {
		a.log.Infof("starting disengaged (VS Code closed): OLED stays with GG")
	}
	var lastUI state.UIState
	var lastHash uint64
	lastTray := ""
	settle := 3 // skip rendering while startup history replay settles

	for {
		select {
		case <-stop:
			return nil
		case cmd := <-a.trayCommands(opts.Tray):
			if a.handleCommand(opts.Tray, cmd) {
				return nil
			}
		case ev := <-mon.Events():
			a.machine.Feed(ev, ev.Time)
		case now := <-ticker.C:
			ui := a.machine.Tick(now)
			cfg := a.currentConfig()
			if settle > 0 {
				settle--
				continue
			}

			// VS Code gating: while the editor is closed the OLED is handed
			// back to GG's own configuration; opening it re-engages ACS.
			gateSec++
			if gateSec >= 5 {
				gateSec = 0
				vscodeFound = winutil.ProcessRunning(cfg.WatchList()...)
			}
			wantEngaged := !cfg.RequireVSCodeEnabled() || vscodeFound

			// Idle blanking (OLED burn-in protection; only while we render).
			blankAfter := cfg.IdleBlank()
			if ui.Status != state.StatusIdle {
				idleSince = now
				blanked = false
			} else if !blanked && blankAfter > 0 && now.Sub(idleSince) >= blankAfter {
				blanked = true
			}

			// Always render the current state — the tray preview stays
			// truthful even while disengaged.
			a.lastFrame = render.FB{}
			render.Layout(&a.lastFrame, ui, now, render.Opts{
				ShowClock: cfg.ShowClockIdle,
				Blanked:   blanked,
			})

			if wantEngaged {
				if !engaged {
					engaged = true
					lastHash = 0
					sinceResend = 0
					a.client.Invalidate()
					a.log.Infof("OLED engaged (%s)", vscodeStateText(vscodeFound, cfg))
				}
				// Connection + frames (EnsureReady applies bounded backoff).
				reRegistered, err := a.client.EnsureReady(now)
				if err != nil {
					a.log.Debugf("gamesense: %v", err)
				} else {
					if reRegistered {
						// Fresh bind shows a blank: force the current frame out.
						lastHash = 0
						a.client.Invalidate()
						sinceResend = 0
					}
					if !a.paused.Load() {
						// Static frames (DONE/FAIL/STOP/IDLE) otherwise never
						// re-send; GG deactivates a game that goes quiet, so
						// push the current frame at least every 30 s.
						sinceResend++
						force := sinceResend >= 30
						if force {
							a.client.Invalidate() // bypass the client-side hash too
						}
						if hash := a.lastFrame.Hash(); force || hash != lastHash {
							if err := a.client.Send(&a.lastFrame, now); err != nil {
								a.log.Warnf("send frame: %v", err)
							} else {
								lastHash = hash
							}
						}
						if force {
							sinceResend = 0
						}
					}

					// Heartbeat every 5 s so GG never falls back to its own
					// configuration while ACS owns the display.
					beats++
					if beats >= 5 {
						beats = 0
						if err := a.client.Heartbeat(); err != nil {
							a.log.Debugf("heartbeat: %v", err)
						}
					}
				}
			} else if engaged {
				engaged = false
				a.client.Disengage()
				a.log.Infof("OLED released to GG (%s)", vscodeStateText(vscodeFound, cfg))
			}

			// Tray reflects engagement as well as Codex state.
			trayText, trayIcon := trayPresentation(ui, engaged)
			if trayText != lastTray {
				if opts.Tray != nil {
					opts.Tray.SetStatus(trayText, trayIcon)
				}
				lastTray = trayText
			}
			if significantChange(ui, lastUI) {
				a.log.Debugf("state: %s (%s | %s | %s)", ui.Status, ui.Project, ui.Action, ui.Detail)
				lastUI = ui
			}
		}
	}
}

// vscodeStateText describes the gate condition for log lines.
func vscodeStateText(found bool, cfg config.Config) string {
	if !cfg.RequireVSCodeEnabled() {
		return "gating disabled"
	}
	if found {
		return "VS Code running"
	}
	return "VS Code closed"
}

// trayPresentation maps the UI state and engagement onto tray text + icon.
func trayPresentation(ui state.UIState, engaged bool) (string, string) {
	icon := trayIconFor(ui.Status)
	if !engaged {
		return "ACS: standby (VS Code closed)", "idle"
	}
	return "ACS: " + ui.Status.String(), icon
}

// significantChange compares only the fields a human would notice; the
// per-tick UpdatedAt timestamp is excluded.
// trayIconFor maps a UI status onto the colored tray icon family.
func trayIconFor(st state.Status) string {
	switch st {
	case state.StatusWaitingUser:
		return "input"
	case state.StatusDone:
		return "done"
	case state.StatusFailed:
		return "fail"
	case state.StatusInterrupted:
		return "stop"
	case state.StatusIdle:
		return "idle"
	}
	return "busy" // every active substate
}

func significantChange(a, b state.UIState) bool {
	return a.Status != b.Status || a.Action != b.Action || a.Detail != b.Detail ||
		a.Project != b.Project || a.Plan != b.Plan
}

// SetConfig swaps display-related settings at runtime (hot reload).
func (a *App) SetConfig(cfg config.Config) {
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
}

// running points at the app instance started by Run, for SetConfigGlobal.
var running atomic.Pointer[App]

// SetConfigGlobal hot-reloads configuration into the running app.
func SetConfigGlobal(cfg config.Config) {
	if a := running.Load(); a != nil {
		a.SetConfig(cfg)
	}
}

func (a *App) currentConfig() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) trayCommands(t *tray.Tray) <-chan tray.Command {
	if t == nil {
		return nil
	}
	return t.Commands()
}

// handleCommand processes a tray command; it returns true when the app
// should exit.
func (a *App) handleCommand(t *tray.Tray, cmd tray.Command) bool {
	switch cmd {
	case tray.CmdPauseToggle:
		p := !a.paused.Load()
		a.paused.Store(p)
		if t != nil {
			t.SetPaused(p)
		}
		if p {
			blank := steelseries.ClearFrame()
			_ = a.client.Send(blank, time.Now())
			a.lastFrame = *blank
		} else {
			// Resuming: push the real frame even if it matches the pre-pause one.
			a.client.Invalidate()
		}
	case tray.CmdPreview:
		path := filepath.Join(os.TempDir(), "codexconnector-oled.png")
		if err := writePreviewFile(path, &a.lastFrame); err != nil {
			a.log.Warnf("preview: %v", err)
		} else {
			_ = winutil.OpenFile(path)
		}
	case tray.CmdDiagnostics:
		report := diagnostics.Report(a.currentConfig())
		path := filepath.Join(os.TempDir(), "codexconnector-doctor.txt")
		if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
			a.log.Warnf("diagnostics: %v", err)
		} else {
			_ = winutil.OpenTextFile(path)
		}
	case tray.CmdOpenConfig:
		if _, err := os.Stat(a.path); os.IsNotExist(err) {
			_ = a.currentConfig().Save()
		}
		_ = winutil.OpenTextFile(a.path)
	case tray.CmdAutostartToggle:
		on := !winutil.AutostartEnabled()
		if err := winutil.SetAutostart(on); err != nil {
			a.log.Warnf("autostart: %v", err)
		}
		if t != nil {
			t.SetAutostart(on)
		}
	case tray.CmdAbout:
		winutil.MessageBox("ACS — ApexCodexStatus "+Version+
			"\n\nShows OpenAI Codex activity from VS Code on the"+
			"\nSteelSeries Apex 5 OLED via SteelSeries GG GameSense."+
			"\n\nRead-only: no Codex files are modified and no"+
			"\nconversation content is ever displayed.",
			"About ACS (ApexCodexStatus)")
	case tray.CmdExit:
		return true
	}
	return false
}

func writePreviewFile(path string, fb *render.FB) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return preview.WritePNG(f, fb, 8)
}
