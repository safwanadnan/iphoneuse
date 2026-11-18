// Package mcp serves iadb's commands as Model Context Protocol tools over
// stdio, so AI agents (Claude, Cursor, Codex, …) can drive an iPhone.
package mcp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/iadb-dev/iadb/internal/device"
	"github.com/iadb-dev/iadb/internal/runner"
	"github.com/iadb-dev/iadb/internal/session"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type content map[string]any

func text(s string) content { return content{"type": "text", "text": s} }

const instructions = `iadb controls a real iPhone (or iOS Simulator) connected to this Mac.
Loop: call "screen" to see the screenshot and the numbered element list, act with "tap" (prefer ref numbers from the latest screen/tree, or visible text), "type_text", "swipe", "press_key", then call "screen" again to check the result.
Coordinates are in points; the screenshot from "screen" is rendered at point resolution, so its pixel coordinates are tap coordinates.
Refs are only valid until the screen changes; refresh with "screen" or "tree" after every action.`

var deviceProp = map[string]any{"type": "string", "description": "Device UDID or name. Optional when one device is connected."}

func schema(props map[string]any, required ...string) map[string]any {
	props["device"] = deviceProp
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func tools() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	return []map[string]any{
		{"name": "list_devices", "description": "List connected iPhones and booted simulators.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "screen", "description": "Take a screenshot of the device (at point resolution) and list the on-screen elements with ref numbers and tap coordinates. Call this before and after acting.",
			"inputSchema": schema(map[string]any{"include_tree": boolean("Include the element list (default true)."), "quality": num("JPEG quality 0.1-1 (default 0.6).")})},
		{"name": "tree", "description": "List on-screen elements of the foreground app as text: [ref] Type \"label\" @(x,y) size. Cheaper than screen when you don't need pixels.",
			"inputSchema": schema(map[string]any{"bundle_id": str("Read a specific app instead of the foreground one.")})},
		{"name": "tap", "description": "Tap an element by ref (from the latest screen/tree), by visible text, or at x/y points. Set duration for a long press or double for a double tap.",
			"inputSchema": schema(map[string]any{"ref": num("Element ref number."), "text": str("Visible text or accessibility id to tap."), "x": num("X in points."), "y": num("Y in points."), "duration": num("Seconds to hold (long press)."), "double": boolean("Double tap.")})},
		{"name": "swipe", "description": "Swipe with one finger. Either give a direction (finger movement: up scrolls content down) or exact x1,y1,x2,y2 points.",
			"inputSchema": schema(map[string]any{"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}}, "x1": num(""), "y1": num(""), "x2": num(""), "y2": num(""), "duration": num("Seconds (default 0.3).")})},
		{"name": "type_text", "description": "Type text into the focused field (tap the field first). Use \\n to press return.",
			"inputSchema": schema(map[string]any{"text": str("Text to type.")}, "text")},
		{"name": "press_key", "description": "Press a key: home, lock, volume-up, volume-down, return, delete, tab, escape.",
			"inputSchema": schema(map[string]any{"key": str("Key name.")}, "key")},
		{"name": "launch_app", "description": "Open an app by bundle id (e.g. com.apple.Preferences, net.whatsapp.WhatsApp). Use list_apps to find ids.",
			"inputSchema": schema(map[string]any{"bundle_id": str("Bundle identifier."), "restart": boolean("Kill it first for a fresh start.")}, "bundle_id")},
		{"name": "terminate_app", "description": "Force-quit an app by bundle id.",
			"inputSchema": schema(map[string]any{"bundle_id": str("Bundle identifier.")}, "bundle_id")},
		{"name": "list_apps", "description": "List apps installed by the user (name and bundle id).",
			"inputSchema": schema(map[string]any{})},
		{"name": "foreground_app", "description": "Return the bundle id of the app in the foreground.",
			"inputSchema": schema(map[string]any{})},
		{"name": "open_url", "description": "Open a URL or deep link (https://…, tel:, maps:, app schemes). Needs iOS 16.4+.",
			"inputSchema": schema(map[string]any{"url": str("URL to open.")}, "url")},
	}
}

// Server is a stdio MCP server.
type Server struct {
	Version string
	mu      sync.Mutex
	out     *json.Encoder
	clients map[string]*runner.Client
}

// Serve reads JSON-RPC messages (one per line) until in closes.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	s.out = json.NewEncoder(out)
	s.clients = map[string]*runner.Client{}
	reader := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var req request
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			} else {
				s.handle(req)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Server) send(r response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Encode(r)
}

func (s *Server) handle(req request) {
	if len(req.ID) == 0 {
		return // notification
	}
	reply := func(result any) { s.send(response{JSONRPC: "2.0", ID: req.ID, Result: result}) }
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		reply(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "iadb", "version": s.Version},
			"instructions":    instructions,
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": tools()})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.send(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "invalid params"}})
			return
		}
		items, err := s.call(p.Name, p.Arguments)
		if err != nil {
			reply(map[string]any{"content": []content{text("Error: " + err.Error())}, "isError": true})
			return
		}
		reply(map[string]any{"content": items})
	default:
		s.send(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32601, "method not found: " + req.Method}})
	}
}

