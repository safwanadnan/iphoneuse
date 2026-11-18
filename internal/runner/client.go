// Package runner is the HTTP client for the on-device helper.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/iadb-dev/iadb/internal/config"
	"github.com/iadb-dev/iadb/internal/usbmux"
)

// Client talks to one device's helper.
type Client struct {
	Device *config.Device
	http   *http.Client
}

// New creates a client. Phones are reached through usbmuxd (USB or Wi-Fi
// sync); simulators share the Mac's network, so they use localhost.
func New(dev *config.Device) *Client {
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dev.IsSimulator() {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", dev.Port))
			}
			return usbmux.Dial(dev.UDID, dev.Port)
		},
	}
	return &Client{Device: dev, http: &http.Client{Transport: transport, Timeout: 90 * time.Second}}
}

// Error is an error returned by the helper.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func (c *Client) do(method, path string, query url.Values, body any, timeout time.Duration) ([]byte, string, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		reader = bytes.NewReader(data)
	}
	u := "http://iadb" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, "", &Error{e.Error}
		}
		return nil, "", &Error{fmt.Sprintf("helper returned HTTP %d", resp.StatusCode)}
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (c *Client) call(method, path string, query url.Values, body any, out any) error {
	data, _, err := c.do(method, path, query, body, 90*time.Second)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Status is the helper's /status response.
type Status struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Model   string `json:"model"`
	OS      string `json:"os"`
	Screen  struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Scale  float64 `json:"scale"`
	} `json:"screen"`
}

// Status pings the helper with a short timeout.
func (c *Client) Status(timeout time.Duration) (*Status, error) {
	data, _, err := c.do("GET", "/status", nil, nil, timeout)
	if err != nil {
		return nil, err
	}
	var s Status
	return &s, json.Unmarshal(data, &s)
}

// ScreenshotOptions controls the image the helper returns.
type ScreenshotOptions struct {
	JPEG    bool
	Quality float64
	Scale   string // "" (full resolution), "points", or a factor like "0.5"
}

// Screenshot returns image bytes and their MIME type.
func (c *Client) Screenshot(opts ScreenshotOptions) ([]byte, string, error) {
	q := url.Values{}
	if opts.JPEG {
		q.Set("format", "jpeg")
		if opts.Quality > 0 {
			q.Set("quality", fmt.Sprint(opts.Quality))
		}
	}
	if opts.Scale != "" {
		q.Set("scale", opts.Scale)
	}
	return c.do("GET", "/screenshot", q, nil, 60*time.Second)
}

// Element is one entry in the accessibility tree.
type Element struct {
	Ref         int    `json:"ref"`
	Type        string `json:"type"`
	Label       string `json:"label"`
	Value       string `json:"value"`
	Identifier  string `json:"identifier"`
	Placeholder string `json:"placeholder"`
	Enabled     bool   `json:"enabled"`
	Selected    bool   `json:"selected"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
}

// Tree is the compact accessibility tree of the foreground app.
type Tree struct {
	BundleID  string    `json:"bundleId"`
	Text      string    `json:"text"`
	Elements  []Element `json:"elements"`
	Truncated bool      `json:"truncated"`
}

// Tree fetches the accessibility tree of the foreground app (or bundleID).
func (c *Client) Tree(bundleID string, limit int) (*Tree, error) {
	q := url.Values{}
	if bundleID != "" {
		q.Set("app", bundleID)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var t Tree
	return &t, c.call("GET", "/tree", q, nil, &t)
}

// TapResult describes what was tapped.
type TapResult struct {
	Tapped string  `json:"tapped"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// Tap taps a target: map with x/y, ref, or text, plus duration/count.
func (c *Client) Tap(target map[string]any) (*TapResult, error) {
	var r TapResult
	return &r, c.call("POST", "/tap", nil, target, &r)
}

// Swipe drags from one point to another.
func (c *Client) Swipe(x1, y1, x2, y2, duration float64) error {
	return c.call("POST", "/swipe", nil, map[string]any{"x1": x1, "y1": y1, "x2": x2, "y2": y2, "duration": duration}, nil)
}

// Type types text into the focused field.
func (c *Client) Type(text string) error {
	return c.call("POST", "/type", nil, map[string]any{"text": text}, nil)
}

// Key presses a hardware or keyboard key.
func (c *Client) Key(name string) error {
	return c.call("POST", "/key", nil, map[string]any{"key": name}, nil)
}

// Launch brings an app to the foreground, starting it if needed.
func (c *Client) Launch(bundleID string, restart bool) error {
	return c.call("POST", "/launch", nil, map[string]any{"bundleId": bundleID, "restart": restart}, nil)
}

// Terminate kills an app.
func (c *Client) Terminate(bundleID string) error {
	return c.call("POST", "/terminate", nil, map[string]any{"bundleId": bundleID}, nil)
}

// Open opens a URL or deep link.
func (c *Client) Open(rawURL string) error {
	return c.call("POST", "/open", nil, map[string]any{"url": rawURL}, nil)
}

// ForegroundApp returns the bundle id and name of the foreground app.
func (c *Client) ForegroundApp() (string, string, error) {
	var r struct {
		BundleID string `json:"bundleId"`
		Name     string `json:"name"`
	}
	err := c.call("GET", "/app", nil, nil, &r)
	return r.BundleID, r.Name, err
}

// Shutdown asks the helper to stop.
func (c *Client) Shutdown() error {
	_, _, err := c.do("POST", "/shutdown", nil, map[string]any{}, 5*time.Second)
	return err
}
