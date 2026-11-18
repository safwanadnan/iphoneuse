// Package config stores iadb's per-device setup state and file locations.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DevicePort is the port the helper listens on inside a physical iPhone.
const DevicePort = 47100

// Device is what `iadb setup` records for one phone or simulator.
type Device struct {
	UDID           string    `json:"udid"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"` // "device" or "simulator"
	OS             string    `json:"os"`
	Team           string    `json:"team,omitempty"`
	FreeTeam       bool      `json:"freeTeam,omitempty"`
	Port           int       `json:"port"`
	XCTestRun      string    `json:"xctestrun"`
	HelperHash     string    `json:"helperHash"`
	BuiltAt        time.Time `json:"builtAt"`
	ProfileExpires time.Time `json:"profileExpires,omitempty"`
}

// IsSimulator reports whether the device is an iOS Simulator.
func (d Device) IsSimulator() bool { return d.Kind == "simulator" }

// Config is the whole on-disk state.
type Config struct {
	Devices map[string]*Device `json:"devices"`
	Default string             `json:"default,omitempty"`
}

var mu sync.Mutex

// Dir is ~/Library/Application Support/iadb (override with IADB_HOME).
func Dir() string {
	if dir := os.Getenv("IADB_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "iadb")
}

// LogDir is ~/Library/Logs/iadb.
func LogDir() string {
	if dir := os.Getenv("IADB_HOME"); dir != "" {
		return filepath.Join(dir, "logs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "iadb")
}

func path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config, returning an empty one if none exists.
func Load() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()
	cfg := &Config{Devices: map[string]*Device{}}
	data, err := os.ReadFile(path())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Devices == nil {
		cfg.Devices = map[string]*Device{}
	}
	return cfg, nil
}

// Save writes the config atomically.
func (c *Config) Save() error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// SortedDevices returns configured devices ordered by name.
func (c *Config) SortedDevices() []*Device {
	var out []*Device
	for _, d := range c.Devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NextSimulatorPort picks a free localhost port for a new simulator.
func (c *Config) NextSimulatorPort() int {
	used := map[int]bool{}
	for _, d := range c.Devices {
		used[d.Port] = true
	}
	for p := DevicePort + 1; ; p++ {
		if !used[p] {
			return p
		}
	}
}

// Status is what the daemon reports about each device's helper.
type Status struct {
	State     string    `json:"state"` // starting, running, stopped, building, needs-trust, error
	Message   string    `json:"message,omitempty"`
	PID       int       `json:"pid,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func statusPath() string { return filepath.Join(Dir(), "status.json") }

// LoadStatus reads the daemon's status file.
func LoadStatus() map[string]Status {
	out := map[string]Status{}
	data, err := os.ReadFile(statusPath())
	if err == nil {
		json.Unmarshal(data, &out)
	}
	return out
}

// SaveStatus writes the daemon's status file.
func SaveStatus(s map[string]Status) {
	os.MkdirAll(Dir(), 0o755)
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := statusPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, statusPath())
	}
}
