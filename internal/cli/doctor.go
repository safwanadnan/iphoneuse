package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/safwanadnan/iphoneuse/internal/config"
	"github.com/safwanadnan/iphoneuse/internal/daemon"
	"github.com/safwanadnan/iphoneuse/internal/device"
	"github.com/safwanadnan/iphoneuse/internal/helper"
	"github.com/safwanadnan/iphoneuse/internal/runner"
	"github.com/safwanadnan/iphoneuse/internal/service"
	"github.com/safwanadnan/iphoneuse/internal/signing"
	"github.com/safwanadnan/iphoneuse/internal/xcode"
)

func cmdDoctor(a args) error {
	healthy := true
	bad := func(format string, v ...any) { healthy = false; warn(format, v...) }

	fmt.Printf("iphoneuse %s (helper %s)\n\n", Version, helper.Hash())

	step("Xcode")
	if dir, err := xcode.DeveloperDir(); err != nil {
		bad("%v", err)
	} else {
		ok("%s", dir)
		if problems := xcode.Check(); len(problems) > 0 {
			for _, p := range problems {
				bad("%s", p)
			}
		} else {
			ok("license accepted, iOS platform installed")
		}
	}

	step("Signing teams")
	teams := signing.Teams()
	if len(teams) == 0 {
		warn("no Apple ID in Xcode (needed for real iPhones; simulators work without one)")
	}
	for _, t := range teams {
		kind := "paid, 1-year signatures"
		if t.Free {
			kind = "free, 7-day signatures"
		}
		ok("%s %s (%s)", t.ID, t.Name, kind)
	}

	step("Background service")
	running, pid := daemon.Running()
	switch {
	case service.Installed() && running:
		ok("login service installed, daemon running (pid %d)", pid)
	case running:
		ok("daemon running (pid %d); run `iphoneuse service install` to start it at login and auto re-sign", pid)
	case service.Installed():
		bad("login service installed but the daemon is not running; see `iphoneuse logs`")
	default:
		warn("daemon not running (it starts on demand)")
	}

	step("Devices")
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	devs, err := device.All()
	if err != nil {
		bad("could not list devices: %v", err)
	}
	status := config.LoadStatus()
	if len(devs) == 0 && len(cfg.Devices) == 0 {
		warn("no devices found; plug in your iPhone and unlock it")
	}
	seen := map[string]bool{}
	for _, d := range devs {
		seen[d.UDID] = true
		fmt.Printf("  %s (%s, %s, %s)\n", d.Name, d.OS, d.Kind, d.Connection)
		if d.Kind == "device" {
			if !d.Paired {
				bad("    not paired: unlock it and run `iphoneuse setup`")
			}
			if d.DeveloperMode != "enabled" && d.Online() {
				d.DeveloperMode = device.DeveloperMode(d.UDID)
			}
			if d.DeveloperMode != "enabled" {
				bad("    Developer Mode is %s: Settings → Privacy & Security → Developer Mode", d.DeveloperMode)
			}
		}
		checkConfigured(d.UDID, cfg.Devices[d.UDID], status, d.Online(), bad)
	}
	for udid, dev := range cfg.Devices {
		if !seen[udid] {
			fmt.Printf("  %s (set up, not connected)\n", dev.Name)
			checkConfigured(udid, dev, status, false, bad)
		}
	}

	fmt.Println()
	if healthy {
		fmt.Println("Everything looks good.")
	} else {
		fmt.Println("Some checks need attention (see above). Logs: " + config.LogDir())
	}
	return nil
}

func checkConfigured(udid string, dev *config.Device, status map[string]config.Status, online bool, bad func(string, ...any)) {
	if dev == nil {
		warn("    not set up: run `iphoneuse setup -s %s`", udid)
		return
	}
	if dev.HelperHash != helper.Hash() {
		warn("    helper is from an older iphoneuse; it will be rebuilt automatically")
	}
	if !dev.ProfileExpires.IsZero() {
		left := time.Until(dev.ProfileExpires)
		if left < 0 {
			bad("    signature expired %s ago; reconnect the phone or run `iphoneuse setup`", (-left).Round(time.Hour))
		} else {
			ok("    signature valid for %s (until %s)", left.Round(time.Hour), dev.ProfileExpires.Local().Format("Mon Jan 2 15:04"))
		}
	}
	if st, has := status[dev.UDID]; has {
		msg := st.State
		if st.Message != "" {
			msg += ": " + strings.TrimSpace(st.Message)
		}
		if st.State == "error" || st.State == "needs-trust" || st.State == "needs-unlock" {
			bad("    helper %s", msg)
		} else {
			ok("    helper %s", msg)
		}
	}
	if online {
		if s, err := runner.New(dev).Status(3 * time.Second); err == nil {
			ok("    helper %s answering (%s, %.0fx%.0f points)", s.Version, s.OS, s.Screen.Width, s.Screen.Height)
		} else {
			warn("    helper not answering yet (any command starts it)")
		}
	}
}
