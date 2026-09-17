// Package tray implements the Windows notification-area icon and its popup
// menu directly on Win32 (no cgo, no third-party tray library).
package tray

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowClass = "CodexConnectorTray"
	callbackMsg = 0x8FFF // custom callback message
	iconID      = 1

	hwndMessage = ^uintptr(2) // (HWND)-3: message-only window parent
)

// Menu command IDs.
const (
	cmdStatus = 100 + iota
	cmdSep1
	cmdPause
	cmdPreview
	cmdDiag
	cmdConfig
	cmdAutostart
	cmdSep2
	cmdAbout
	cmdExit
)

// Command is a menu selection.
type Command int

const (
	CmdPauseToggle Command = iota
	CmdPreview
	CmdDiagnostics
	CmdOpenConfig
	CmdAutostartToggle
	CmdAbout
	CmdExit
)

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	user32            = windows.NewLazySystemDLL("user32.dll")
	procNotifyIcon    = shell32.NewProc("Shell_NotifyIconW")
	procCreateMenu    = user32.NewProc("CreatePopupMenu")
	procAppendMenu    = user32.NewProc("AppendMenuW")
	procTrackMenu     = user32.NewProc("TrackPopupMenu")
	procDestroyMenu   = user32.NewProc("DestroyMenu")
	procPostQuit      = user32.NewProc("PostQuitMessage")
	procDefWindow     = user32.NewProc("DefWindowProcW")
	procRegisterCls   = user32.NewProc("RegisterClassExW")
	procCreateWindow  = user32.NewProc("CreateWindowExW")
	procGetMessage    = user32.NewProc("GetMessageW")
	procTranslate     = user32.NewProc("TranslateMessage")
	procDispatch      = user32.NewProc("DispatchMessageW")
	procSetForeground = user32.NewProc("SetForegroundWindow")
	procLoadIcon      = user32.NewProc("LoadIconW")
	procGetModHandle  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procGetSystemMet  = user32.NewProc("GetSystemMetrics")
	procGetCursorPos  = user32.NewProc("GetCursorPos")
)

// Debugf is set by the embedding app to trace tray events (clicks, menu).
var Debugf func(format string, args ...any)

type pointW struct {
	X, Y int32
}

// notifyIconDataW mirrors the Windows structure (winuser.h).
type notifyIconDataW struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

const (
	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 1
	nifIcon    = 2
	nifTip     = 4
)

// Tray owns the notification icon. The message loop must run on the thread
// that created it; the rest of the app uses the channel API.
type Tray struct {
	commands chan Command

	mu        sync.Mutex
	status    string
	paused    bool
	autostart bool
	hwnd      windows.HWND
	icon      uintptr
	icons     map[string]uintptr // cached per-status HICONs
	kind      string             // current icon kind
}

// Commands returns the menu command stream.
func (t *Tray) Commands() <-chan Command { return t.commands }

// SetStatus updates the tooltip and the status icon. The icon kinds are the
// keys of iconColors ("busy", "done", "input", "fail", "stop", "idle").
func (t *Tray) SetStatus(text, iconKind string) {
	t.mu.Lock()
	t.status = text
	if _, ok := iconColors[iconKind]; !ok {
		iconKind = "idle"
	}
	t.kind = iconKind
	t.mu.Unlock()
	t.modifyIcon()
}

// SetPaused updates the pause checkbox.
func (t *Tray) SetPaused(p bool) {
	t.mu.Lock()
	t.paused = p
	t.mu.Unlock()
}

// SetAutostart updates the autostart checkbox.
func (t *Tray) SetAutostart(on bool) {
	t.mu.Lock()
	t.autostart = on
	t.mu.Unlock()
}

// New creates the message-only window and the tray icon. Call Run next.
func New() (*Tray, error) {
	t := &Tray{commands: make(chan Command, 16)}

	var wc wndClassEx
	className, _ := windows.UTF16PtrFromString(windowClass)
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = windows.NewCallback(syscallWndProc)
	wc.LpszClassName = className
	wc.HInstance, _, _ = procGetModHandle.Call(0) // returns uintptr; field is Handle-typed below
	if r, _, _ := procRegisterCls.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("RegisterClassExW failed")
	}
	title, _ := windows.UTF16PtrFromString("ACS — ApexCodexStatus")
	// A normal hidden popup window (not HWND_MESSAGE): TrackPopupMenu needs
	// a window that can take foreground, which message-only windows cannot.
	const wsPopup = 0x80000000
	hwnd, _, err := procCreateWindow.Call(
		0,                                  // exStyle
		uintptr(unsafe.Pointer(className)), // class
		uintptr(unsafe.Pointer(title)),     // title
		wsPopup, 0, 0, 0, 0,                // style (hidden), x, y, w, h
		0,                                  // parent
		0,                                  // menu
		uintptr(wc.HInstance),
		0, // param
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW failed: %v", err)
	}
	t.hwnd = windows.HWND(hwnd)
	t.icons = make(map[string]uintptr)
	t.icon = t.statusHICON("idle")
	if err := t.addIcon(); err != nil {
		return nil, err
	}
	wndProcTarget = t
	return t, nil
}

