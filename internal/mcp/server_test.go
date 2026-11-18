package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestInitializeAndList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var out bytes.Buffer
	if err := (&Server{Version: "test"}).Serve(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 responses, got %d: %s", len(lines), out.String())
	}
	var list struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[1]), &list)
	if len(list.Result.Tools) < 10 {
		t.Fatalf("expected tools, got %s", lines[1])
	}
}

func TestDirectionToPoints(t *testing.T) {
	x1, y1, x2, y2, err := DirectionToPoints("up", 400, 800)
	if err != nil || x1 != 200 || x2 != 200 || y1 <= y2 {
		t.Fatalf("up swipe should move the finger upward: %v %v %v %v %v", x1, y1, x2, y2, err)
	}
	if _, _, _, _, err := DirectionToPoints("sideways", 1, 1); err == nil {
		t.Fatal("expected error")
	}
}
