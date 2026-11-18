// Package helper extracts, builds, signs and launches the on-device helper
// (an XCUITest runner) that iadb talks to.
package helper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iadb-dev/iadb"
	"github.com/iadb-dev/iadb/internal/config"
	"github.com/iadb-dev/iadb/internal/signing"
	"github.com/iadb-dev/iadb/internal/xcode"
	"howett.net/plist"
)

const (
	project = "IADBHelper.xcodeproj"
	scheme  = "IADBRunner"
)

var (
	hashOnce sync.Once
	hash     string
)

// Hash identifies the embedded helper source; a new iadb version with a
// changed helper triggers a rebuild.
func Hash() string {
	hashOnce.Do(func() {
		h := sha256.New()
		var files []string
		fs.WalkDir(iadb.HelperFS, "helper", func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !strings.Contains(path, "xcuserdata") {
				files = append(files, path)
			}
			return nil
		})
		sort.Strings(files)
		for _, f := range files {
			data, _ := iadb.HelperFS.ReadFile(f)
			h.Write([]byte(f))
			h.Write(data)
		}
		hash = hex.EncodeToString(h.Sum(nil))[:12]
	})
	return hash
}

// Extract writes the helper source to the iadb support directory.
func Extract() (string, error) {
	dir := filepath.Join(config.Dir(), "helper", Hash())
	if _, err := os.Stat(filepath.Join(dir, project)); err == nil {
		return dir, nil
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	err := fs.WalkDir(iadb.HelperFS, "helper", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, strings.TrimPrefix(path, "helper"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := iadb.HelperFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return "", err
	}
	os.RemoveAll(dir)
	return dir, os.Rename(tmp, dir)
}

// BuildError carries a friendly explanation for common xcodebuild failures.
type BuildError struct {
	Hint string
	Log  string
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("%s\n(full log: %s)", e.Hint, e.Log)
}

// Diagnose turns xcodebuild output into an actionable hint, or "".
func Diagnose(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "has not been explicitly trusted") || strings.Contains(lower, "untrusted developer") ||
		strings.Contains(lower, "invalid code signature, inadequate entitlements"):
		return "NEEDS_TRUST: On your iPhone open Settings → General → VPN & Device Management, tap your Apple ID under \"Developer App\", and tap Trust."
	case strings.Contains(lower, "no accounts") || strings.Contains(lower, "no account for team") ||
		strings.Contains(lower, "no signing certificate") && strings.Contains(lower, "found"):
		return "Xcode has no Apple ID to sign with. Open Xcode → Settings → Accounts, add your Apple ID, then run `iadb setup` again."
	case strings.Contains(lower, "maximum number of apps for free development profiles"):
		return "Free Apple IDs can only have 3 sideloaded apps on a phone. Delete one of your other sideloaded apps and run `iadb setup` again."
	case strings.Contains(lower, "developer mode disabled") || strings.Contains(lower, "developer mode is disabled"):
		return "Developer Mode is off. On your iPhone open Settings → Privacy & Security → Developer Mode, turn it on, and restart the phone."
	case strings.Contains(lower, "is locked") || strings.Contains(lower, "passcode protected"):
		return "Your iPhone is locked. Unlock it and keep it unlocked while iadb starts."
	case strings.Contains(lower, "is not available because it is unpaired") || strings.Contains(lower, "not paired"):
		return "This Mac is not trusted by the phone. Unlock it and tap Trust on the \"Trust This Computer?\" prompt."
	case strings.Contains(lower, "ios") && strings.Contains(lower, "is not installed") ||
		strings.Contains(lower, "platform is not installed"):
		return "The iOS platform is missing from Xcode. Run: xcodebuild -downloadPlatform iOS"
	case strings.Contains(lower, "license"):
		return xcode.LicenseHint()
	}
	return ""
}

