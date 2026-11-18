// Package daemon keeps the on-device helper running for every set-up device
// and re-signs it before a free Apple ID's 7-day profile expires.
package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/safwanadnan/iphoneuse/internal/config"
	"github.com/safwanadnan/iphoneuse/internal/device"
	"github.com/safwanadnan/iphoneuse/internal/helper"
	"github.com/safwanadnan/iphoneuse/internal/runner"
)

// ResignBefore is how long before profile expiry the daemon re-signs.
const ResignBefore = 48 * time.Hour

func lockPath() string { return filepath.Join(config.Dir(), "daemon.lock") }

// lock takes the daemon's exclusive lock; it fails if a daemon is running.
func lock() (*os.File, error) {
	os.MkdirAll(config.Dir(), 0o755)
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another iphoneuse daemon is already running")
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// Running reports whether a daemon holds the lock, and its PID.
func Running() (bool, int) {
	f, err := os.OpenFile(lockPath(), os.O_RDWR, 0o644)
	if err != nil {
		return false, 0
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, 0
	}
	var pid int
	fmt.Fscan(f, &pid)
	return true, pid
}

// Spawn starts a background daemon if none is running.
func Spawn() error {
	if ok, _ := Running(); ok {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.MkdirAll(config.LogDir(), 0o755)
	logFile, err := os.OpenFile(filepath.Join(config.LogDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 20; i++ {
		if ok, _ := Running(); ok {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// Stop stops the running daemon (and with it every helper).
func Stop() error {
	ok, pid := Running()
	if !ok || pid == 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if ok, _ := Running(); !ok {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

type helperProc struct {
	cmd       *exec.Cmd
	done      chan struct{}
	hint      string
	ready     bool
	startedAt time.Time
}

type daemon struct {
	procs     map[string]*helperProc
	failures  map[string]int
	nextTry   map[string]time.Time
	nextBuild map[string]time.Time
	status    map[string]config.Status
}

// Run is the daemon's main loop.
func Run() error {
	lockFile, err := lock()
	if err != nil {
		return err
	}
	defer lockFile.Close()
	log.SetFlags(log.LstdFlags)
	log.Printf("iphoneuse daemon started (pid %d)", os.Getpid())

	d := &daemon{
		procs:     map[string]*helperProc{},
		failures:  map[string]int{},
		nextTry:   map[string]time.Time{},
		nextBuild: map[string]time.Time{},
		status:    config.LoadStatus(),
	}
	exe, _ := os.Executable()
	startInfo, _ := os.Stat(exe)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	d.tick()
	for {
		select {
		case <-sigs:
			log.Printf("stopping")
			for udid := range d.procs {
				d.stop(udid, "stopped")
			}
			config.SaveStatus(d.status)
			return nil
		case <-ticker.C:
			// If iphoneuse was upgraded, this process is running old code that would
			// fight the new version (e.g. rebuild the helper from stale source).
			// Exit so launchd, or the next command, starts the new binary.
			if info, err := os.Stat(exe); err == nil && startInfo != nil && !info.ModTime().Equal(startInfo.ModTime()) {
				log.Printf("iphoneuse binary changed on disk; restarting")
				for udid := range d.procs {
					d.stop(udid, "restarting")
				}
				config.SaveStatus(d.status)
				return nil
			}
			d.tick()
		}
	}
}

func (d *daemon) setStatus(udid, state, msg string, pid int) {
	prev := d.status[udid]
	if prev.State == state && prev.Message == msg && prev.PID == pid {
		return
	}
	d.status[udid] = config.Status{State: state, Message: msg, PID: pid, UpdatedAt: time.Now()}
	config.SaveStatus(d.status)
}

func (d *daemon) tick() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
		return
	}
	booted := map[string]bool{}
	for _, dev := range cfg.Devices {
		if dev.IsSimulator() {
			if sims, err := device.Simulators(); err == nil {
				for _, s := range sims {
					booted[s.UDID] = true
				}
			}
			break
		}
	}
	for udid, proc := range d.procs {
		if _, ok := cfg.Devices[udid]; !ok {
			d.stop(udid, "removed")
		} else {
			select {
			case <-proc.done:
				d.exited(udid, proc)
			default:
			}
		}
	}
	for udid, dev := range cfg.Devices {
		present := booted[udid]
		if !dev.IsSimulator() {
			present = device.Reachable(udid)
		}
		if !present {
			if _, ok := d.procs[udid]; ok {
				d.stop(udid, "offline")
			} else if d.status[udid].State != "offline" {
				d.setStatus(udid, "offline", "", 0)
			}
			continue
		}
		if helper.Building(udid) {
			continue // `iphoneuse setup` (or another daemon pass) is building right now
		}
		if p, ok := d.procs[udid]; ok && dev.BuiltAt.After(p.startedAt) {
			d.stop(udid, "restarting") // a newer build exists; run that one
		}
		if reason := buildReason(dev); reason != "" && time.Now().After(d.nextBuild[udid]) {
			if _, ok := d.procs[udid]; ok {
				runner.New(dev).Shutdown()
				d.stop(udid, "rebuilding")
			}
			d.rebuild(cfg, dev, reason)
			continue
		}
		if !usable(dev) {
			continue // nothing runnable until a build succeeds
		}
		if _, ok := d.procs[udid]; !ok && time.Now().After(d.nextTry[udid]) {
			d.start(dev)
		}
		if p, ok := d.procs[udid]; ok && !p.ready {
			if _, err := runner.New(dev).Status(2 * time.Second); err == nil {
				p.ready = true
				d.failures[udid] = 0
				d.setStatus(udid, "running", "", p.cmd.Process.Pid)
				log.Printf("%s: helper ready", dev.Name)
			}
		}
	}
}

// buildReason says why the helper must be (re)built now, or "" if it needn't.
func buildReason(dev *config.Device) string {
	if dev.XCTestRun == "" {
		return "first build"
	}
	if _, err := os.Stat(dev.XCTestRun); err != nil {
		return "build files missing"
	}
	if dev.HelperHash != helper.Hash() {
		return "helper updated"
	}
	if dev.ResignRequested {
		return "re-sign requested"
	}
	if !dev.IsSimulator() && !dev.ProfileExpires.IsZero() && time.Until(dev.ProfileExpires) < ResignBefore {
		return "signature expires " + dev.ProfileExpires.Local().Format("Mon Jan 2 15:04")
	}
	return ""
}

// usable reports whether the current build can run (exists, not expired).
func usable(dev *config.Device) bool {
	if dev.XCTestRun == "" {
		return false
	}
	if _, err := os.Stat(dev.XCTestRun); err != nil {
		return false
	}
	return dev.IsSimulator() || dev.ProfileExpires.IsZero() || time.Now().Before(dev.ProfileExpires)
}

// rebuild builds and signs the helper again. For a re-sign it first deletes
// the cached provisioning profile; otherwise Xcode would reuse it and the
// expiry date would never move.
func (d *daemon) rebuild(cfg *config.Config, dev *config.Device, reason string) {
	resign := !dev.IsSimulator() && (dev.ResignRequested || strings.HasPrefix(reason, "signature expires"))
	log.Printf("%s: rebuilding helper (%s)", dev.Name, reason)
	d.setStatus(dev.UDID, "building", reason, 0)
	before := dev.ProfileExpires
	if resign {
		if n := helper.ForgetProfiles(dev); n > 0 {
			log.Printf("%s: removed %d cached provisioning profile(s)", dev.Name, n)
		}
	}
	err := helper.Build(dev, nil)
	dev.ResignRequested = false
	if saveErr := cfg.Save(); saveErr != nil {
		log.Printf("config save: %v", saveErr)
	}
	if err != nil {
		log.Printf("%s: build failed: %v", dev.Name, err)
		d.setStatus(dev.UDID, "error", err.Error(), 0)
		d.nextBuild[dev.UDID] = time.Now().Add(15 * time.Minute)
		notify("iPhoneUse couldn't re-sign the helper", dev.Name+": run `iphoneuse doctor` for details")
		return
	}
	d.nextBuild[dev.UDID] = time.Time{}
	if resign {
		if !dev.ProfileExpires.After(before) {
			// Apple handed back the same profile; don't loop. Try again later.
			log.Printf("%s: re-sign did not extend the signature (still %s)", dev.Name, dev.ProfileExpires.Local().Format(time.RFC1123))
			d.nextBuild[dev.UDID] = time.Now().Add(6 * time.Hour)
		} else {
			log.Printf("%s: re-signed, now valid until %s", dev.Name, dev.ProfileExpires.Local().Format(time.RFC1123))
			notify("iPhoneUse renewed your iPhone helper", fmt.Sprintf("%s is good until %s", dev.Name, dev.ProfileExpires.Local().Format("Mon Jan 2")))
		}
	}
	d.start(dev)
}

func (d *daemon) backoff(udid string) {
	d.failures[udid]++
	delay := time.Duration(1<<min(d.failures[udid], 6)) * time.Second
	d.nextTry[udid] = time.Now().Add(delay)
}

func (d *daemon) start(dev *config.Device) {
	cmd, err := helper.RunnerCommand(context.Background(), dev)
	if err != nil {
		d.setStatus(dev.UDID, "error", err.Error(), 0)
		d.backoff(dev.UDID)
		return
	}
	logPath := filepath.Join(config.LogDir(), "helper-"+dev.UDID+".log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 5<<20 {
		os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		d.setStatus(dev.UDID, "error", err.Error(), 0)
		return
	}
	fmt.Fprintf(logFile, "\n===== %s starting helper =====\n", time.Now().Format(time.RFC3339))
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		d.setStatus(dev.UDID, "error", err.Error(), 0)
		d.backoff(dev.UDID)
		return
	}
	proc := &helperProc{cmd: cmd, done: make(chan struct{}), startedAt: time.Now()}
	d.procs[dev.UDID] = proc
	d.setStatus(dev.UDID, "starting", "", cmd.Process.Pid)
	log.Printf("%s: starting helper (pid %d)", dev.Name, cmd.Process.Pid)

	hints := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		var recent []string
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(logFile, line)
			recent = append(recent, line)
			if len(recent) > 200 {
				recent = recent[1:]
			}
			if h := helper.Diagnose(line); h != "" {
				select {
				case hints <- h:
				default:
				}
			}
		}
		logFile.Close()
		if h := helper.Diagnose(strings.Join(recent, "\n")); h != "" {
			select {
			case hints <- h:
			default:
			}
		}
		close(hints)
	}()
	go func() {
		cmd.Wait()
		pw.Close()
		for h := range hints {
			if proc.hint == "" {
				proc.hint = h
			}
		}
		close(proc.done)
	}()
}

func (d *daemon) exited(udid string, proc *helperProc) {
	delete(d.procs, udid)
	uptime := time.Since(proc.startedAt).Round(time.Second)
	msg := proc.hint
	if msg == "" {
		msg = fmt.Sprintf("helper exited after %s (see %s)", uptime, filepath.Join(config.LogDir(), "helper-"+udid+".log"))
	}
	log.Printf("%s: %s", udid, msg)
	state := "error"
	if strings.HasPrefix(msg, "NEEDS_TRUST") || strings.HasPrefix(msg, "NEEDS_UNLOCK") {
		state = "needs-trust"
		if strings.HasPrefix(msg, "NEEDS_UNLOCK") {
			state = "needs-unlock"
		}
		msg = msg[strings.Index(msg, ": ")+2:]
		d.nextTry[udid] = time.Now().Add(5 * time.Second)
		d.setStatus(udid, state, msg, 0)
		return
	}
	if uptime > 2*time.Minute {
		d.failures[udid] = 0
	}
	d.setStatus(udid, state, msg, 0)
	d.backoff(udid)
}

func (d *daemon) stop(udid, state string) {
	proc, ok := d.procs[udid]
	if !ok {
		return
	}
	if proc.cmd.Process != nil {
		syscall.Kill(-proc.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-proc.done:
		case <-time.After(5 * time.Second):
			syscall.Kill(-proc.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	delete(d.procs, udid)
	d.setStatus(udid, state, "", 0)
}

func notify(title, message string) {
	script := fmt.Sprintf("display notification %q with title %q", message, title)
	exec.Command("/usr/bin/osascript", "-e", script).Run()
}
