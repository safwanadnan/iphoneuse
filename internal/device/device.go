// Package device discovers iPhones (via devicectl + usbmuxd) and booted
// iOS Simulators (via simctl).
package device

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/safwanadnan/iphoneuse/internal/usbmux"
	"github.com/safwanadnan/iphoneuse/internal/xcode"
	"howett.net/plist"
)

// Info describes one phone or simulator that iphoneuse can see.
type Info struct {
	UDID          string `json:"udid"`
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	OS            string `json:"os"`
	Kind          string `json:"kind"`       // "device" or "simulator"
	Connection    string `json:"connection"` // usb, wifi, local, offline
	Paired        bool   `json:"paired"`
	DeveloperMode string `json:"developerMode,omitempty"` // enabled, disabled, unknown
}

// Online reports whether iphoneuse can talk to the device right now.
func (i Info) Online() bool { return i.Connection != "offline" }

type devicectlList struct {
	Result struct {
		Devices []struct {
			Identifier       string `json:"identifier"`
			DeviceProperties struct {
				Name                string `json:"name"`
				OSVersionNumber     string `json:"osVersionNumber"`
				DeveloperModeStatus string `json:"developerModeStatus"`
			} `json:"deviceProperties"`
			HardwareProperties struct {
				UDID          string `json:"udid"`
				Platform      string `json:"platform"`
				MarketingName string `json:"marketingName"`
				Reality       string `json:"reality"`
			} `json:"hardwareProperties"`
			ConnectionProperties struct {
				PairingState    string `json:"pairingState"`
				TransportType   string `json:"transportType"`
				TunnelState     string `json:"tunnelState"`
				TunnelIPAddress string `json:"tunnelIPAddress"`
			} `json:"connectionProperties"`
		} `json:"devices"`
	} `json:"result"`
}

// Physical lists real iPhones/iPads known to this Mac.
func Physical() ([]Info, error) {
	data, err := xcode.RunJSON(30*time.Second, "devicectl", "list", "devices")
	if err != nil {
		return nil, err
	}
	var list devicectlList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	attached := map[string]string{}
	if devs, err := usbmux.List(); err == nil {
		for _, d := range devs {
			if attached[d.UDID] != "usb" {
				attached[d.UDID] = map[bool]string{true: "usb", false: "wifi"}[d.ConnectionType == "USB"]
			}
		}
	}
	var out []Info
	for _, d := range list.Result.Devices {
		hw := d.HardwareProperties
		if hw.Platform != "iOS" || hw.Reality == "virtual" || hw.UDID == "" {
			continue
		}
		conn := attached[hw.UDID]
		if conn == "" && d.ConnectionProperties.TransportType == "localNetwork" {
			conn = "wifi"
		}
		if conn == "" {
			conn = "offline"
		}
		mode := d.DeviceProperties.DeveloperModeStatus
		if mode == "" {
			mode = "unknown"
		}
		out = append(out, Info{
			UDID:          hw.UDID,
			Name:          d.DeviceProperties.Name,
			Model:         hw.MarketingName,
			OS:            "iOS " + d.DeviceProperties.OSVersionNumber,
			Kind:          "device",
			Connection:    conn,
			Paired:        d.ConnectionProperties.PairingState == "paired",
			DeveloperMode: mode,
		})
	}
	return out, nil
}

// Link is how a phone is reachable over the network right now.
type Link struct {
	Transport string // "wired", "localNetwork", or "" when not connected
	TunnelIP  string // CoreDevice tunnel address, set while a tunnel is up
}

var (
	linkMu    sync.Mutex
	linkCache map[string]Link
	linkAt    time.Time
)

// Links reports every phone's CoreDevice connection (cached for 5 s, since
// devicectl takes about half a second). This is what makes Wi-Fi work: when
// the cable is out, Xcode still reaches a paired phone over the local
// network, and while the helper runs there's a tunnel address to dial.
func Links() map[string]Link {
	linkMu.Lock()
	defer linkMu.Unlock()
	if linkCache != nil && time.Since(linkAt) < 5*time.Second {
		return linkCache
	}
	out := map[string]Link{}
	data, err := xcode.RunJSON(30*time.Second, "devicectl", "list", "devices")
	if err == nil {
		var list devicectlList
		if json.Unmarshal(data, &list) == nil {
			for _, d := range list.Result.Devices {
				c := d.ConnectionProperties
				link := Link{Transport: c.TransportType}
				if c.TunnelState == "connected" {
					link.TunnelIP = c.TunnelIPAddress
				}
				out[d.HardwareProperties.UDID] = link
			}
		}
	}
	linkCache, linkAt = out, time.Now()
	return out
}

// Reachable reports whether a phone can be used right now, by cable (usbmuxd)
// or over Wi-Fi (CoreDevice local network).
func Reachable(udid string) bool {
	if _, ok := usbmux.Find(udid); ok {
		return true
	}
	l := Links()[udid]
	return l.Transport == "localNetwork" || l.Transport == "wired"
}

