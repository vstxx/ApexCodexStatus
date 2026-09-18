// Package config loads and stores the tiny JSON configuration file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"codexconnector/internal/state"
)

// Config is the complete on-disk configuration. Keep it small and stable.
type Config struct {
	// How long the DONE frame stays before falling back to idle.
	DoneHoldSeconds int `json:"done_hold_seconds"`
	// How long FAILED/STOPPED frames stay.
	FailHoldSeconds int `json:"fail_hold_seconds"`
	// Minutes of idle before the OLED is blanked (burn-in protection).
	// 0 disables blanking.
	IdleBlankMinutes int `json:"idle_blank_minutes"`
	// Show the wall clock on the idle frame.
	ShowClockIdle bool `json:"show_clock_when_idle"`
	// Fallback poll interval for the session watcher (milliseconds).
	PollIntervalMS int `json:"poll_interval_ms"`
	// Release the OLED while VS Code is not running (default true).
	// With CLI-only setups set this to false to always own the display.
	RequireVSCode *bool `json:"require_vscode"`
	// Processes whose presence keeps ACS engaged (VS Code by default).
	WatchProcesses []string `json:"watch_processes"`
}

// RequireVSCodeEnabled reports whether OLED ownership is gated on the
// watched processes. Missing field defaults to true.
func (c Config) RequireVSCodeEnabled() bool {
	if c.RequireVSCode == nil {
		return true
	}
	return *c.RequireVSCode
}

// WatchList returns the process image names that keep ACS engaged.
func (c Config) WatchList() []string {
	if len(c.WatchProcesses) == 0 {
		return []string{"code.exe", "code - insiders.exe"}
	}
	return c.WatchProcesses
}

// Defaults returns the shipped configuration.
func Defaults() Config {
	return Config{
		DoneHoldSeconds:  12,
		FailHoldSeconds:  25,
		IdleBlankMinutes: 10,
		ShowClockIdle:    true,
		PollIntervalMS:   2000,
	}
}

// FilePath is the per-user configuration location.
func FilePath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "CodexConnector", "config.json")
}

// Load reads the configuration, filling missing fields with defaults.
// A missing file is not an error; a malformed file is.
func Load() (Config, string, error) {
	path := FilePath()
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, path, nil
	}
	if err != nil {
		return cfg, path, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Defaults(), path, err
	}
	cfg.normalize()
	return cfg, path, nil
}

// Save writes the configuration back to disk.
func (c Config) Save() error {
	path := FilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (c *Config) normalize() {
	d := Defaults()
	if c.DoneHoldSeconds <= 0 {
		c.DoneHoldSeconds = d.DoneHoldSeconds
	}
	if c.FailHoldSeconds <= 0 {
		c.FailHoldSeconds = d.FailHoldSeconds
	}
	if c.IdleBlankMinutes < 0 {
		c.IdleBlankMinutes = d.IdleBlankMinutes
	}
	if c.PollIntervalMS < 250 {
		c.PollIntervalMS = d.PollIntervalMS
	}
}

// MachineTimings maps configuration onto state machine timings.
// done_hold_seconds / fail_hold_seconds are accepted for compatibility but
// no longer used: terminal states (DONE/FAIL/STOP) persist until new Codex
// activity replaces them.
func (c Config) MachineTimings() state.MachineConfig {
	mc := state.DefaultMachineConfig()
	_ = c.DoneHoldSeconds
	_ = c.FailHoldSeconds
	return mc
}

// PollInterval converts the millisecond field.
func (c Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalMS) * time.Millisecond
}

// IdleBlank converts the minute field; 0 means disabled.
func (c Config) IdleBlank() time.Duration {
	if c.IdleBlankMinutes <= 0 {
		return 0
	}
	return time.Duration(c.IdleBlankMinutes) * time.Minute
}