// Build compiles and signs the helper for a device and returns the
// xctestrun path and profile expiry (zero for simulators).
func Build(dev *config.Device, logw io.Writer) error {
	unlock, err := lockBuild(dev.UDID, true)
	if err != nil {
		return err
	}
	defer unlock()
	src, err := Extract()
	if err != nil {
		return err
	}
	derived := filepath.Join(config.Dir(), "build", dev.UDID)
	args := []string{
		"build-for-testing",
		"-project", filepath.Join(src, project),
		"-scheme", scheme,
		"-destination", "id=" + dev.UDID,
		"-derivedDataPath", derived,
	}
	if dev.IsSimulator() {
		args = append(args, "CODE_SIGN_IDENTITY=-", "CODE_SIGNING_REQUIRED=NO")
	} else {
		if dev.Team == "" {
			return fmt.Errorf("no signing team set for %s", dev.Name)
		}
		args = append(args,
			"-allowProvisioningUpdates",
			"-allowProvisioningDeviceRegistration",
			"DEVELOPMENT_TEAM="+dev.Team,
			"CODE_SIGN_STYLE=Automatic",
			"IADB_BUNDLE_SUFFIX=."+strings.ToLower(dev.Team),
		)
	}

	logPath := filepath.Join(config.LogDir(), "build-"+dev.UDID+".log")
	os.MkdirAll(config.LogDir(), 0o755)
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	var tail tailBuffer
	writers := []io.Writer{logFile, &tail}
	if logw != nil {
		writers = append(writers, logw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := xcode.Command(ctx, "xcodebuild", args...)
	cmd.Stdout = io.MultiWriter(writers...)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		hint := Diagnose(tail.String())
		if hint == "" {
			hint = "Building the helper failed:\n" + lastLines(tail.String(), 15)
		}
		return &BuildError{Hint: hint, Log: logPath}
	}

	platform := "iphoneos"
	if dev.IsSimulator() {
		platform = "iphonesimulator"
	}
	matches, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "*"+platform+"*.xctestrun"))
	var xctestrun string
	for _, m := range matches {
		if !strings.Contains(filepath.Base(m), "iadb-") {
			xctestrun = m
		}
	}
	if xctestrun == "" {
		return fmt.Errorf("build succeeded but no .xctestrun file was produced (see %s)", logPath)
	}
	dev.XCTestRun = xctestrun
	dev.HelperHash = Hash()
	dev.BuiltAt = time.Now()
	dev.ProfileExpires = time.Time{}
	if !dev.IsSimulator() {
		profiles, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "*-iphoneos", "*.app", "embedded.mobileprovision"))
		for _, p := range profiles {
			if exp, err := signing.ProfileExpiry(p); err == nil && (dev.ProfileExpires.IsZero() || exp.Before(dev.ProfileExpires)) {
				dev.ProfileExpires = exp
			}
		}
	}
	return nil
}

// RunnerCommand returns the xcodebuild process that keeps the helper alive
// on the device. It writes a per-device copy of the .xctestrun with the
// helper's port injected (the copy must sit next to the original because
// paths inside it are relative to __TESTROOT__).
func RunnerCommand(ctx context.Context, dev *config.Device) (*exec.Cmd, error) {
	data, err := os.ReadFile(dev.XCTestRun)
	if err != nil {
		return nil, fmt.Errorf("helper is not built for %s; run `iadb setup`: %w", dev.Name, err)
	}
	var run map[string]any
	format, err := plist.Unmarshal(data, &run)
	if err != nil {
		return nil, err
	}
	env := map[string]any{"IADB_PORT": fmt.Sprint(dev.Port)}
	injectEnv(run, env)
	patched, err := plist.Marshal(run, format)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(dev.XCTestRun), "iadb-"+dev.UDID+".xctestrun")
	if err := os.WriteFile(path, patched, 0o644); err != nil {
		return nil, err
	}
	cmd := xcode.Command(ctx, "xcodebuild",
		"test-without-building",
		"-xctestrun", path,
		"-destination", "id="+dev.UDID,
		"-only-testing:IADBRunner/IADBRunner/testRun",
	)
	return cmd, nil
}

// injectEnv adds environment variables to every test target in an
// .xctestrun (works for both format v1 and v2).
func injectEnv(node any, env map[string]any) {
	switch t := node.(type) {
	case map[string]any:
		if _, ok := t["TestBundlePath"]; ok {
			for _, key := range []string{"EnvironmentVariables", "TestingEnvironmentVariables"} {
				vars, _ := t[key].(map[string]any)
				if vars == nil {
					vars = map[string]any{}
				}
				for k, v := range env {
					vars[k] = v
				}
				t[key] = vars
			}
		}
		for _, child := range t {
			injectEnv(child, env)
		}
	case []any:
		for _, child := range t {
			injectEnv(child, env)
		}
	}
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 64<<10 {
		t.buf = t.buf[len(t.buf)-64<<10:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var keep []string
	for _, l := range lines {
		if strings.Contains(l, "error") || strings.Contains(l, "Error") || strings.Contains(l, "failed") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		keep = lines
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

func lockBuild(udid string, wait bool) (func(), error) {
	os.MkdirAll(filepath.Join(config.Dir(), "build"), 0o755)
	f, err := os.OpenFile(filepath.Join(config.Dir(), "build", udid+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Building reports whether a build for this device is in progress.
func Building(udid string) bool {
	unlock, err := lockBuild(udid, false)
	if err != nil {
		return true
	}
	unlock()
	return false
}
