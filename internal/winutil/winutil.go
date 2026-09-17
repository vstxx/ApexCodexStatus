// Package winutil wraps the few Win32 facilities the tray app needs:
// opening files with the shell, message boxes and per-user autostart.
package winutil

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32        = windows.NewLazySystemDLL("shell32.dll")
	user32         = windows.NewLazySystemDLL("user32.dll")
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procShellExec  = shell32.NewProc("ShellExecuteW")
	procMessageBox = user32.NewProc("MessageBoxW")
	procCreateMut  = kernel32.NewProc("CreateMutexW")
)

// SingleInstance returns true when no other tray instance is running. It
// creates a named mutex held for the process lifetime; a second launch gets
// false (ERROR_ALREADY_EXISTS).
func SingleInstance() bool {
	const errorAlreadyExists = 183
	h, _, err := procCreateMut.Call(0, 0, utf16Ptr(`Local\CodexConnector.SingleInstance`))
	if h == 0 {
		return true // cannot tell; assume we are alone rather than block
	}
	runtime.KeepAlive(h) // never released: the mutex lives as long as we do
	return err != syscall.Errno(errorAlreadyExists)
}

func utf16Ptr(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// OpenFile opens a file with its default association.
func OpenFile(path string) error {
	r, _, _ := procShellExec.Call(0, utf16Ptr("open"), utf16Ptr(path), 0, 0, 5 /*SW_SHOW*/)
	if r <= 32 {
		return os.ErrInvalid
	}
	return nil
}

// OpenTextFile opens a file in Notepad (deterministic, always present).
func OpenTextFile(path string) error {
	r, _, _ := procShellExec.Call(0, utf16Ptr("open"), utf16Ptr("notepad.exe"), utf16Ptr(path), 0, 5)
	if r <= 32 {
		return os.ErrInvalid
	}
	return nil
}

// MessageBox shows a simple info dialog.
func MessageBox(text, title string) {
	procMessageBox.Call(0, utf16Ptr(text), utf16Ptr(title), 0x40 /*MB_ICONINFORMATION*/)
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// AutostartEnabled reports whether the per-user Run entry exists.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("CodexConnector")
	return err == nil
}

// SetAutostart adds or removes the per-user Run entry (no elevation).
func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue("CodexConnector")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("CodexConnector", `"`+exe+`"`)
}
