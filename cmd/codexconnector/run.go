package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"codexconnector/internal/app"
	"codexconnector/internal/config"
	"codexconnector/internal/diagnostics"
	"codexconnector/internal/logging"
	"codexconnector/internal/tray"
	"codexconnector/internal/winutil"
)

// runTray runs the full tray application; blocks until Exit.
func runTray(debug bool) {
	cfg, cfgPath, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		cfg = config.Defaults()
	}
	log := logging.New(logging.Warn)
	if debug {
		log.SetLevel(logging.Debug)
	}
	log.EnableFile()

	if !winutil.SingleInstance() {
		winutil.MessageBox("ACS (ApexCodexStatus) is already running in the notification area.",
			"ACS")
		return
	}

	tray.Debugf = log.Debugf
	tr, err := tray.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tray:", err)
		os.Exit(1)
	}

	stop := make(chan struct{})
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = app.Run(app.Options{
			Config:     cfg,
			ConfigPath: cfgPath,
			Tray:       tr,
			Log:        log,
		}, stop)
	}()

	// Hot-reload display settings when the config file changes.
	go watchConfig(cfgPath, func(c config.Config) {
		app.SetConfigGlobal(c)
	}, stop)

	tr.Run() // message loop, blocks until quit
	close(stop)
	select {
	case <-appDone:
	case <-time.After(3 * time.Second):
	}
}

// runConsole runs the pipeline without the tray, logging state changes.
func runConsole(debug bool) {
	cfg, cfgPath, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		cfg = config.Defaults()
	}
	log := logging.New(logging.Debug) // console mode exists for visibility
	fmt.Println("CodexConnector console mode — Ctrl+C to stop")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Println("\nstopping...")
		close(consoleStop)
	}()
	if err := app.Run(app.Options{Config: cfg, ConfigPath: cfgPath, Log: log}, consoleStop); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var consoleStop = make(chan struct{})

// runDoctor prints the diagnostics report; exit code 1 on failures.
func runDoctor(debug bool) {
	cfg, _, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err, "— using defaults")
		cfg = config.Defaults()
	}
	report := diagnostics.Report(cfg)
	fmt.Print(report)
	if !allPassed(report) {
		os.Exit(1)
	}
}

func allPassed(report string) bool {
	return !strings.Contains(report, "[FAIL]")
}

func watchConfig(path string, onChange func(config.Config), stop <-chan struct{}) {
	var lastMod time.Time
	if st, err := os.Stat(path); err == nil {
		lastMod = st.ModTime()
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !st.ModTime().Equal(lastMod) {
				lastMod = st.ModTime()
				if cfg, _, err := config.Load(); err == nil {
					onChange(cfg)
				}
			}
		}
	}
}
