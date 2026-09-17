package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// DefaultRoot is the standard Codex session store.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// Monitor streams normalized Codex events from the JSONL rollout store.
type Monitor interface {
	Events() <-chan Event
	Stop()
}

// tailer is the JSONL Monitor implementation: a recursive directory watcher
// plus per-file byte offsets, with a slow poll as a safety net.
type tailer struct {
	root        string
	events      chan Event
	watchHandle windows.Handle

	mu         sync.Mutex
	files      map[string]*trackedFile
	stop       chan struct{}
	done       sync.WaitGroup
	ratesSeen  [2]int // last emitted 5H/weekly left percents (dedupe)
	ratesValid bool

	// handleMu serializes handleFile: the watcher goroutine and the poller
	// can both fire for the same file, and interleaved offset-based reads
	// would emit duplicated events.
	handleMu sync.Mutex

	debug func(format string, args ...any)
}

type trackedFile struct {
	offset int64
	meta   *SessionMeta
}

// MonitorOptions tunes the tailer.
type MonitorOptions struct {
	Root         string
	PollInterval time.Duration // fallback scan cadence; 0 = 2s
	Debugf       func(format string, args ...any)
}

// NewTailer starts monitoring the Codex sessions tree. It replays recent
// history enough to establish the current state, then tails incrementally.
func NewTailer(opts MonitorOptions) (Monitor, error) {
	root := opts.Root
	if root == "" {
		root = DefaultRoot()
	}
	if root == "" {
		return nil, errNoHome
	}
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	t := &tailer{
		root:   root,
		events: make(chan Event, 1024),
		files:  make(map[string]*trackedFile),
		stop:   make(chan struct{}),
		debug:  opts.Debugf,
	}
	if t.debug == nil {
		t.debug = func(string, ...any) {}
	}
	// The initial scan emits events, so it must run concurrently with the
	// consumer — otherwise a large replay would deadlock on the buffer.
	t.done.Add(3)
	go t.watchLoop()
	go t.pollLoop(opts.pollIntervalOrDefault())
	go func() {
		defer t.done.Done()
		t.scanExisting()
	}()
	return t, nil
}

func (o MonitorOptions) pollIntervalOrDefault() time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}
	return 2 * time.Second
}

// Events returns the event stream.
func (t *tailer) Events() <-chan Event { return t.events }

// Stop shuts down the watcher and poller. The blocked ReadDirectoryChangesW
// call is aborted via CancelIoEx; the wait is bounded so shutdown always
// returns promptly.
func (t *tailer) Stop() {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	t.mu.Lock()
	h := t.watchHandle
	t.mu.Unlock()
	cancelWatchIo(h)
	finished := make(chan struct{})
	go func() {
		t.done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		// Extremely rare race (cancel landing before the blocking call);
		// the watcher goroutine will exit on its next event. The process
		// exits regardless.
		t.debug("stop: watcher goroutine did not confirm within 3s")
	}
}

// scanExisting seeds state from files already on disk: metadata for every
// session, recent events for anything modified in the last day.
func (t *tailer) scanExisting() {
	type found struct {
		path  string
		mtime time.Time
		size  int64
	}
	var all []found
	_ = filepath.WalkDir(t.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			all = append(all, found{path, info.ModTime(), info.Size()})
		}
		return nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].mtime.After(all[j].mtime) })

	cutoff := time.Now().Add(-24 * time.Hour)
	for i, f := range all {
		t.ensureMeta(f.path)
		if i < 3 || f.mtime.After(cutoff) {
			t.readRecentHistory(f.path, f.size)
		} else {
			// Old, quiet file: park the offset at EOF; it must not emit.
			// (ensureMeta already created the entry with offset 0.)
			t.mu.Lock()
			tf := t.files[f.path]
			if tf == nil {
				tf = &trackedFile{}
				t.files[f.path] = tf
			}
			tf.offset = f.size
			t.mu.Unlock()
		}
	}
}

