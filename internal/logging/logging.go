// Package logging is a deliberately tiny logger: quiet by default, verbose
// only when asked, with a size-capped file sink for tray mode.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level of a log line.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

var levelNames = map[Level]string{Debug: "DEBUG", Info: "INFO", Warn: "WARN", Error: "ERROR"}

// Logger writes to stderr and, once EnableFile is called, to a rotated file.
type Logger struct {
	mu    sync.Mutex
	min   Level
	file  *os.File
	size  int64
	limit int64
	path  string
}

// New returns a logger with the given minimum level.
func New(min Level) *Logger {
	return &Logger{min: min, limit: 256 * 1024}
}

// SetLevel changes the runtime verbosity.
func (l *Logger) SetLevel(min Level) {
	l.mu.Lock()
	l.min = min
	l.mu.Unlock()
}

// EnableFile mirrors output into %LOCALAPPDATA%\CodexConnector\app.log,
// rotated at 256 KB (one backup kept). Failures are silent: logging must
// never take the app down.
func (l *Logger) EnableFile() {
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	dir := filepath.Join(base, "CodexConnector")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = filepath.Join(dir, "app.log")
	l.openFileLocked()
}

func (l *Logger) openFileLocked() {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	l.file = f
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
}

func (l *Logger) rotateLocked() {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	if l.path == "" {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
	l.openFileLocked()
	l.size = 0
}

// Logf emits one line if the level is enabled.
func (l *Logger) Logf(level Level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level < l.min {
		return
	}
	line := fmt.Sprintf("%s %s %s\n",
		time.Now().Format("2006-01-02 15:04:05"), levelNames[level], fmt.Sprintf(format, args...))
	_, _ = io.WriteString(os.Stderr, line)
	if l.file != nil {
		if n, err := io.WriteString(l.file, line); err == nil {
			l.size += int64(n)
			if l.size > l.limit {
				l.rotateLocked()
			}
		}
	}
}

// Debugf logs at debug level.
func (l *Logger) Debugf(format string, args ...any) { l.Logf(Debug, format, args...) }

// Info logs at info level.
func (l *Logger) Infof(format string, args ...any) { l.Logf(Info, format, args...) }

// Warnf logs at warn level.
func (l *Logger) Warnf(format string, args ...any) { l.Logf(Warn, format, args...) }

// Errorf logs at error level.
func (l *Logger) Errorf(format string, args ...any) { l.Logf(Error, format, args...) }
