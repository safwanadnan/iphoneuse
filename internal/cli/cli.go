// Package cli implements the iadb command line.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/iadb-dev/iadb/internal/config"
	"github.com/iadb-dev/iadb/internal/daemon"
	"github.com/iadb-dev/iadb/internal/device"
	"github.com/iadb-dev/iadb/internal/mcp"
	"github.com/iadb-dev/iadb/internal/runner"
	"github.com/iadb-dev/iadb/internal/service"
	"github.com/iadb-dev/iadb/internal/session"
)

// Version is set at build time.
var Version = "dev"

const usage = `iadb — adb for iPhone. Control a real iPhone (or iOS Simulator) from your Mac.

Setup
  iadb setup                 Set up the connected iPhone (build, sign and install the helper)
  iadb doctor                Check Xcode, signing, devices and the helper
  iadb devices               List iPhones and booted simulators

Screen
  iadb screenshot [-o file]  Save a screenshot (PNG; --jpeg, --scale 0.5|points, -o - for stdout)
  iadb tree [--json]         List on-screen elements: [ref] Type "label" @(x,y)
  iadb app                   Show the foreground app
  iadb wait <text>           Wait until text appears on screen (--timeout 10)

Input
  iadb tap <x> <y>           Tap at points (also: tap <text>, tap --ref 3, --long, --double)
  iadb swipe <up|down|left|right> | <x1> <y1> <x2> <y2> [--duration 0.3]
  iadb type <text>           Type into the focused field
  iadb key <name>            home, lock, volume-up, volume-down, return, delete, tab, escape

Apps
  iadb launch <bundle-id>    Open an app (--restart to relaunch)
  iadb kill <bundle-id>      Force-quit an app
  iadb open <url>            Open a URL or deep link
  iadb apps                  List installed apps
  iadb install <path>        Install an .app or .ipa
  iadb uninstall <bundle-id> Remove an app

AI agents
  iadb mcp                   Run the MCP server on stdio (claude mcp add iadb -- iadb mcp)

Background service
  iadb service install       Start iadb at login and auto re-sign before the 7-day expiry
  iadb service uninstall     Remove the login service
  iadb start-server          Start the background daemon now
  iadb kill-server           Stop the daemon and all helpers
  iadb logs [-n 50]          Show the daemon log
  iadb forget                Remove a device's setup

Global flags
  -s <udid|name>             Pick a device (or set IADB_DEVICE)
  --version                  Print the version
`

// Main runs the CLI and returns the process exit code.
func Main(argv []string) int {
	a, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "iadb:", err)
		return 2
	}
	if a.bool("version") || (len(a.pos) > 0 && a.pos[0] == "version") {
		fmt.Println("iadb", Version)
		return 0
	}
	if len(a.pos) == 0 || a.bool("h") || a.bool("help") || a.pos[0] == "help" {
		fmt.Print(usage)
		return 0
	}
	cmd, rest := a.pos[0], a.pos[1:]
	a.pos = rest
	handlers := map[string]func(args) error{
		"setup": cmdSetup, "doctor": cmdDoctor, "devices": cmdDevices,
		"screenshot": cmdScreenshot, "screencap": cmdScreenshot, "tree": cmdTree, "app": cmdApp, "wait": cmdWait,
		"tap": cmdTap, "swipe": cmdSwipe, "type": cmdType, "text": cmdType, "key": cmdKey, "keyevent": cmdKey,
		"launch": cmdLaunch, "kill": cmdKill, "open": cmdOpen, "apps": cmdApps,
		"install": cmdInstall, "uninstall": cmdUninstall,
		"mcp": cmdMCP, "service": cmdService, "daemon": cmdDaemon,
		"start-server": cmdStartServer, "kill-server": cmdKillServer, "logs": cmdLogs, "forget": cmdForget,
	}
	handler, ok := handlers[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "iadb: unknown command %q (run `iadb help`)\n", cmd)
		return 2
	}
	if err := handler(a); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			if exit.msg != "" {
				fmt.Fprintln(os.Stderr, exit.msg)
			}
			return exit.code
		}
		fmt.Fprintln(os.Stderr, "iadb:", err)
		return 1
	}
	return 0
}

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func progress(format string, v ...any) { fmt.Fprintf(os.Stderr, format+"\n", v...) }