// readRecentHistory replays the tail of a file (bounded) to establish the
// current turn state without parsing the whole history. Only events from the
// last task boundary (task_started/complete/aborted) onward are parsed —
// anything earlier cannot affect the current state. Turns longer than the
// window are reconstructed by locating the last boundary line backwards in
// the file (cheap byte scan) and synthesizing that single event.
func (t *tailer) readRecentHistory(path string, size int64) {
	const window = 256 * 1024
	start := size - window
	if start < 0 {
		start = 0
	}
	f, err := os.Open(path)
	if err != nil {
		t.debug("read %s: %v", path, err)
		return
	}
	defer f.Close()
	if start > 0 {
		// Snap past a partial line at the window start. Read a chunk before
		// the window and scan it in memory: a byte-wise ReadAt loop would
		// take minutes to cross a multi-megabyte line.
		const lookback = 256 * 1024
		chunkStart := start - lookback
		if chunkStart < 0 {
			chunkStart = 0
		}
		chunk := make([]byte, start-chunkStart)
		n, err := f.ReadAt(chunk, chunkStart)
		switch {
		case err != nil && n == 0:
			start = 0
		default:
			if i := lastByteIndex(chunk[:n], '\n'); i >= 0 {
				start = chunkStart + int64(i) + 1
			} else {
				start = 0 // no line boundary in the lookback: parse from scratch
			}
		}
	}
	data := make([]byte, size-start)
	n, err := f.ReadAt(data, start)
	if err != nil && n == 0 {
		t.debug("readAt %s: %v", path, err)
		return
	}
	data = data[:n]

	// Find the last task boundary and replay only from there.
	if b := lastTaskBoundary(data); b >= 0 {
		start += int64(b)
		data = data[b:]
	} else if bl := findBoundaryBackward(f, start, size); bl != nil {
		// The boundary lies beyond the window (a turn longer than 256 KB):
		// synthesize the single boundary event so the turn state is still
		// reconstructed, then parse the window tail for the latest substate.
		t.debug("seed %s: boundary beyond window at offset %d", filepath.Base(path), bl.start)
		for _, ev := range ParseLine(bl.line) {
			if ev.SessionID == "" {
				ev.SessionID = t.sessionIDFor(path)
			}
			if ev.SessionID != "" {
				t.emit(ev)
			}
		}
	} else {
		// No boundary in the whole file: nothing usable for state; skip
		// parsing entirely but keep the offset current.
		t.mu.Lock()
		tf := t.files[path]
		if tf == nil {
			tf = &trackedFile{}
			t.files[path] = tf
		}
		tf.offset = start + int64(len(data))
		t.mu.Unlock()
		return
	}
	t.debug("seed %s: replaying %d bytes from offset %d", filepath.Base(path), len(data), start)
	t.consume(path, data, start)
}

// boundaryLine describes the location and content of the last task boundary
// line found scanning a file backwards.
type boundaryLine struct {
	start int64
	line  []byte
}

const backwardScanLimit = 16 << 20 // give up beyond 16 MB of searching

// findBoundaryBackward scans the region [0, before) backwards in chunks for
// the last task boundary line. Only a cheap byte search runs here — no JSON
// parsing — so multi-megabyte turns cost milliseconds.
func findBoundaryBackward(f *os.File, before, size int64) *boundaryLine {
	const chunkSize = 1 << 20
	patterns := [][]byte{
		[]byte(`"type":"task_started"`),
		[]byte(`"type":"task_complete"`),
		[]byte(`"type":"turn_aborted"`),
	}
	chunkEnd := before
	limit := before - backwardScanLimit
	if limit < 0 {
		limit = 0
	}
	for chunkEnd > limit {
		chunkStart := chunkEnd - chunkSize
		if chunkStart < limit {
			chunkStart = limit
		}
		chunk := make([]byte, chunkEnd-chunkStart)
		n, err := f.ReadAt(chunk, chunkStart)
		if err != nil && n == 0 {
			return nil
		}
		chunk = chunk[:n]
		hit := -1
		for _, p := range patterns {
			if i := bytes.LastIndex(chunk, p); i > hit {
				hit = i
			}
		}
		if hit >= 0 {
			// Snap to the start of the line containing the hit.
			lineStart := lastByteIndex(chunk[:hit], '\n') + 1 // -1+1 = 0 if none
			abs := chunkStart + int64(lineStart)
			lineEndAbs := chunkStart + int64(indexByte(chunk[lineStart:], '\n'))
			if indexByte(chunk[lineStart:], '\n') < 0 {
				lineEndAbs = size
			}
			if lineEndAbs <= abs {
				return nil // malformed; give up rather than mis-report
			}
			line := make([]byte, lineEndAbs-abs)
			rn, rerr := f.ReadAt(line, abs)
			if rerr != nil && rn == 0 {
				return nil
			}
			return &boundaryLine{start: abs, line: line[:rn]}
		}
		chunkEnd = chunkStart
	}
	return nil
}

