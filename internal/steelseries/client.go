// Package steelseries talks to the local SteelSeries GG GameSense server:
// endpoint discovery from coreProps.json, game/event registration, and
// dynamic 128x40 OLED frames. All traffic stays on loopback.
package steelseries

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"codexconnector/internal/render"
)

// Default identity for this app's registration. The game id is deliberately
// namespaced so it cannot collide with other GameSense applications.
const (
	Game        = "CODEXCONNECTOR"
	Event       = "CODEX_STATE"
	DisplayName = "ApexCodexStatus (ACS)"
	Developer   = "local"
)

// GG deactivates a game when no event arrives within its timeout (default
// 15 s) and the OLED falls back to whatever GG has configured instead. This
// app must always own the OLED, so the registration requests the maximum
// grace period and the app heartbeats well inside it.
const DeinitializeTimerMS = 60000

// reRegisterEvery forces a full re-registration periodically so silent GG
// state loss (a restart that keeps the same port, internal reset) recovers.
const reRegisterEvery = 5 * time.Minute

// coreProps locations in priority order (GG first, legacy Engine second).
func corePropsPaths() []string {
	pd := os.Getenv("PROGRAMDATA")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return []string{
		filepath.Join(pd, "SteelSeries", "GG", "coreProps.json"),
		filepath.Join(pd, "SteelSeries", "SteelSeries Engine 3", "coreProps.json"),
	}
}

// DiscoverEndpoint reads the GameSense base URL from the local GG
// configuration. It is called on every (re)connection because GG changes its
// port between restarts.
func DiscoverEndpoint() (string, error) {
	for _, p := range corePropsPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var props struct {
			Address string `json:"address"`
		}
		if json.Unmarshal(data, &props) != nil || props.Address == "" {
			continue
		}
		return "http://" + props.Address, nil
	}
	return "", errors.New("gamesense endpoint not found (SteelSeries GG not installed or not running?)")
}

// Client posts frames to GameSense and re-registers itself after GG restarts.
// It is safe for concurrent use; Send and Heartbeat serialize internally.
type Client struct {
	http *http.Client

	// discover is overridable for tests.
	discover func() (string, error)

	mu           sync.Mutex
	endpoint     string
	registered   bool
	lastRegister time.Time
	lastHash     uint64
	backoff      time.Duration
	nextTry      time.Time
}

// NewClient returns a client; connection is lazy.
func NewClient() *Client {
	return &Client{
		http:     &http.Client{Timeout: 2 * time.Second},
		discover: DiscoverEndpoint,
	}
}

// Endpoint returns the current base URL (diagnostics), or "" before first
// successful discovery.
func (c *Client) Endpoint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endpoint
}

// Reset forgets the registration; used after connection failures.
func (c *Client) Reset() {
	c.mu.Lock()
	c.registered = false
	c.mu.Unlock()
}

// ClearFrame is an all-black frame.
func ClearFrame() *render.FB {
	return &render.FB{}
}

// EnsureReady discovers the endpoint and (re)registers the game and the OLED
// handler, respecting bounded exponential backoff while GG is unavailable.
// A registration older than reRegisterEvery is refreshed proactively.
// The returned flag reports whether a (re)registration just happened, so the
// caller can force-resend the current frame (a fresh bind shows a blank).
func (c *Client) EnsureReady(now time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.registered {
		if now.Sub(c.lastRegister) >= reRegisterEvery {
			c.registered = false // silent state loss: re-register, resend frame
		} else {
			return false, nil
		}
	}
	if now.Before(c.nextTry) {
		return false, fmt.Errorf("gamesense unavailable, retrying (backoff %s)", c.backoff)
	}
	if err := c.discoverAndRegisterLocked(); err != nil {
		if c.backoff == 0 {
			c.backoff = time.Second
		} else {
			c.backoff *= 2
			if c.backoff > time.Minute {
				c.backoff = time.Minute
			}
		}
		c.nextTry = now.Add(c.backoff)
		return false, err
	}
	c.backoff = 0
	c.registered = true
	c.lastRegister = now
	return true, nil
}

// Invalidate drops the client-side frame dedupe so the next Send transmits
// even if the framebuffer is unchanged (used for periodic force-refreshes).
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.lastHash = 0
	c.mu.Unlock()
}

func (c *Client) discoverAndRegisterLocked() error {
	ep, err := c.discover()
	if err != nil {
		return err
	}
	meta := map[string]any{
		"game":                         Game,
		"game_display_name":            DisplayName,
		"developer":                    Developer,
		"deinitialize_timer_length_ms": DeinitializeTimerMS,
	}
	if err := postJSON(c.http, ep+"/game_metadata", meta); err != nil {
		return err
	}
	blank := (&render.FB{}).Pack640()
	if err := c.bindLocked(ep, blank); err != nil {
		return err
	}
	c.endpoint = ep
	c.lastHash = 0 // force the next frame through
	return nil
}

func (c *Client) bindLocked(ep string, blank [render.FrameBytes]byte) error {
	payload := []byte(`{"game":"` + Game + `","event":"` + Event + `","value_optional":true,"handlers":[{"device-type":"screened-128x40","zone":"one","mode":"screen","datas":[{"has-text":false,"image-data":`)
	payload = render.AppendJSONUints(payload, blank[:])
	payload = append(payload, "}]}]}"...)
	return postRaw(c.http, ep+"/bind_game_event", payload)
}

// Send pushes a frame if it differs from the last one transmitted.
func (c *Client) Send(fb *render.FB, now time.Time) error {
	packed := fb.Pack640()
	hash := fb.Hash()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.registered {
		return errors.New("gamesense not registered")
	}
	if hash == c.lastHash {
		return nil
	}
	payload := []byte(`{"game":"` + Game + `","event":"` + Event + `","data":{"frame":{"image-data-128x40":`)
	payload = render.AppendJSONUints(payload, packed[:])
	payload = append(payload, "}}}"...)
	if err := postRaw(c.http, c.endpoint+"/game_event", payload); err != nil {
		c.registered = false // GG may have restarted; next EnsureReady re-registers
		return err
	}
	c.lastHash = hash
	return nil
}

// Disengage releases the OLED immediately: the game is removed from GG so
// the device returns to its own configured content without waiting for the
// deinitialize window. Idempotent; safe to call repeatedly.
func (c *Client) Disengage() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoint != "" {
		_ = postJSON(c.http, c.endpoint+"/remove_game", map[string]string{"game": Game})
	}
	c.registered = false
	c.lastHash = 0
	c.lastRegister = time.Time{}
}

// Heartbeat resets GG's game-deactivation timer. A failing heartbeat is
// treated as connection loss: the client de-registers itself so the next
// EnsureReady re-registers and re-sends the current frame.
func (c *Client) Heartbeat() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.registered {
		return nil
	}
	if err := postJSON(c.http, c.endpoint+"/game_heartbeat", map[string]string{"game": Game}); err != nil {
		c.registered = false
		return err
	}
	return nil
}

// Connected reports whether the endpoint currently answers (diagnostics).
func (c *Client) Connected() bool {
	c.mu.Lock()
	ep := c.endpoint
	c.mu.Unlock()
	if ep == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", hostOf(ep), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func hostOf(endpoint string) string {
	if len(endpoint) > 7 && endpoint[:7] == "http://" {
		endpoint = endpoint[7:]
	}
	return endpoint
}

func postJSON(h *http.Client, url string, body any) error {
	data, _ := json.Marshal(body)
	return postRaw(h, url, data)
}

func postRaw(h *http.Client, url string, body []byte) error {
	resp, err := h.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gamesense %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}
