// Package xcode finds Xcode and runs its tools (xcrun, xcodebuild, devicectl, simctl).
package xcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DeveloperDir returns the Xcode developer directory to use. It honours
// DEVELOPER_DIR, then xcode-select, and falls back to /Applications/Xcode*.app
// when xcode-select still points at the Command Line Tools (a common setup
// that would otherwise make every Xcode tool fail).
func DeveloperDir() (string, error) {
	if dir := os.Getenv("DEVELOPER_DIR"); dir != "" {
		return dir, nil
	}
	out, err := exec.Command("/usr/bin/xcode-select", "-p").Output()
	selected := strings.TrimSpace(string(out))
	if err == nil && strings.Contains(selected, ".app/") {
		return selected, nil
	}
	candidates, _ := filepath.Glob("/Applications/Xcode*.app")
	for _, app := range candidates {
		dir := filepath.Join(app, "Contents", "Developer")
		if _, err := os.Stat(filepath.Join(dir, "usr", "bin", "xcodebuild")); err == nil {
			return dir, nil
		}
	}
	return "", errors.New("Xcode is not installed. Install it from the App Store (https://apps.apple.com/app/xcode/id497799835), open it once, then run `iadb setup` again")
}

// Env returns the environment for Xcode subprocesses.
func Env() []string {
	env := os.Environ()
	if dir, err := DeveloperDir(); err == nil {
		env = append(env, "DEVELOPER_DIR="+dir)
	}
	return env
}

// Command builds an exec.Cmd for an Xcode tool via xcrun.
func Command(ctx context.Context, tool string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/xcrun", append([]string{tool}, args...)...)
	cmd.Env = Env()
	return cmd
}

// Run runs an Xcode tool and returns stdout, with stderr folded into the error.
func Run(timeout time.Duration, tool string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := Command(ctx, tool, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if strings.Contains(msg, "license agreements") {
			return out, errors.New(LicenseHint())
		}
		return out, fmt.Errorf("%s %s: %v: %s", tool, strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// RunJSON runs a devicectl-style command that writes JSON to --json-output.
func RunJSON(timeout time.Duration, tool string, args ...string) ([]byte, error) {
	f, err := os.CreateTemp("", "iadb-*.json")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	args = append(args, "--json-output", path, "--quiet")
	if _, err := Run(timeout, tool, args...); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Check reports problems that stop iadb from working: missing Xcode,
// unaccepted license, or missing iOS platform.
func Check() []string {
	var problems []string
	dir, err := DeveloperDir()
	if err != nil {
		return []string{err.Error()}
	}
	cmd := exec.Command(filepath.Join(dir, "usr", "bin", "xcodebuild"), "-license", "check")
	cmd.Env = Env()
	if err := cmd.Run(); err != nil {
		problems = append(problems, LicenseHint())
		return problems
	}
	out, _ := Run(60*time.Second, "xcodebuild", "-showsdks")
	if !bytes.Contains(out, []byte("iphoneos")) {
		problems = append(problems, "The iOS platform is not installed in Xcode. Run: xcodebuild -downloadPlatform iOS")
	}
	return problems
}

// LicenseHint is the exact command to accept the Xcode license. When
// xcode-select points at the Command Line Tools, a plain
// `sudo xcodebuild -license accept` fails, so select Xcode first.
func LicenseHint() string {
	out, _ := exec.Command("/usr/bin/xcode-select", "-p").Output()
	if !strings.Contains(string(out), ".app/") {
		if dir, err := DeveloperDir(); err == nil {
			app := strings.TrimSuffix(dir, "/Contents/Developer")
			return "The Xcode license has not been accepted. Run: sudo xcode-select -s " + app + " && sudo xcodebuild -license accept"
		}
	}
	return "The Xcode license has not been accepted. Run: sudo xcodebuild -license accept"
}