// lastTaskBoundary returns the byte offset of the last task boundary line
// inside data (already-read window), or -1 when absent. The boundary line
// itself is replayed too — it defines the state.
func lastTaskBoundary(data []byte) int {
	patterns := [][]byte{
		[]byte(`"type":"task_started"`),
		[]byte(`"type":"task_complete"`),
		[]byte(`"type":"turn_aborted"`),
	}
	offset := -1
	for lineStart := 0; lineStart < len(data); {
		lineEnd := lineStart + indexByte(data[lineStart:], '\n')
		if lineEnd < lineStart {
			lineEnd = len(data)
		}
		line := data[lineStart:lineEnd]
		for _, p := range patterns {
			if bytes.Contains(line, p) {
				offset = lineStart // remember the LAST boundary seen
				break
			}
		}
		if lineEnd >= len(data) {
			break
		}
		lineStart = lineEnd + 1
	}
	return offset
}

// headLine reads the first line of a file (up to 256KB) — session_meta lines
// embed the full base instructions and can be tens of KB.
func headLine(f *os.File) []byte {
	var all []byte
	buf := make([]byte, 32*1024)
	for len(all) < 256*1024 {
		n, err := f.Read(buf)
		if n > 0 {
			all = append(all, buf[:n]...)
			if i := indexByte(buf[:n], '\n'); i >= 0 {
				return all[:len(all)-n+i]
			}
		}
		if err != nil {
			return all
		}
	}
	return all
}

// ensureMeta reads the session_meta line from the head of a file and emits
// it so downstream state tracking knows the session's identity from startup.
func (t *tailer) ensureMeta(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	head := headLine(f)
	if len(head) == 0 {
		return
	}
	for _, ev := range ParseLine(head) {
		if ev.Kind == EvSessionMeta && ev.Meta != nil {
			t.mu.Lock()
			tf := t.files[path]
			if tf == nil {
				tf = &trackedFile{}
				t.files[path] = tf
			}
			tf.meta = ev.Meta
			t.mu.Unlock()
			t.emit(ev)
		}
	}
}

// consume parses complete JSONL lines from data and emits events; returns
// the offset to resume from.
func (t *tailer) consume(path string, data []byte, start int64) {
	var consumed int64
	sessID := t.sessionIDFor(path)
	for len(data) > 0 {
		i := indexByte(data, '\n')
		if i < 0 {
			break // partial line: wait for the rest
		}
		lineData := data[:i]
		data = data[i+1:]
		consumed += int64(i) + 1

		for _, ev := range ParseLine(lineData) {
			if ev.Kind == EvSessionMeta {
				// ensureMeta already announced this file's meta at discovery.
				t.mu.Lock()
				tf := t.files[path]
				already := tf != nil && tf.meta != nil && tf.meta.ID == ev.SessionID
				if tf != nil {
					tf.meta = ev.Meta
				}
				t.mu.Unlock()
				if !already {
					t.emit(ev)
				}
				sessID = ev.SessionID
				continue
			}
			if sessID == "" {
				continue // pre-meta event: useless without a session identity
			}
			if ev.SessionID == "" {
				ev.SessionID = sessID
			}
			if ev.Kind == EvRates && !t.shouldEmitRates(ev.Rates) {
				continue // unchanged account-wide values; skip the flood
			}
			t.emit(ev)
		}
	}
	t.mu.Lock()
	tf := t.files[path]
	if tf == nil {
		tf = &trackedFile{}
		t.files[path] = tf
	}
	tf.offset = start + consumed
	t.mu.Unlock()
}

func (t *tailer) sessionIDFor(path string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tf := t.files[path]; tf != nil && tf.meta != nil {
		return tf.meta.ID
	}
	return ""
}

