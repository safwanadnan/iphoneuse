package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/iadb-dev/iadb/internal/config"
	"github.com/iadb-dev/iadb/internal/daemon"
	"github.com/iadb-dev/iadb/internal/device"
	"github.com/iadb-dev/iadb/internal/helper"
	"github.com/iadb-dev/iadb/internal/runner"
	"github.com/iadb-dev/iadb/internal/service"
	"github.com/iadb-dev/iadb/internal/session"
	"github.com/iadb-dev/iadb/internal/signing"
	"github.com/iadb-dev/iadb/internal/usbmux"
	"github.com/iadb-dev/iadb/internal/xcode"
)

func step(format string, v ...any) { fmt.Printf("• "+format+"\n", v...) }
func ok(format string, v ...any)   { fmt.Printf("  ✓ "+format+"\n", v...) }
func warn(format string, v ...any) { fmt.Printf("  ! "+format+"\n", v...) }

func cmdSetup(a args) error {
	step("Checking Xcode")
	if problems := xcode.Check(); len(problems) > 0 {
		for _, p := range problems {
			warn(p)
		}
		return exitError{1, "\nFix the above, then run `iadb setup` again."}
	}
	dir, _ := xcode.DeveloperDir()
	ok("Xcode at %s", strings.TrimSuffix(dir, "/Contents/Developer"))

	step("Finding your device")
	info, err := device.Resolve(session.Selector(a.str("s")))
	if err != nil {
		return err
	}
	kind := "iPhone"
	if info.Kind == "simulator" {
		kind = "simulator"
	}
	ok("%s %q (%s, %s)", kind, info.Name, info.OS, info.UDID)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dev := cfg.Devices[info.UDID]
	if dev == nil {
		dev = &config.Device{UDID: info.UDID}
	}
	dev.Name, dev.OS, dev.Kind = info.Name, info.OS, info.Kind
	if dev.IsSimulator() {
		if dev.Port == 0 {
			dev.Port = cfg.NextSimulatorPort()
		}
	} else {
		dev.Port = config.DevicePort
		if err := preparePhone(info); err != nil {
			return err
		}
		if err := pickTeam(a, dev); err != nil {
			return err
		}
	}

	// Stop an old helper so the new build can replace it.
	runner.New(dev).Shutdown()

	step("Building and signing the helper (the first build takes 1–3 minutes)")
	start := time.Now()
	stopTicker := ticker(start)
	err = helper.Build(dev, nil)
	stopTicker()
	if err != nil {
		var be *helper.BuildError
		if errors.As(err, &be) && strings.HasPrefix(be.Hint, "NEEDS_TRUST") {
			// Build succeeded far enough to install; trust is handled below.
		} else {
			return err
		}
	}
	ok("Built in %s", time.Since(start).Round(time.Second))
	if !dev.ProfileExpires.IsZero() {
		ok("Signature valid until %s", dev.ProfileExpires.Format("Mon Jan 2 15:04"))
	}
	cfg.Devices[dev.UDID] = dev
	if err := cfg.Save(); err != nil {
		return err
	}

	step("Starting the helper")
	if !a.bool("no-service") && !service.Installed() {
		daemon.Stop()
		if err := service.Install(); err != nil {
			warn("Could not install the login service (%v); using a background daemon instead", err)
		} else {
			ok("Installed the login service: iadb keeps the helper running and re-signs it automatically")
		}
	}
	if !service.Installed() {
		if err := daemon.Spawn(); err != nil {
			return err
		}
	}
	if !dev.IsSimulator() {
		fmt.Println("  Keep the phone unlocked. The first time, it may ask you to trust the developer:")
		fmt.Println("  Settings → General → VPN & Device Management → your Apple ID → Trust.")
	}
	client, err := session.Connect(dev.UDID, 5*time.Minute, func(format string, v ...any) {
		fmt.Printf("  … "+format+"\n", v...)
	})
	if err != nil {
		return err
	}
	st, err := client.Status(10 * time.Second)
	if err != nil {
		return err
	}
	ok("Helper %s running on %s (%s), screen %.0fx%.0f points", st.Version, dev.Name, st.OS, st.Screen.Width, st.Screen.Height)
	if !dev.IsSimulator() {
		client.Key("home")
	}

	fmt.Printf("\n%s is ready. Try:\n  iadb screenshot\n  iadb tree\n  iadb tap \"Settings\"\n\nFor AI agents: claude mcp add iadb -- iadb mcp\n", dev.Name)
	if dev.FreeTeam {
		fmt.Println("\nYou're signing with a free Apple ID, so the helper's signature lasts 7 days.")
		fmt.Println("iadb re-signs it automatically while the phone is connected to this Mac.")
	}
	return nil
}