// DeveloperMode asks the phone whether Developer Mode is on
// ("enabled", "disabled" or "unknown"). Newer Xcodes only report it in the
// per-device details, not in the device list.
func DeveloperMode(udid string) string {
	data, err := xcode.RunJSON(60*time.Second, "devicectl", "device", "info", "details", "--device", udid)
	if err != nil {
		return "unknown"
	}
	var details struct {
		Result struct {
			DeviceProperties struct {
				DeveloperModeStatus string `json:"developerModeStatus"`
			} `json:"deviceProperties"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &details) != nil || details.Result.DeviceProperties.DeveloperModeStatus == "" {
		return "unknown"
	}
	return details.Result.DeviceProperties.DeveloperModeStatus
}

// Simulators lists booted iOS Simulators.
func Simulators() ([]Info, error) {
	out, err := xcode.Run(30*time.Second, "simctl", "list", "devices", "booted", "-j")
	if err != nil {
		return nil, err
	}
	var list struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	var sims []Info
	for runtime, devs := range list.Devices {
		if !strings.Contains(runtime, "iOS") {
			continue
		}
		version := runtime[strings.LastIndex(runtime, "iOS-")+4:]
		for _, d := range devs {
			if d.State != "Booted" {
				continue
			}
			sims = append(sims, Info{
				UDID: d.UDID, Name: d.Name, OS: "iOS " + strings.ReplaceAll(version, "-", "."),
				Kind: "simulator", Connection: "local", Paired: true, DeveloperMode: "enabled",
			})
		}
	}
	return sims, nil
}

// All lists phones first, then booted simulators.
func All() ([]Info, error) {
	phones, perr := Physical()
	sims, serr := Simulators()
	if perr != nil && serr != nil {
		return nil, perr
	}
	sort.SliceStable(phones, func(i, j int) bool { return phones[i].Online() && !phones[j].Online() })
	return append(phones, sims...), nil
}

// Resolve picks a device by UDID, UDID prefix, or name. With an empty
// selector it picks the only online device, or fails if there are several.
func Resolve(selector string) (Info, error) {
	all, err := All()
	if err != nil {
		return Info{}, err
	}
	if selector != "" {
		var matches []Info
		for _, d := range all {
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
			return Info{}, fmt.Errorf("%q matches %d devices; use the full UDID", selector, len(matches))
		}
		return Info{}, fmt.Errorf("no device matches %q (run `iphoneuse devices`)", selector)
	}
	var online []Info
	for _, d := range all {
		if d.Online() {
			online = append(online, d)
		}
	}
	switch len(online) {
	case 0:
		return Info{}, fmt.Errorf("no iPhone connected and no simulator booted. Plug in your iPhone with a cable and unlock it")
	case 1:
		return online[0], nil
	}
	return Info{}, fmt.Errorf("more than one device is connected; pick one with -s <udid|name> (see `iphoneuse devices`)")
}

// App is an installed app.
type App struct {
	BundleID string `json:"bundleId"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
}

// Apps lists user-installed apps on a device or simulator.
func Apps(udid string, simulator bool) ([]App, error) {
	var apps []App
	if simulator {
		out, err := xcode.Run(30*time.Second, "simctl", "listapps", udid)
		if err != nil {
			return nil, err
		}
		var list map[string]map[string]any
		if _, err := plist.Unmarshal(out, &list); err != nil {
			return nil, err
		}
		for id, info := range list {
			name, _ := info["CFBundleDisplayName"].(string)
			if name == "" {
				name, _ = info["CFBundleName"].(string)
			}
			version, _ := info["CFBundleShortVersionString"].(string)
			apps = append(apps, App{BundleID: id, Name: name, Version: version})
		}
	} else {
		data, err := xcode.RunJSON(60*time.Second, "devicectl", "device", "info", "apps", "--device", udid)
		if err != nil {
			return nil, err
		}
		var list struct {
			Result struct {
				Apps []struct {
					BundleIdentifier string `json:"bundleIdentifier"`
					Name             string `json:"name"`
					Version          string `json:"version"`
				} `json:"apps"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		for _, a := range list.Result.Apps {
			apps = append(apps, App{BundleID: a.BundleIdentifier, Name: a.Name, Version: a.Version})
		}
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	return apps, nil
}

// Install installs an .app or .ipa.
func Install(udid string, simulator bool, path string) error {
	if simulator {
		_, err := xcode.Run(5*time.Minute, "simctl", "install", udid, path)
		return err
	}
	_, err := xcode.Run(10*time.Minute, "devicectl", "device", "install", "app", "--device", udid, path)
	return err
}

// Uninstall removes an app by bundle id.
func Uninstall(udid string, simulator bool, bundleID string) error {
	if simulator {
		_, err := xcode.Run(2*time.Minute, "simctl", "uninstall", udid, bundleID)
		return err
	}
	_, err := xcode.Run(2*time.Minute, "devicectl", "device", "uninstall", "app", "--device", udid, bundleID)
	return err
}

// Pair asks the phone to trust this Mac (shows the "Trust This Computer?" prompt).
func Pair(udid string) error {
	_, err := xcode.Run(2*time.Minute, "devicectl", "manage", "pair", "--device", udid)
	return err
}