// Run pumps the Win32 message loop; it blocks the calling thread.
func (t *Tray) Run() {
	var msg msgW
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslate.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// Close removes the icon and stops the message loop.
func (t *Tray) Close() {
	t.deleteIcon()
	t.mu.Lock()
	for _, h := range t.icons {
		procDestroyIcon.Call(h)
	}
	t.icons = nil
	t.mu.Unlock()
	procPostQuit.Call(0)
}

func (t *Tray) addIcon() error {
	var nid notifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = t.hwnd
	nid.UID = iconID
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = callbackMsg
	nid.HIcon = t.icon
	tip, _ := windows.UTF16FromString("ACS (ApexCodexStatus) — starting")
	copy(nid.SzTip[:], tip)
	r, _, _ := procNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return fmt.Errorf("Shell_NotifyIconW add failed")
	}
	return nil
}

func (t *Tray) modifyIcon() {
	t.mu.Lock()
	status, kind, cached := t.status, t.kind, t.icons
	t.mu.Unlock()

	// Swap the status icon when the kind changed (or is unknown to cache).
	hicon := uintptr(0)
	if h, ok := cached[kind]; ok {
		hicon = h
	} else {
		hicon = t.statusHICON(kind)
		t.mu.Lock()
		t.icons[kind] = hicon
		t.mu.Unlock()
	}

	var nid notifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = t.hwnd
	nid.UID = iconID
	nid.UFlags = nifIcon | nifTip
	nid.HIcon = hicon
	tip, _ := windows.UTF16FromString(status)
	copy(nid.SzTip[:], tip)
	procNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// statusHICON renders (and caches) the HICON for one icon kind. Caller does
// not need to free it — Close destroys the whole cache.
func (t *Tray) statusHICON(kind string) uintptr {
	if h, ok := t.icons[kind]; ok {
		return h
	}
	sm, _, _ := procGetSystemMet.Call(49) // SM_CXSMICON
	size := int(int32(sm))
	if size <= 0 {
		size = 16
	}
	return hiconFromBGRA(size, DrawStatusIcon(kind, size))
}

func (t *Tray) deleteIcon() {
	var nid notifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = t.hwnd
	nid.UID = iconID
	procNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

// showMenu builds and tracks the popup menu; returns the chosen command.
func (t *Tray) showMenu() Command {
	const CmdNone = Command(0)
	t.mu.Lock()
	status, paused, autostart := t.status, t.paused, t.autostart
	t.mu.Unlock()

	menu, _, _ := procCreateMenu.Call()
	if menu == 0 {
		return 0 // none
	}
	defer procDestroyMenu.Call(menu)

	pauseLabel := "Pause display"
	if paused {
		pauseLabel = "Resume display"
	}
	appendItem(menu, cmdStatus, status, true, false)
	separator(menu)
	appendItem(menu, cmdPause, pauseLabel, false, paused)
	appendItem(menu, cmdPreview, "OLED preview", false, false)
	appendItem(menu, cmdDiag, "Diagnostics", false, false)
	appendItem(menu, cmdConfig, "Open config", false, false)
	appendItem(menu, cmdAutostart, "Autostart", false, autostart)
	separator(menu)
	appendItem(menu, cmdAbout, "About", false, false)
	appendItem(menu, cmdExit, "Exit", false, false)

	procSetForeground.Call(uintptr(t.hwnd)) // dismiss on outside click
	var pt pointW
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	const (
		tpmRightButton = 2
		tpmReturnCmd   = 0x100
	)
	if Debugf != nil {
		Debugf("tray: showing menu at (%d,%d)", pt.X, pt.Y)
	}
	r, _, _ := procTrackMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y),
		0, // reserved
		uintptr(t.hwnd),
		0, // rect
	)
	switch int(r) {
	case cmdPause:
		return CmdPauseToggle
	case cmdPreview:
		return CmdPreview
	case cmdDiag:
		return CmdDiagnostics
	case cmdConfig:
		return CmdOpenConfig
	case cmdAutostart:
		return CmdAutostartToggle
	case cmdAbout:
		return CmdAbout
	case cmdExit:
		return CmdExit
	}
	return 0 // CmdNone
}

func appendItem(menu uintptr, id int, label string, disabled, checked bool) {
	flags := uintptr(0) // MF_STRING
	if disabled {
		flags |= 0x1 | 0x3 // MF_DISABLED | MF_GRAYED
	}
	if checked {
		flags |= 0x8 // MF_CHECKED
	}
	text, _ := windows.UTF16PtrFromString(label)
	procAppendMenu.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(text)))
}

func separator(menu uintptr) {
	procAppendMenu.Call(menu, 0x800, 0, 0) // MF_SEPARATOR
}

// wndClassEx / msgW mirror the Win32 structures we need.
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msgW struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct {
		X, Y int32
	}
	LPrivate uint32
}

var wndProcTarget *Tray

func syscallWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	if wndProcTarget != nil && msg == callbackMsg {
		if Debugf != nil {
			Debugf("tray: callback message lParam=0x%x", lParam)
		}
		switch uint32(lParam) & 0xFFFF {
		case 0x0202, 0x0205: // WM_LBUTTONUP, WM_RBUTTONUP
			cmd := wndProcTarget.showMenu()
			if Debugf != nil {
				Debugf("tray: menu returned %d", int(cmd))
			}
			if cmd != 0 {
				select {
				case wndProcTarget.commands <- cmd:
				default:
				}
			}
		}
		return 0
	}
	r, _, _ := procDefWindow.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}