// ticker prints elapsed time while a long step runs.
func ticker(start time.Time) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Printf("  … still working (%s)\n", time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() { close(done) }
}

// preparePhone handles pairing and Developer Mode, waiting for the user.
func preparePhone(info device.Info) error {
	if info.Connection == "offline" {
		return fmt.Errorf("%s is known to this Mac but not connected. Plug it in with a cable and unlock it", info.Name)
	}
	if !info.Paired {
		step("Pairing")
		fmt.Println("  Unlock your iPhone and tap Trust on the \"Trust This Computer?\" prompt.")
		if err := device.Pair(info.UDID); err != nil {
			return fmt.Errorf("pairing failed: %w", err)
		}
		ok("Paired")
	}
	if info.DeveloperMode != "enabled" {
		info.DeveloperMode = device.DeveloperMode(info.UDID)
	}
	if info.DeveloperMode == "enabled" {
		ok("Developer Mode is on")
		return nil
	}
	step("Developer Mode")
	fmt.Println("  Developer Mode is off. On your iPhone:")
	fmt.Println("    Settings → Privacy & Security → Developer Mode → On, then restart the phone.")
	fmt.Println("    After it restarts, unlock it and tap Turn On.")
	fmt.Println("  (If the Developer Mode switch is missing, keep the phone plugged in for a minute; it appears once Xcode has seen the phone.)")
	fmt.Println("  Waiting for Developer Mode (up to 10 minutes)…")
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if _, attached := usbmux.Find(info.UDID); !attached {
			continue // restarting
		}
		if device.DeveloperMode(info.UDID) == "enabled" {
			ok("Developer Mode is on")
			return nil
		}
	}
	return errors.New("Developer Mode is still off; turn it on and run `iadb setup` again")
}

// pickTeam chooses the signing team: --team, the saved one, or the best found.
func pickTeam(a args, dev *config.Device) error {
	step("Choosing a signing team")
	teams := signing.Teams()
	if t := a.str("team"); t != "" {
		dev.Team = t
		dev.FreeTeam = true
		for _, team := range teams {
			if team.ID == t {
				dev.FreeTeam = team.Free
			}
		}
		ok("Using team %s", t)
		return nil
	}
	if dev.Team != "" {
		for _, team := range teams {
			if team.ID == dev.Team {
				ok("Using team %s (%s)", team.Name, team.ID)
				return nil
			}
		}
	}
	if len(teams) == 0 {
		fmt.Println("  No Apple ID is signed in to Xcode. To fix it:")
		fmt.Println("    1. Open Xcode → Settings → Accounts")
		fmt.Println("    2. Click + and sign in with your Apple ID (a free one works)")
		fmt.Println("    3. Run `iadb setup` again")
		if os.Getenv("IADB_NO_OPEN") == "" {
			exec.Command("/usr/bin/open", "-a", "Xcode").Run()
		}
		return exitError{1, ""}
	}
	team := teams[0]
	dev.Team, dev.FreeTeam = team.ID, team.Free
	kind := "paid"
	if team.Free {
		kind = "free"
	}
	ok("Using %s (%s, %s account)", team.Name, team.ID, kind)
	if len(teams) > 1 {
		var others []string
		for _, t := range teams[1:] {
			others = append(others, t.ID+" "+t.Name)
		}
		fmt.Printf("    Other teams: %s (pick one with --team <id>)\n", strings.Join(others, ", "))
	}
	return nil
}
