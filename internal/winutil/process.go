package winutil

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessRunning reports whether any process image name matches one of the
// given names (case-insensitive, e.g. "code.exe"). Uses a one-shot process
// snapshot: a few hundred entries, microseconds of work.
func ProcessRunning(imageNames ...string) bool {
	if len(imageNames) == 0 {
		return false
	}
	want := make(map[string]bool, len(imageNames))
	for _, n := range imageNames {
		want[strings.ToLower(n)] = true
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	pe := &windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snap, pe); err != nil {
		return false
	}
	for {
		if want[strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))] {
			return true
		}
		if err := windows.Process32Next(snap, pe); err != nil {
			return false
		}
	}
}
