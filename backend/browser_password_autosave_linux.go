//go:build linux

package backend

import (
	"bufio"
	"context"
	"fmt"
	"github.com/godbus/dbus/v5"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func browserPasswordAutosaveSupported() bool { return true }
func browserPasswordAutosaveLaunchArgs(args []string) []string {
	// Do not override an explicit accessibility choice or enable screen-reader mode.
	for _, arg := range args {
		if arg == "--disable-renderer-accessibility" || strings.HasPrefix(arg, "--force-renderer-accessibility") {
			return args
		}
	}
	return append(args, "--force-renderer-accessibility=basic")
}

type browserPasswordTarget struct {
	id, directory, started string
	pid                    int
	profile                *BrowserProfile
}
type browserPasswordProcess struct {
	PID       int
	StartTime string
}
type browserPasswordSeenButton struct {
	firstSeen time.Time
	submitted bool
	profileID string
}

func (a *App) browserPasswordTargets() []browserPasswordTarget {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	var targets []browserPasswordTarget
	for id, p := range a.browserMgr.Profiles {
		if p != nil && p.Running && p.DeletedAt == "" {
			targets = append(targets, browserPasswordTarget{id: id, directory: a.browserMgr.ResolveUserDataDir(p), started: p.LastStartAt, pid: p.Pid, profile: p})
		}
	}
	return targets
}
func (a *App) browserPasswordTargetCurrent(target browserPasswordTarget) bool {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	p := a.browserMgr.Profiles[target.id]
	return p != nil && p == target.profile && p.Running && p.DeletedAt == "" && p.Pid == target.pid && p.LastStartAt == target.started && a.browserMgr.ResolveUserDataDir(p) == target.directory
}

type browserPasswordAXBus struct {
	connection *dbus.Conn
	ctx        context.Context
}
type browserPasswordAXRef struct {
	Bus  string
	Path dbus.ObjectPath
}

func (bus *browserPasswordAXBus) call(ref browserPasswordAccessible, method string, args ...any) *dbus.Call {
	return bus.connection.Object(ref.Bus, dbus.ObjectPath(ref.Path)).CallWithContext(bus.ctx, method, 0, args...)
}
func (bus *browserPasswordAXBus) Role(ref browserPasswordAccessible) (uint32, error) {
	var value uint32
	err := bus.call(ref, "org.a11y.atspi.Accessible.GetRole").Store(&value)
	return value, err
}
func (bus *browserPasswordAXBus) Name(ref browserPasswordAccessible) (string, error) {
	var value dbus.Variant
	err := bus.call(ref, "org.freedesktop.DBus.Properties.Get", "org.a11y.atspi.Accessible", "Name").Store(&value)
	name, _ := value.Value().(string)
	return name, err
}
func (bus *browserPasswordAXBus) State(ref browserPasswordAccessible) ([]uint32, error) {
	var value []uint32
	err := bus.call(ref, "org.a11y.atspi.Accessible.GetState").Store(&value)
	return value, err
}
func (bus *browserPasswordAXBus) Children(ref browserPasswordAccessible) ([]browserPasswordAccessible, error) {
	var value []browserPasswordAXRef
	err := bus.call(ref, "org.a11y.atspi.Accessible.GetChildren").Store(&value)
	result := make([]browserPasswordAccessible, 0, len(value))
	for _, child := range value {
		result = append(result, browserPasswordAccessible{Bus: child.Bus, Path: string(child.Path)})
	}
	return result, err
}
func (bus *browserPasswordAXBus) Actions(ref browserPasswordAccessible) ([]string, error) {
	// Chromium can return empty names in GetActions even though the indexed
	// action API exposes "press". Use the same indexed API as AT-SPI clients.
	var count dbus.Variant
	if err := bus.call(ref, "org.freedesktop.DBus.Properties.Get", "org.a11y.atspi.Action", "NActions").Store(&count); err != nil {
		return nil, err
	}
	return browserPasswordActionNames(count.Value(), func(index int32) (string, error) {
		var name string
		err := bus.call(ref, "org.a11y.atspi.Action.GetName", index).Store(&name)
		return name, err
	})
}
func browserPasswordActionNames(value any, getName func(int32) (string, error)) ([]string, error) {
	var count uint32
	switch typed := value.(type) {
	case int32:
		if typed < 0 {
			return nil, fmt.Errorf("invalid native action count")
		}
		count = uint32(typed)
	case uint32:
		count = typed
	default:
		return nil, fmt.Errorf("invalid native action count")
	}
	if count > 16 {
		return nil, fmt.Errorf("native action limit exceeded")
	}
	names := make([]string, int(count))
	for index := range names {
		name, err := getName(int32(index))
		if err != nil {
			return nil, err
		}
		names[index] = name
	}
	return names, nil
}
func (bus *browserPasswordAXBus) pid(name string) (int, error) {
	var value uint32
	err := bus.connection.BusObject().CallWithContext(bus.ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&value)
	return int(value), err
}
func connectBrowserPasswordAXBus(ctx context.Context) (*dbus.Conn, error) {
	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var address string
	if err := session.Object("org.a11y.Bus", "/org/a11y/bus").CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0).Store(&address); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(address, "unix:") {
		return nil, fmt.Errorf("accessibility bus is not local")
	}
	return dbus.Connect(address, dbus.WithContext(ctx))
}
func (a *App) watchBrowserPasswordAutosave(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var connection *dbus.Conn
	defer func() {
		if connection != nil {
			connection.Close()
		}
	}()
	seen := make(map[string]*browserPasswordSeenButton)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		targets := a.browserPasswordTargets()
		profiles := make(map[string]bool)
		for _, target := range targets {
			profiles[target.id] = true
		}
		for key, state := range seen {
			if !profiles[state.profileID] {
				delete(seen, key)
			}
		}
		if len(targets) == 0 {
			clear(seen)
			continue
		}
		if connection == nil || !connection.Connected() {
			var err error
			connection, err = connectBrowserPasswordAXBus(ctx)
			if err != nil {
				continue
			}
		}
		cycle, cancel := context.WithTimeout(ctx, 5*time.Second)
		bus := &browserPasswordAXBus{connection: connection, ctx: cycle}
		apps, err := bus.Children(browserPasswordAccessible{Bus: "org.a11y.atspi.Registry", Path: "/org/a11y/atspi/accessible/root"})
		if err != nil {
			cancel()
			connection.Close()
			connection = nil
			continue
		}
		active := make(map[string]bool)
		for _, root := range apps {
			pid, err := bus.pid(root.Bus)
			if err != nil {
				continue
			}
			for _, target := range targets {
				if target.pid > 0 && target.pid != pid {
					continue
				}
				process, err := browserPasswordProcessIdentity("/proc", target.directory, pid)
				if err != nil {
					continue
				}
				buttons, err := inspectBrowserPasswordAccessible(bus, root)
				if err != nil {
					continue
				}
				for _, button := range buttons {
					key := fmt.Sprintf("%s:%d:%s:%s:%s", target.id, pid, process.StartTime, button.Ref.Bus, button.Ref.Path)
					active[key] = true
					state := seen[key]
					if state == nil {
						if len(seen) < 4096 {
							seen[key] = &browserPasswordSeenButton{firstSeen: time.Now(), profileID: target.id}
						}
						continue
					}
					if state.submitted || time.Since(state.firstSeen) < 1500*time.Millisecond {
						continue
					}
					current := func() bool {
						if cycle.Err() != nil || !a.browserPasswordTargetCurrent(target) {
							return false
						}
						owner, err := bus.pid(button.Ref.Bus)
						if err != nil || owner != pid {
							return false
						}
						now, err := browserPasswordProcessIdentity("/proc", target.directory, pid)
						return err == nil && now == process
					}
					if !current() {
						continue
					}
					fresh, err := inspectBrowserPasswordAccessible(bus, root)
					if err != nil {
						continue
					}
					matched := false
					for _, candidate := range fresh {
						if candidate == button {
							matched = true
						}
					}
					if !matched || !current() {
						continue
					}
					state.submitted = true // A timeout never authorizes a repeated press.
					var accepted bool
					_ = bus.call(button.Ref, "org.a11y.atspi.Action.DoAction", button.Action).Store(&accepted)
				}
			}
		}
		for key, state := range seen {
			if !active[key] && !state.submitted {
				delete(seen, key)
			}
		}
		cancel()
	}
}
func browserPasswordCanonicalDirectory(value string) string {
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}
func browserPasswordProcessIdentity(procRoot, directory string, pid int) (browserPasswordProcess, error) {
	var result browserPasswordProcess
	if pid <= 0 || directory == "" {
		return result, fmt.Errorf("invalid browser process")
	}
	processDir := filepath.Join(procRoot, strconv.Itoa(pid))
	command, err := os.ReadFile(filepath.Join(processDir, "cmdline"))
	if err != nil || len(command) > 1<<20 {
		return result, fmt.Errorf("browser command unavailable")
	}
	args := strings.Split(strings.TrimRight(string(command), "\x00"), "\x00")
	// Some fingerprint kernels flatten argv into one display string. Parse its
	// process type, but do not infer argument boundaries for directories: an
	// unquoted path containing spaces cannot be recovered from that format.
	flattened := len(args) == 1 && strings.ContainsAny(args[0], " \t\r\n")
	if len(args) == 1 {
		args = splitProcessCommandLine(args[0])
	}
	for _, arg := range args {
		if arg == "--type" || strings.HasPrefix(arg, "--type=") {
			return result, fmt.Errorf("browser subprocess is not a password target")
		}
	}
	start, err := browserPasswordProcessStartTime(processDir)
	if err != nil {
		return result, err
	}
	hasDirectory := false
	for _, arg := range args {
		if !flattened && (arg == "--user-data-dir" || strings.HasPrefix(arg, "--user-data-dir=")) {
			hasDirectory = true
		}
	}
	if hasDirectory && browserPasswordCanonicalDirectory(processCommandLineFlagValue(args, "--user-data-dir")) != browserPasswordCanonicalDirectory(directory) {
		return result, fmt.Errorf("browser process identity mismatch")
	}
	// Fingerprint kernels may erase or flatten switches in /proc/cmdline. Bind
	// their main process through Chromium's profile-specific singleton listener
	// instead; never override an explicit different directory or subprocess.
	if !hasDirectory {
		if err := browserPasswordSingletonOwner(procRoot, directory, pid); err != nil {
			return result, err
		}
	}
	if current, err := browserPasswordProcessStartTime(processDir); err != nil || current != start {
		return result, fmt.Errorf("browser process changed during identity check")
	}
	return browserPasswordProcess{PID: pid, StartTime: start}, nil
}

