package codex

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procReadDirectoryChangesW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadDirectoryChangesW")
	procCancelIoEx            = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelIoEx")
)

// cancelWatchIo aborts a blocked ReadDirectoryChangesW on the watch handle.
// Closing the handle is not enough for synchronous I/O: CloseHandle would
// block until the pending call returns.
func cancelWatchIo(h windows.Handle) {
	if h != 0 {
		procCancelIoEx.Call(uintptr(h), 0)
	}
}

// readDirectoryChangesW is the blocking form; returns bytes filled into buf.
func readDirectoryChangesW(handle windows.Handle, buf []byte, watchSubtree bool, notifyFilter uint32) (uint32, error) {
	var nbytes uint32
	subtree := uintptr(0)
	if watchSubtree {
		subtree = 1
	}
	r1, _, e1 := procReadDirectoryChangesW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		subtree,
		uintptr(notifyFilter),
		uintptr(unsafe.Pointer(&nbytes)),
		0,
		0,
	)
	if r1 == 0 {
		return 0, e1
	}
	return nbytes, nil
}

// watchLoop runs one blocking ReadDirectoryChangesW call after another on
// the sessions root (recursive) until Stop closes the directory handle —
// closing the handle is what aborts the blocked syscall.
func (t *tailer) watchLoop() {
	defer t.done.Done()
	dir, err := syscall.UTF16PtrFromString(t.root)
	if err != nil {
		return
	}
	handle, err := windows.CreateFile(
		dir,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		t.debug("watcher: open %s: %v (falling back to polling)", t.root, err)
		return
	}
	t.mu.Lock()
	t.watchHandle = handle
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.watchHandle = 0
		t.mu.Unlock()
		windows.CloseHandle(handle)
	}()
	// Stop() may have fired between CreateFile and publishing the handle.
	select {
	case <-t.stop:
		return
	default:
	}

	const bufSize = 64 * 1024
	buf := make([]byte, bufSize)
	filter := uint32(windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE |
		windows.FILE_NOTIFY_CHANGE_SIZE)

	for {
		nread, err := readDirectoryChangesW(handle, buf, true, filter)
		select {
		case <-t.stop:
			return
		default:
		}
		if err != nil {
			// ERROR_MORE_DATA (234): the buffer holds valid (truncated)
			// entries; drain them and keep watching.
			if errno, ok := err.(syscall.Errno); ok && errno == 234 && nread > 0 {
				t.parseNotifications(buf[:nread])
				continue
			}
			t.debug("watcher: %v (polling continues at fallback interval)", err)
			return
		}
		t.parseNotifications(buf[:nread])
	}
}

// parseNotifications dispatches FILE_NOTIFY_INFORMATION entries.
func (t *tailer) parseNotifications(b []byte) {
	for len(b) >= 16 {
		next := *(*uint32)(unsafe.Pointer(&b[0]))
		action := *(*uint32)(unsafe.Pointer(&b[4]))
		nameLen := int(*(*uint32)(unsafe.Pointer(&b[8])))
		if 16+nameLen > len(b) {
			break
		}
		name := syscall.UTF16ToString((*[1 << 15]uint16)(unsafe.Pointer(&b[12]))[:nameLen/2])
		if next == 0 {
			b = nil
		} else {
			if int(next) > len(b) {
				break
			}
			b = b[next:]
		}
		path := filepath.Join(t.root, strings.ReplaceAll(name, `\`, string(filepath.Separator)))

		switch action {
		case 0, 4: // FILE_ACTION_ADDED, RENAMED_NEW_NAME
			t.handleFile(path)
		case 2, 3: // FILE_ACTION_MODIFIED, RENAMED_NEW-ish
			t.handleFile(path)
		case 1: // FILE_ACTION_REMOVED
			t.mu.Lock()
			delete(t.files, path)
			t.mu.Unlock()
		}
	}
}
