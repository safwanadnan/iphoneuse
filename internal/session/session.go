// Package session turns a device selector into a ready helper client,
// starting the background daemon and waiting for the helper if needed.
package session

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iadb-dev/iadb/internal/config"
	"github.com/iadb-dev/iadb/internal/daemon"
	"github.com/iadb-dev/iadb/internal/device"
	"github.com/iadb-dev/iadb/internal/runner"
	"github.com/iadb-dev/iadb/internal/usbmux"
)

// Selector returns the device chosen by flag, then $IADB_DEVICE.
func Selector(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("IADB_DEVICE")
}

// FindConfigured resolves a selector against set-up devices without asking
// Xcode (fast path used by every command).
func FindConfigured(cfg *config.Config, selector string) (*config.Device, error) {
	if len(cfg.Devices) == 0 {
		return nil, errors.New("no device is set up yet. Plug in your iPhone and run: iadb setup")
	}
	if selector != "" {
		var matches []*config.Device
		for _, d := range cfg.Devices {
			if d.UDID == selector {
				return d, nil
			}
			if strings.HasPrefix(d.UDID, selector) || strings.EqualFold(d.Name, selector) {
				matches = append(matches, d)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("%q matches more than one device; use the full UDID", selector)
		}
		// Maybe it is a real device that isn't set up yet.
		if info, err := device.Resolve(selector); err == nil {
			return nil, fmt.Errorf("%s is not set up yet. Run: iadb setup -s %s", info.Name, info.UDID)
		}
		return nil, fmt.Errorf("no set-up device matches %q (see `iadb devices`)", selector)
	}
	var online []*config.Device
	devs := cfg.SortedDevices()
	var sims map[string]bool
	for _, d := range devs {
		if d.IsSimulator() {
			if sims == nil {
				sims = map[string]bool{}
				if list, err := device.Simulators(); err == nil {
					for _, s := range list {
						sims[s.UDID] = true
					}
				}
			}
			if sims[d.UDID] {
				online = append(online, d)
			}
		} else if _, ok := usbmux.Find(d.UDID); ok {
			online = append(online, d)
		}
	}
	sort.SliceStable(online, func(i, j int) bool { return !online[i].IsSimulator() && online[j].IsSimulator() })
	if len(online) == 1 || (len(online) > 1 && cfg.Default != "" && containsUDID(online, cfg.Default)) {
		if len(online) > 1 {
			for _, d := range online {
				if d.UDID == cfg.Default {
					return d, nil
				}
			}
		}
		return online[0], nil
	}
	if len(online) > 1 {
		return nil, errors.New("more than one device is connected; pick one with -s <udid|name> or set IADB_DEVICE")
	}
	if len(devs) == 1 {
		return nil, fmt.Errorf("%s is not connected. Plug it in with a cable and unlock it", devs[0].Name)
	}
	return nil, errors.New("none of your set-up devices is connected. Plug one in with a cable and unlock it")
}

func containsUDID(list []*config.Device, udid string) bool {
	for _, d := range list {
		if d.UDID == udid {
			return true
		}
	}
	return false
}

// Connect returns a client whose helper is answering, starting the daemon
// and waiting up to wait for the helper to come up. Progress goes to logf.
func Connect(selector string, wait time.Duration, logf func(string, ...any)) (*runner.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dev, err := FindConfigured(cfg, Selector(selector))
	if err != nil {
		return nil, err
	}
	client := runner.New(dev)
	if _, err := client.Status(3 * time.Second); err == nil {
		return client, nil
	}
	if err := daemon.Spawn(); err != nil {
		return nil, fmt.Errorf("could not start the iadb daemon: %w", err)
	}
	if logf != nil {
		logf("Starting the iadb helper on %s…", dev.Name)
	}
	deadline := time.Now().Add(wait)
	lastMsg := ""
	for time.Now().Before(deadline) {
		if _, err := client.Status(2 * time.Second); err == nil {
			return client, nil
		}
		st := config.LoadStatus()[dev.UDID]
		switch st.State {
		case "needs-trust":
			if st.Message != lastMsg && logf != nil {
				logf("Waiting for you: %s", st.Message)
				lastMsg = st.Message
			}
		case "error":
			if time.Since(st.UpdatedAt) < 30*time.Second {
				return nil, fmt.Errorf("the helper on %s failed: %s", dev.Name, st.Message)
			}
		case "building":
			if st.Message != lastMsg && logf != nil {
				logf("Re-signing the helper (%s)…", st.Message)
				lastMsg = st.Message
			}
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("the helper on %s did not start within %s; check `iadb doctor` and %s", dev.Name, wait, config.LogDir())
}