func connect(a args) (*runner.Client, error) {
	return session.Connect(a.str("s"), 3*time.Minute, progress)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---------- devices ----------

func cmdDevices(a args) error {
	devs, err := device.All()
	if err != nil {
		return err
	}
	cfg, _ := config.Load()
	status := config.LoadStatus()
	if a.bool("json") {
		return printJSON(devs)
	}
	if len(devs) == 0 {
		fmt.Println("No iPhones or booted simulators found. Plug in your iPhone with a cable and unlock it.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "UDID\tNAME\tOS\tCONNECTION\tIADB")
	for _, d := range devs {
		state := "not set up"
		if c, ok := cfg.Devices[d.UDID]; ok {
			state = "set up"
			if st, ok := status[d.UDID]; ok && st.State != "" {
				state = st.State
			}
			if !c.ProfileExpires.IsZero() && time.Until(c.ProfileExpires) < 0 {
				state = "signature expired"
			}
		} else if d.Kind == "device" && d.DeveloperMode == "disabled" {
			state = "developer mode off"
		}
		name := d.Name
		if d.Kind == "simulator" {
			name += " (simulator)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.UDID, name, d.OS, d.Connection, state)
	}
	return w.Flush()
}

// ---------- screen ----------

func cmdScreenshot(a args) error {
	c, err := connect(a)
	if err != nil {
		return err
	}
	jpeg := a.bool("jpeg") || strings.HasSuffix(strings.ToLower(a.str("o")), ".jpg") || strings.HasSuffix(strings.ToLower(a.str("o")), ".jpeg")
	quality, err := a.float("quality", 0.8)
	if err != nil {
		return err
	}
	img, _, err := c.Screenshot(runner.ScreenshotOptions{JPEG: jpeg, Quality: quality, Scale: a.str("scale")})
	if err != nil {
		return err
	}
	out := a.str("o")
	if out == "" && len(a.pos) > 0 {
		out = a.pos[0]
	}
	if out == "-" {
		_, err := os.Stdout.Write(img)
		return err
	}
	if out == "" {
		ext := ".png"
		if jpeg {
			ext = ".jpg"
		}
		out = "screenshot-" + time.Now().Format("20060102-150405") + ext
	}
	if err := os.WriteFile(out, img, 0o644); err != nil {
		return err
	}
	abs, _ := filepath.Abs(out)
	fmt.Println(abs)
	return nil
}

func cmdTree(a args) error {
	c, err := connect(a)
	if err != nil {
		return err
	}
	limit, err := a.int("limit", 0)
	if err != nil {
		return err
	}
	tree, err := c.Tree(a.str("app"), limit)
	if err != nil {
		return err
	}
	if a.bool("json") {
		return printJSON(tree)
	}
	fmt.Println(tree.Text)
	return nil
}

func cmdApp(a args) error {
	c, err := connect(a)
	if err != nil {
		return err
	}
	id, name, err := c.ForegroundApp()
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", id, name)
	return nil
}

func cmdWait(a args) error {
	text := a.str("text")
	if text == "" && len(a.pos) > 0 {
		text = strings.Join(a.pos, " ")
	}
	if text == "" {
		return errors.New("usage: iadb wait <text> [--timeout 10] [--gone]")
	}
	timeout, err := a.float("timeout", 10)
	if err != nil {
		return err
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	gone := a.bool("gone")
	deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	needle := strings.ToLower(text)
	for {
		tree, err := c.Tree("", 0)
		if err == nil {
			found := false
			for _, e := range tree.Elements {
				if strings.Contains(strings.ToLower(e.Label), needle) || strings.Contains(strings.ToLower(e.Value), needle) || e.Identifier == text {
					found = true
					if !gone {
						fmt.Printf("[%d] %s %q @(%d,%d)\n", e.Ref, e.Type, e.Label, e.X, e.Y)
					}
					break
				}
			}
			if found != gone {
				return nil
			}
		}
		if time.Now().After(deadline) {
			state := "appear"
			if gone {
				state = "disappear"
			}
			return exitError{1, fmt.Sprintf("timed out after %gs waiting for %q to %s", timeout, text, state)}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---------- input ----------

func cmdTap(a args) error {
	target := map[string]any{}
	switch {
	case a.str("ref") != "":
		ref, err := strconv.Atoi(a.str("ref"))
		if err != nil {
			return errors.New("--ref must be a number")
		}
		target["ref"] = ref
	case a.str("text") != "":
		target["text"] = a.str("text")
	case len(a.pos) == 2 && isNumber(a.pos[0]) && isNumber(a.pos[1]):
		x, _ := strconv.ParseFloat(a.pos[0], 64)
		y, _ := strconv.ParseFloat(a.pos[1], 64)
		target["x"], target["y"] = x, y
	case len(a.pos) == 1 && strings.HasPrefix(a.pos[0], "@") && isNumber(a.pos[0][1:]):
		ref, _ := strconv.Atoi(a.pos[0][1:])
		target["ref"] = ref
	case len(a.pos) >= 1:
		target["text"] = strings.Join(a.pos, " ")
	default:
		return errors.New("usage: iadb tap <x> <y> | tap <text> | tap --ref <n> [--long] [--double]")
	}
	if a.bool("long") {
		d, err := a.float("duration", 1.0)
		if err != nil {
			return err
		}
		target["duration"] = d
	} else if a.str("duration") != "" {
		d, err := a.float("duration", 0)
		if err != nil {
			return err
		}
		target["duration"] = d
	}
	if a.bool("double") {
		target["count"] = 2
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	r, err := c.Tap(target)
	if err != nil {
		return err
	}
	fmt.Printf("tapped %s at (%.0f,%.0f)\n", r.Tapped, r.X, r.Y)
	return nil
}

func cmdSwipe(a args) error {
	duration, err := a.float("duration", 0.3)
	if err != nil {
		return err
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	var x1, y1, x2, y2 float64
	switch {
	case len(a.pos) == 1:
		st, err := c.Status(5 * time.Second)
		if err != nil {
			return err
		}
		x1, y1, x2, y2, err = mcp.DirectionToPoints(a.pos[0], st.Screen.Width, st.Screen.Height)
		if err != nil {
			return err
		}
	case len(a.pos) == 4:
		var vals [4]float64
		for i, p := range a.pos {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return fmt.Errorf("%q is not a number", p)
			}
			vals[i] = v
		}
		x1, y1, x2, y2 = vals[0], vals[1], vals[2], vals[3]
	default:
		return errors.New("usage: iadb swipe <up|down|left|right> | iadb swipe <x1> <y1> <x2> <y2> [--duration 0.3]")
	}
	return c.Swipe(x1, y1, x2, y2, duration)
}

func cmdType(a args) error {
	if len(a.pos) == 0 {
		return errors.New("usage: iadb type <text>")
	}
	text := strings.Join(a.pos, " ")
	if a.bool("enter") {
		text += "\n"
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	return c.Type(text)
}

func cmdKey(a args) error {
	if len(a.pos) != 1 {
		return errors.New("usage: iadb key <home|lock|volume-up|volume-down|return|delete|tab|escape>")
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	return c.Key(a.pos[0])
}

// ---------- apps ----------

func oneArg(a args, usage string) (string, error) {
	if len(a.pos) != 1 {
		return "", errors.New("usage: " + usage)
	}
	return a.pos[0], nil
}

func cmdLaunch(a args) error {
	id, err := oneArg(a, "iadb launch <bundle-id> [--restart]")
	if err != nil {
		return err
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	return c.Launch(id, a.bool("restart"))
}

func cmdKill(a args) error {
	id, err := oneArg(a, "iadb kill <bundle-id>")
	if err != nil {
		return err
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	return c.Terminate(id)
}

func cmdOpen(a args) error {
	u, err := oneArg(a, "iadb open <url>")
	if err != nil {
		return err
	}
	c, err := connect(a)
	if err != nil {
		return err
	}
	return c.Open(u)
}

// targetDevice resolves a device for commands that don't need the helper.
func targetDevice(a args) (string, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", false, err
	}
	if d, err := session.FindConfigured(cfg, session.Selector(a.str("s"))); err == nil {
		return d.UDID, d.IsSimulator(), nil
	}
	info, err := device.Resolve(session.Selector(a.str("s")))
	if err != nil {
		return "", false, err
	}
	return info.UDID, info.Kind == "simulator", nil
}

func cmdApps(a args) error {
	udid, sim, err := targetDevice(a)
	if err != nil {
		return err
	}
	apps, err := device.Apps(udid, sim)
	if err != nil {
		return err
	}
	if a.bool("json") {
		return printJSON(apps)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, app := range apps {
		fmt.Fprintf(w, "%s\t%s\t%s\n", app.BundleID, app.Name, app.Version)
	}
	return w.Flush()
}

func cmdInstall(a args) error {
	path, err := oneArg(a, "iadb install <path.app|path.ipa>")
	if err != nil {
		return err
	}
	udid, sim, err := targetDevice(a)
	if err != nil {
		return err
	}
	if err := device.Install(udid, sim, path); err != nil {
		return err
	}
	fmt.Println("Success")
	return nil
}

func cmdUninstall(a args) error {
	id, err := oneArg(a, "iadb uninstall <bundle-id>")
	if err != nil {
		return err
	}
	udid, sim, err := targetDevice(a)
	if err != nil {
		return err
	}
	if err := device.Uninstall(udid, sim, id); err != nil {
		return err
	}
	fmt.Println("Success")
	return nil
}

// ---------- MCP & daemon ----------

func cmdMCP(a args) error {
	if len(a.pos) > 0 && a.pos[0] == "config" {
		exe, _ := os.Executable()
		fmt.Printf(`Claude Code:
  claude mcp add iadb -- %[1]s mcp

Claude Desktop / Cursor / other clients (JSON config):
  {
    "mcpServers": {
      "iadb": { "command": %[2]q, "args": ["mcp"] }
    }
  }
`, exe, exe)
		return nil
	}
	s := &mcp.Server{Version: Version}
	return s.Serve(os.Stdin, os.Stdout)
}

func cmdDaemon(a args) error { return daemon.Run() }

func cmdStartServer(a args) error {
	if service.Installed() {
		fmt.Println("iadb runs as a login service; it is managed by launchd.")
		return nil
	}
	if err := daemon.Spawn(); err != nil {
		return err
	}
	_, pid := daemon.Running()
	fmt.Printf("daemon running (pid %d)\n", pid)
	return nil
}

func cmdKillServer(a args) error {
	if service.Loaded() {
		fmt.Println("Stopping the login service too (run `iadb service install` to bring it back).")
		service.Uninstall()
	}
	return daemon.Stop()
}

func cmdService(a args) error {
	sub := ""
	if len(a.pos) > 0 {
		sub = a.pos[0]
	}
	switch sub {
	case "install":
		daemon.Stop()
		if err := service.Install(); err != nil {
			return err
		}
		fmt.Println("iadb now starts at login and re-signs the helper before it expires.")
		return nil
	case "uninstall":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Login service removed.")
		return nil
	case "status", "":
		running, pid := daemon.Running()
		fmt.Printf("service installed: %v\nservice loaded:    %v\ndaemon running:    %v", service.Installed(), service.Loaded(), running)
		if running {
			fmt.Printf(" (pid %d)", pid)
		}
		fmt.Println()
		return nil
	}
	return errors.New("usage: iadb service install|uninstall|status")
}

func cmdLogs(a args) error {
	n, err := a.int("n", 50)
	if err != nil {
		return err
	}
	path := filepath.Join(config.LogDir(), "daemon.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no daemon log yet (%s)", path)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	fmt.Fprintf(os.Stderr, "\n(%s; helper and build logs are in the same folder)\n", path)
	return nil
}

func cmdForget(a args) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dev, err := session.FindConfigured(cfg, session.Selector(a.str("s")))
	if err != nil {
		return err
	}
	runner.New(dev).Shutdown()
	delete(cfg.Devices, dev.UDID)
	if err := cfg.Save(); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(config.Dir(), "build", dev.UDID))
	fmt.Printf("Forgot %s. The helper app stays on the phone until you delete it (it's named IADBRunner).\n", dev.Name)
	return nil
}
