package helper

import (
	"strings"
	"testing"
)

func TestInjectEnvBothFormats(t *testing.T) {
	v1 := map[string]any{
		"IADBRunner":             map[string]any{"TestBundlePath": "x", "EnvironmentVariables": map[string]any{"A": "1"}},
		"__xctestrun_metadata__": map[string]any{"FormatVersion": 1},
	}
	v2 := map[string]any{"TestConfigurations": []any{map[string]any{"TestTargets": []any{map[string]any{"TestBundlePath": "y"}}}}}
	env := map[string]any{"IADB_PORT": "47100"}
	injectEnv(v1, env)
	injectEnv(v2, env)
	t1 := v1["IADBRunner"].(map[string]any)
	if t1["EnvironmentVariables"].(map[string]any)["IADB_PORT"] != "47100" || t1["EnvironmentVariables"].(map[string]any)["A"] != "1" {
		t.Fatalf("v1 not patched: %v", t1)
	}
	t2 := v2["TestConfigurations"].([]any)[0].(map[string]any)["TestTargets"].([]any)[0].(map[string]any)
	if t2["TestingEnvironmentVariables"].(map[string]any)["IADB_PORT"] != "47100" {
		t.Fatalf("v2 not patched: %v", t2)
	}
}

func TestDiagnose(t *testing.T) {
	cases := map[string]string{
		"its profile has not been explicitly trusted by the user": "NEEDS_TRUST",
		"Developer Mode disabled":                                 "Developer Mode",
		"No Accounts: Add a new account in Accounts settings.":    "Apple ID",
		"maximum number of apps for free development profiles":    "3 sideloaded",
	}
	for in, want := range cases {
		if got := Diagnose(in); !strings.Contains(got, want) {
			t.Errorf("Diagnose(%q) = %q, want it to mention %q", in, got, want)
		}
	}
	if Diagnose("** TEST BUILD SUCCEEDED **") != "" {
		t.Error("success output should have no hint")
	}
}

func TestHashStable(t *testing.T) {
	if Hash() == "" || Hash() != Hash() {
		t.Fatal("hash must be stable and non-empty")
	}
}
