// Package service installs the iphoneuse daemon as a macOS launchd agent so the
// helper starts at login and re-signs itself without the user doing anything.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/safwanadnan/iphoneuse/internal/config"
)

// Label is the launchd job label.
const Label = "dev.iphoneuse.daemon"

// PlistPath is ~/Library/LaunchAgents/dev.iphoneuse.daemon.plist.
func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Installed reports whether the launch agent file exists.
func Installed() bool {
	_, err := os.Stat(PlistPath())
	return err == nil
}

// Install writes the launch agent and loads it.
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		// Homebrew upgrades replace the versioned Cellar/Caskroom path;
		// point the service at the stable symlink instead.
		if strings.Contains(resolved, "/Cellar/") || strings.Contains(resolved, "/Caskroom/") {
			if _, err := os.Stat("/opt/homebrew/bin/iphoneuse"); err == nil {
				exe = "/opt/homebrew/bin/iphoneuse"
			} else if _, err := os.Stat("/usr/local/bin/iphoneuse"); err == nil {
				exe = "/usr/local/bin/iphoneuse"
			}
		}
	}
	os.MkdirAll(config.LogDir(), 0o755)
	logPath := filepath.Join(config.LogDir(), "daemon.log")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
</dict>
</plist>
`, Label, exe, logPath, logPath)
	os.MkdirAll(filepath.Dir(PlistPath()), 0o755)
	if err := os.WriteFile(PlistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	exec.Command("/bin/launchctl", "bootout", domain()+"/"+Label).Run()
	// bootout returns before the old job is fully gone; loading again too
	// soon fails with "Bootstrap failed: 5: Input/output error".
	for i := 0; i < 50 && Loaded(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		out, err := exec.Command("/bin/launchctl", "bootstrap", domain(), PlistPath()).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		time.Sleep(time.Second)
	}
	return lastErr
}

// Uninstall unloads and removes the launch agent.
func Uninstall() error {
	exec.Command("/bin/launchctl", "bootout", domain()+"/"+Label).Run()
	err := os.Remove(PlistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Loaded reports whether launchd currently has the job.
func Loaded() bool {
	return exec.Command("/bin/launchctl", "print", domain()+"/"+Label).Run() == nil
}
