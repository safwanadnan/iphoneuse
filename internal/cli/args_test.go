package cli

import "testing"

func TestParseArgsFlagsAnywhere(t *testing.T) {
	a, err := parseArgs([]string{"-s", "phone", "tap", "10", "-20.5", "--long", "--duration=2"})
	if err != nil {
		t.Fatal(err)
	}
	if a.str("s") != "phone" || !a.bool("long") || a.str("duration") != "2" {
		t.Fatalf("flags: %+v", a.flags)
	}
	if len(a.pos) != 3 || a.pos[0] != "tap" || a.pos[2] != "-20.5" {
		t.Fatalf("positionals: %v", a.pos)
	}
}

func TestParseArgsAliasesAndDoubleDash(t *testing.T) {
	a, err := parseArgs([]string{"type", "--device", "x", "--", "--not-a-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if a.str("s") != "x" || a.pos[1] != "--not-a-flag" {
		t.Fatalf("got %+v", a)
	}
	if _, err := parseArgs([]string{"-s"}); err == nil {
		t.Fatal("expected missing value error")
	}
}