func browserPasswordProcessStartTime(processDir string) (string, error) {
	stat, err := os.ReadFile(filepath.Join(processDir, "stat"))
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("invalid process start time")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", fmt.Errorf("invalid process start time")
	}
	return fields[19], nil
}

func browserPasswordSingletonOwner(procRoot, directory string, pid int) error {
	lockPath, socketLink := filepath.Join(directory, "SingletonLock"), filepath.Join(directory, "SingletonSocket")
	lock, err := os.Readlink(lockPath)
	if err != nil {
		return fmt.Errorf("browser singleton lock unavailable")
	}
	hostname, err := os.Hostname()
	if err != nil || lock != hostname+"-"+strconv.Itoa(pid) {
		return fmt.Errorf("browser singleton lock belongs to another process")
	}
	target, err := os.Readlink(socketLink)
	if err != nil || target == "" {
		return fmt.Errorf("browser singleton socket unavailable")
	}
	socketPath := target
	if !filepath.IsAbs(socketPath) {
		socketPath = filepath.Join(directory, socketPath)
	}
	socketPath = filepath.Clean(socketPath)
	info, err := os.Stat(socketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("browser singleton is not a live socket")
	}
	file, err := os.Open(filepath.Join(procRoot, "net", "unix"))
	if err != nil {
		return err
	}
	defer file.Close()
	inode := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[4] != "0001" || fields[5] != "01" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x10000 == 0 {
			continue
		}
		// Leave the path intact: profile/temp directories can contain spaces.
		for index := 0; index < 7; index++ {
			line = strings.TrimLeft(line, " \t")
			end := strings.IndexAny(line, " \t")
			if end < 0 {
				line = ""
				break
			}
			line = line[end:]
		}
		if strings.TrimLeft(line, " \t") != socketPath {
			continue
		}
		if value, err := strconv.ParseUint(fields[6], 10, 64); err == nil && value > 0 {
			if inode != "" && inode != fields[6] {
				return fmt.Errorf("ambiguous browser singleton listener")
			}
			inode = fields[6]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if inode == "" {
		return fmt.Errorf("browser singleton listener missing")
	}
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return err
	}
	owned := false
	for _, fd := range fds {
		if value, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && value == "socket:["+inode+"]" {
			owned = true
			break
		}
	}
	if !owned {
		return fmt.Errorf("browser singleton listener belongs to another process")
	}
	if current, err := os.Readlink(lockPath); err != nil || current != lock {
		return fmt.Errorf("browser singleton lock changed")
	}
	if current, err := os.Readlink(socketLink); err != nil || current != target {
		return fmt.Errorf("browser singleton socket changed")
	}
	return nil
}
