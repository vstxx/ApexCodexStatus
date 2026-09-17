package steelseries

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexconnector/internal/render"
)

func TestDiscoverEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROGRAMDATA", dir)
	ggDir := filepath.Join(dir, "SteelSeries", "GG")
	if err := os.MkdirAll(ggDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ggDir, "coreProps.json"),
		[]byte(`{"address":"127.0.0.1:59626","encryptedAddress":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ep, err := DiscoverEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if ep != "http://127.0.0.1:59626" {
		t.Errorf("endpoint = %q", ep)
	}
}

func TestDiscoverEndpointMissing(t *testing.T) {
	t.Setenv("PROGRAMDATA", t.TempDir())
	if _, err := DiscoverEndpoint(); err == nil {
		t.Fatal("expected error with no coreProps.json")
	}
}

// recordingServer captures GameSense posts so the wire format can be pinned.
type recordingServer struct {
	*httptest.Server
	mu        chan chan<- request
	requested chan request
}

type request struct {
	path string
	body string
}

func newRecordingServer(t *testing.T, failFirst int) *recordingServer {
	rs := &recordingServer{requested: make(chan request, 32)}
	var calls int
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		body := string(buf[:n])
		// Body may exceed one Read; drain the rest.
		var extra [1024]byte
		for {
			m, err := r.Body.Read(extra[:])
			body += string(extra[:m])
			if err != nil {
				break
			}
		}
		calls++
		if calls <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		select {
		case rs.requested <- request{path: r.URL.Path, body: body}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rs.Server.Close)
	return rs
}

func TestRegistrationAndFrameFormat(t *testing.T) {
	rs := newRecordingServer(t, 0)
	c := NewClient()
	c.discover = func() (string, error) { return rs.URL, nil }

	if _, err := c.EnsureReady(time.Now()); err != nil {
		t.Fatal(err)
	}

	// First request: game registration.
	req := <-rs.requested
	if req.path != "/game_metadata" {
		t.Fatalf("path = %s", req.path)
	}
	if !strings.Contains(req.body, `"game":"`+Game+`"`) {
		t.Errorf("metadata body = %s", req.body)
	}

	// Second: handler binding with the exact 128x40 screened handler.
	req = <-rs.requested
	if req.path != "/bind_game_event" {
		t.Fatalf("path = %s", req.path)
	}
	for _, want := range []string{
		`"device-type":"screened-128x40"`,
		`"zone":"one"`,
		`"mode":"screen"`,
		`"has-text":false`,
		`"value_optional":true`,
	} {
		if !strings.Contains(req.body, want) {
			t.Errorf("bind body missing %s: %s", want, req.body)
		}
	}
	var bind struct {
		Handlers []struct {
			Datas []struct {
				ImageData []int `json:"image-data"`
			} `json:"datas"`
		} `json:"handlers"`
	}
	if err := json.Unmarshal([]byte(req.body), &bind); err != nil {
		t.Fatalf("bind json: %v", err)
	}
	if n := len(bind.Handlers[0].Datas[0].ImageData); n != 640 {
		t.Fatalf("bound image-data length = %d", n)
	}

	// Sending a frame produces the dynamic frame payload.
	fb := &render.FB{}
	fb.Set(0, 0)
	fb.Set(127, 39)
	if err := c.Send(fb, time.Now()); err != nil {
		t.Fatal(err)
	}
	req = <-rs.requested
	if req.path != "/game_event" {
		t.Fatalf("path = %s", req.path)
	}
	var frame struct {
		Data struct {
			Frame map[string][]int `json:"frame"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(req.body), &frame); err != nil {
		t.Fatalf("frame json: %v", err)
	}
	pix, ok := frame.Data.Frame["image-data-128x40"]
	if !ok {
		t.Fatalf("frame key missing: %s", req.body)
	}
	if len(pix) != 640 {
		t.Fatalf("frame length = %d", len(pix))
	}
	if pix[0] != 0x80 || pix[639] != 0x01 {
		t.Errorf("packing wrong: %d %d", pix[0], pix[639])
	}

	// An identical frame is not re-sent.
	if err := c.Send(fb, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-rs.requested:
		t.Fatalf("identical frame transmitted: %s", r.path)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestReconnectAfterGGRestart(t *testing.T) {
	bad := newRecordingServer(t, 1<<30) // always fails
	good := newRecordingServer(t, 0)

	c := NewClient()
	// Both endpoints answer discovery, but the first one's HTTP fails.
	target := bad.URL
	c.discover = func() (string, error) { return target, nil }
	if _, err := c.EnsureReady(time.Now()); err == nil {
		t.Fatal("expected registration failure")
	}
	// Backoff is active; a Send now must be a no-op error, not a crash.
	if err := c.Send(&render.FB{}, time.Now()); err == nil {
		t.Fatal("send should fail while unregistered")
	}

	// "GG restarts": discovery now returns the good server.
	target = good.URL
	// Backoff still applies for this wall-clock instant; skip it via future.
	if _, err := c.EnsureReady(time.Now().Add(2 * time.Minute)); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	fb := &render.FB{}
	fb.Set(3, 3)
	if err := c.Send(fb, time.Now()); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}
	select {
	case req := <-good.requested:
		if req.path != "/game_metadata" {
			t.Fatalf("first post after reconnect = %s", req.path)
		}
	case <-time.After(time.Second):
		t.Fatal("no registration seen after reconnect")
	}
}

