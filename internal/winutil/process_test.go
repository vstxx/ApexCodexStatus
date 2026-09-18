package winutil

import "testing"

func TestProcessRunning(t *testing.T) {
	if !ProcessRunning("explorer.exe") {
		t.Fatal("explorer.exe should be found on a live desktop session")
	}
	if ProcessRunning("definitely-not-a-real-process-xyz.exe") {
		t.Fatal("bogus process reported as running")
	}
	if ProcessRunning() {
		t.Fatal("empty watch list must not match anything")
	}
}