// shouldEmitRates dedupes account-wide rate-limit values so the frequent
// token_count stream only produces events when a percentage changes.
func (t *tailer) shouldEmitRates(r RateLimits) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := [2]int{r.FiveHourLeft, r.WeeklyLeft}
	if t.ratesValid && next == t.ratesSeen {
		return false
	}
	t.ratesSeen = next
	t.ratesValid = true
	return true
}

func (t *tailer) emit(ev Event) {
	select {
	case t.events <- ev:
	case <-t.stop:
	}
}

// handleFile tail-reads one file if it grew.
func (t *tailer) handleFile(path string) {
	if !strings.HasSuffix(path, ".jsonl") {
		return
	}
	t.handleMu.Lock()
	defer t.handleMu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return // transient; the poller will retry
	}
	t.mu.Lock()
	tf, tracked := t.files[path]
	t.mu.Unlock()
	if !tracked {
		t.ensureMeta(path)
		t.mu.Lock()
		tf, tracked = t.files[path]
		t.mu.Unlock()
		if !tracked {
			tf = &trackedFile{}
			t.mu.Lock()
			t.files[path] = tf
			t.mu.Unlock()
		}
		if info.Size() > 1024*1024 {
			// Joined mid-session: take recent history only.
			t.readRecentHistory(path, info.Size())
			return
		}
	}
	if info.Size() < tf.offset {
		// Rewritten/truncated: start over.
		t.mu.Lock()
		tf.offset = 0
		t.mu.Unlock()
	}
	if info.Size() == tf.offset {
		return
	}
	t.debug("append %s: %d new bytes", filepath.Base(path), info.Size()-tf.offset)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	data := make([]byte, info.Size()-tf.offset)
	n, err := f.ReadAt(data, tf.offset)
	if err != nil && n == 0 {
		t.debug("readAt %s: %v", path, err)
		return
	}
	t.consume(path, data[:n], tf.offset)
}

// pollLoop is the safety net that catches anything the watcher misses.
func (t *tailer) pollLoop(interval time.Duration) {
	defer t.done.Done()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			t.pollOnce()
		}
	}
}

func (t *tailer) pollOnce() {
	var latest []string
	_ = filepath.WalkDir(t.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		latest = append(latest, path)
		return nil
	})
	t.mu.Lock()
	known := make(map[string]bool, len(t.files))
	for p := range t.files {
		known[p] = true
	}
	t.mu.Unlock()
	for _, p := range latest {
		delete(known, p)
		t.handleFile(p)
	}
	// Files that vanished: drop tracking (session cleanup).
	if len(known) > 0 {
		t.mu.Lock()
		for p := range known {
			delete(t.files, p)
		}
		t.mu.Unlock()
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// lastByteIndex returns the index of the last occurrence of c in b.
func lastByteIndex(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}

var errNoHome = errString("cannot determine home directory")

type errString string

func (e errString) Error() string { return string(e) }

// SessionsSummary describes discovered sessions for diagnostics.
type SessionsSummary struct {
	Root      string
	Count     int
	VSCode    int
	TotalSize int64
	Newest    *SessionMeta
	NewestAt  time.Time
}

// Summarize walks the session store for doctor output (cheap, head-only).
func Summarize(root string) SessionsSummary {
	if root == "" {
		root = DefaultRoot()
	}
	sum := SessionsSummary{Root: root}
	var newestPath string
	var newestAt time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		sum.Count++
		if info, err := d.Info(); err == nil {
			sum.TotalSize += info.Size()
			if info.ModTime().After(newestAt) {
				newestAt = info.ModTime()
				newestPath = path
			}
		}
		return nil
	})
	if newestPath != "" {
		sum.NewestAt = newestAt
		if meta := headMeta(newestPath); meta != nil {
			sum.Newest = meta
			if meta.VSCode {
				sum.VSCode = 1
			}
		}
	}
	return sum
}

func headMeta(path string) *SessionMeta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := headLine(f)
	if len(head) == 0 {
		return nil
	}
	var l struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(head, &l) != nil {
		return nil
	}
	for _, ev := range parseSessionMeta(line{Timestamp: time.Now(), Payload: l.Payload}) {
		return ev.Meta
	}
	return nil
}
