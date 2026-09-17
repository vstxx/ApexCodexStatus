// Command codexconnector is a Windows tray utility that mirrors OpenAI Codex
// activity (VS Code or CLI) onto the SteelSeries Apex 5 OLED via GameSense.
//
// Modes:
//
//	codexconnector              run the tray application (default)
//	codexconnector --console    run the pipeline in the console (no tray)
//	codexconnector --doctor     run diagnostics and print a report
//	codexconnector --preview    render representative OLED frames to PNGs
//	codexconnector --testframe  put a known test frame on the OLED (hardware check)
//	codexconnector --inspect    tail live Codex sessions and print events
//	codexconnector --debug      verbose logging (combined with other modes)
//	codexconnector --version    print version
package main

import (
	"fmt"
	"os"

	"codexconnector/internal/app"
)

func main() {
	args := os.Args[1:]
	debug := flagMode(args, "--debug")

	switch {
	case flagMode(args, "--version"):
		fmt.Println("ACS (ApexCodexStatus)", app.Version)
	case flagMode(args, "--preview"):
		runPreview(flagValue(args, "--out"), flagValue(args, "--state"))
	case flagMode(args, "--doctor"):
		runDoctor(debug)
	case flagMode(args, "--testframe"):
		runTestFrame(debug)
	case flagMode(args, "--inspect"):
		runInspect(debug)
	case flagMode(args, "--console"):
		runConsole(debug)
	default:
		runTray(debug)
	}
}

func flagMode(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