func (s *Server) client(args map[string]any) (*runner.Client, error) {
	sel, _ := args["device"].(string)
	if c, ok := s.clients[sel]; ok {
		if _, err := c.Status(3 * time.Second); err == nil {
			return c, nil
		}
	}
	c, err := session.Connect(sel, 3*time.Minute, nil)
	if err != nil {
		return nil, err
	}
	s.clients[sel] = c
	return c, nil
}

func num(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case string:
		var f float64
		_, err := fmt.Sscan(v, &f)
		return f, err == nil
	}
	return 0, false
}

func (s *Server) call(name string, args map[string]any) ([]content, error) {
	if args == nil {
		args = map[string]any{}
	}
	if name == "list_devices" {
		devs, err := device.All()
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(devs, "", "  ")
		return []content{text(string(data))}, nil
	}
	c, err := s.client(args)
	if err != nil {
		return nil, err
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch name {
	case "screen":
		quality, ok := num(args, "quality")
		if !ok {
			quality = 0.6
		}
		img, mime, err := c.Screenshot(runner.ScreenshotOptions{JPEG: true, Quality: quality, Scale: "points"})
		if err != nil {
			return nil, err
		}
		items := []content{{"type": "image", "data": base64.StdEncoding.EncodeToString(img), "mimeType": mime}}
		if v, ok := args["include_tree"].(bool); !ok || v {
			tree, err := c.Tree("", 0)
			if err != nil {
				items = append(items, text("(element list unavailable: "+err.Error()+")"))
			} else {
				items = append(items, text(tree.Text))
			}
		}
		return items, nil
	case "tree":
		tree, err := c.Tree(str("bundle_id"), 0)
		if err != nil {
			return nil, err
		}
		return []content{text(tree.Text)}, nil
	case "tap":
		target := map[string]any{}
		if ref, ok := num(args, "ref"); ok {
			target["ref"] = int(ref)
		} else if t := str("text"); t != "" {
			target["text"] = t
		} else {
			x, okx := num(args, "x")
			y, oky := num(args, "y")
			if !okx || !oky {
				return nil, fmt.Errorf("tap needs ref, text, or x and y")
			}
			target["x"], target["y"] = x, y
		}
		if d, ok := num(args, "duration"); ok {
			target["duration"] = d
		}
		if b, _ := args["double"].(bool); b {
			target["count"] = 2
		}
		r, err := c.Tap(target)
		if err != nil {
			return nil, err
		}
		return []content{text(fmt.Sprintf("Tapped %s at (%.0f,%.0f). Call screen to see the result.", r.Tapped, r.X, r.Y))}, nil
	case "swipe":
		duration, ok := num(args, "duration")
		if !ok {
			duration = 0.3
		}
		if dir := str("direction"); dir != "" {
			st, err := c.Status(5 * time.Second)
			if err != nil {
				return nil, err
			}
			x1, y1, x2, y2, err := DirectionToPoints(dir, st.Screen.Width, st.Screen.Height)
			if err != nil {
				return nil, err
			}
			return []content{text("Swiped " + dir)}, c.Swipe(x1, y1, x2, y2, duration)
		}
		x1, a := num(args, "x1")
		y1, b := num(args, "y1")
		x2, cc := num(args, "x2")
		y2, d := num(args, "y2")
		if !a || !b || !cc || !d {
			return nil, fmt.Errorf("swipe needs a direction or x1, y1, x2, y2")
		}
		return []content{text("Swiped")}, c.Swipe(x1, y1, x2, y2, duration)
	case "type_text":
		return []content{text("Typed")}, c.Type(str("text"))
	case "press_key":
		return []content{text("Pressed " + str("key"))}, c.Key(str("key"))
	case "launch_app":
		restart, _ := args["restart"].(bool)
		return []content{text("Launched " + str("bundle_id"))}, c.Launch(str("bundle_id"), restart)
	case "terminate_app":
		return []content{text("Terminated " + str("bundle_id"))}, c.Terminate(str("bundle_id"))
	case "open_url":
		return []content{text("Opened " + str("url"))}, c.Open(str("url"))
	case "foreground_app":
		id, label, err := c.ForegroundApp()
		if err != nil {
			return nil, err
		}
		return []content{text(fmt.Sprintf("%s (%s)", id, label))}, nil
	case "list_apps":
		apps, err := device.Apps(c.Device.UDID, c.Device.IsSimulator())
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, a := range apps {
			fmt.Fprintf(&b, "%s\t%s\n", a.BundleID, a.Name)
		}
		return []content{text(b.String())}, nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// DirectionToPoints converts a finger direction into swipe endpoints.
func DirectionToPoints(dir string, w, h float64) (x1, y1, x2, y2 float64, err error) {
	cx, cy := w/2, h/2
	switch strings.ToLower(dir) {
	case "up":
		return cx, h * 0.7, cx, h * 0.3, nil
	case "down":
		return cx, h * 0.3, cx, h * 0.7, nil
	case "left":
		return w * 0.8, cy, w * 0.2, cy, nil
	case "right":
		return w * 0.2, cy, w * 0.8, cy, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("direction must be up, down, left or right")
}