func TestRegistrationRequestsMaxDeinitializeTimer(t *testing.T) {
	rs := newRecordingServer(t, 0)
	c := NewClient()
	c.discover = func() (string, error) { return rs.URL, nil }
	if _, err := c.EnsureReady(time.Now()); err != nil {
		t.Fatal(err)
	}
	req := <-rs.requested // game_metadata arrives first
	if !strings.Contains(req.body, `"deinitialize_timer_length_ms":60000`) {
		t.Fatalf("metadata missing deinitialize timer: %s", req.body)
	}
}

func TestHeartbeatFailureTriggersReregister(t *testing.T) {
	rs := newRecordingServer(t, 0)
	fail := false
	c := NewClient()
	c.discover = func() (string, error) {
		if fail {
			return "http://127.0.0.1:1", nil // unroutable port: posts fail
		}
		return rs.URL, nil
	}
	if _, err := c.EnsureReady(time.Now()); err != nil {
		t.Fatal(err)
	}
	<-rs.requested // metadata
	<-rs.requested // bind

	// A heartbeat failure must de-register so EnsureReady re-registers.
	// Simulate the endpoint dying by pointing the client at a dead port.
	c.mu.Lock()
	c.endpoint = "http://127.0.0.1:1"
	c.mu.Unlock()
	if err := c.Heartbeat(); err == nil {
		t.Fatal("heartbeat should fail while the endpoint is gone")
	}
	if _, err := c.EnsureReady(time.Now()); err != nil {
		t.Fatalf("re-register after heartbeat failure: %v", err)
	}
	select {
	case req := <-rs.requested:
		if req.path != "/game_metadata" {
			t.Fatalf("first post after failure = %s", req.path)
		}
	case <-time.After(time.Second):
		t.Fatal("no re-registration after heartbeat failure")
	}
}

func TestPeriodicReregistration(t *testing.T) {
	rs := newRecordingServer(t, 0)
	c := NewClient()
	c.discover = func() (string, error) { return rs.URL, nil }
	now := time.Now()
	if _, err := c.EnsureReady(now); err != nil {
		t.Fatal(err)
	}
	<-rs.requested // metadata
	<-rs.requested // bind

	// Still inside the re-register window: nothing new.
	if _, err := c.EnsureReady(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-rs.requested:
		t.Fatalf("unexpected re-registration: %s", r.path)
	case <-time.After(150 * time.Millisecond):
	}

	// Past the window: full re-registration happens.
	if _, err := c.EnsureReady(now.Add(reRegisterEvery + time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-rs.requested:
		if req.path != "/game_metadata" {
			t.Fatalf("periodic re-registration = %s", req.path)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic re-registration did not happen")
	}
}
