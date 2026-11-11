//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	mutexName      = `Local\ATH.Mutex`
	stopEventName  = `Local\ATH.Stop`
	markerFileName = "hidden.marker"
	logFileName    = "ath.log"
	runSubKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyPath     = `HKCU\` + runSubKey
	runValueName   = "ATH"
	reassertLogGap = 10 * time.Second // at most one "came back" log line per gap
)

var exeDir string

func markerPath() string { return filepath.Join(exeDir, markerFileName) }

// hideTaskbars hides the taskbars on mon (0 = all) and writes the marker
// file, so a crash while hidden can be undone on the next start.
func hideTaskbars(mon uintptr) {
	setVisible(taskbarsOn(mon), false)
	_ = os.WriteFile(markerPath(), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

func showTaskbars(mon uintptr) {
	setVisible(taskbarsOn(mon), true)
	_ = os.Remove(markerPath())
}

// ---------------------------------------------------------------- main loop

// target is a taskbar state: whether to hide, and on which monitor
// (mon is 0 for every taskbar, which is always the case unless per_monitor is on).
type target struct {
	on  bool
	mon uintptr
}

func run(cfgPath string) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Printf("config problem, using defaults: %v", err)
	}
	cfgMod := modTime(cfgPath)

	stopEv, _, _ := pCreateEventW.Call(0, 1, 0, uintptr(unsafe.Pointer(utf16(stopEventName))))

	// Ctrl+C / SIGTERM (when run from a console) trigger the same clean exit as --quit.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; pSetEvent.Call(stopEv) }()

	// Crash recovery: if a previous run died while the taskbar was hidden, restore it.
	if _, err := os.Stat(markerPath()); err == nil {
		log.Println("found stale marker from a previous run, restoring taskbar")
		showTaskbars(0)
	}

	var applied, pending target // state in effect / state waiting to be applied
	weHid := false              // true only if THIS process hid the taskbar
	var pendingSince, lastReassertLog time.Time
	var pendingDelay time.Duration // delay the pending state must hold before it is applied
	var lastLogKey string
	lastCfgCheck := time.Now()

	// Wake the loop the moment the foreground window changes, instead of
	// waiting for the next poll. Polling stays on as a fallback for changes
	// that send no event (e.g. a window resizing into fullscreen).
	wakeEv, _, _ := pCreateEventW.Call(0, 0, 0, 0) // auto-reset
	stopWatch, hooked := startForegroundWatcher(wakeEv)
	defer stopWatch()
	if !hooked {
		log.Println("could not install the foreground hook, polling only")
	}

	// Keyboard shortcut that toggles Pause (RegisterHotKey, not a keyboard hook).
	setHotkey, stopHotkey := startHotkey(cfg.Hotkey)
	defer stopHotkey()

	if cfg.TrayIcon {
		if stopTray, ok := startTray(stopEv); ok {
			defer stopTray()
		} else {
			log.Println("could not create the tray icon, running without it")
		}
	}

	log.Printf("ATH %s watching (poll %dms, enter %dms, leave %dms, per_monitor %v, reassert %v)",
		versionString(), cfg.PollMs, cfg.EnterDelayMs, cfg.LeaveDelayMs, cfg.PerMonitor, cfg.Reassert)

	for {
		// Wait one poll interval, or less if a pending state is due sooner, so
		// delays are not rounded up to the poll interval. Wake early on stop or
		// on a foreground change.
		timeout := cfg.PollMs
		if pending != applied {
			left := pendingDelay - time.Since(pendingSince)
			timeout = max(0, min(timeout, int((left+time.Millisecond-1)/time.Millisecond)))
		}
		if waitStopOrWake(stopEv, wakeEv, timeout) {
			break
		}

		// Hot-reload config. The tray sets reloadConfig after editing the file,
		// so that change applies now instead of after the once-a-second check.
		if forced := reloadConfig.Swap(false); forced || time.Since(lastCfgCheck) > time.Second {
			lastCfgCheck = time.Now()
			if mt := modTime(cfgPath); forced || !mt.Equal(cfgMod) {
				cfgMod = mt
				if nc, err := loadConfig(cfgPath); err == nil {
					if nc.Hotkey != cfg.Hotkey {
						setHotkey(nc.Hotkey)
					}
					cfg = nc
					log.Println("config reloaded")
				} else {
					log.Printf("config reload failed, keeping old settings: %v", err)
				}
			}
		}

		fg := detectForeground(&cfg)
		// Log each foreground change (not rect changes, to keep dragging quiet).
		if key := fmt.Sprintf("%x|%v|%v|%s", fg.hwnd, fg.on, fg.shellUI, fg.note); fg.hwnd != 0 && key != lastLogKey {
			lastLogKey = key
			log.Printf("foreground: %s", fg)
		}
		// Remember the last fullscreen app for "Never hide for ..." in the tray
		// menu. Opening the menu makes ATH the foreground window, so the tray
		// cannot look this up itself at that point.
		if fg.on {
			if last := lastFullscreen.Load(); last == nil || *last != fg.proc {
				proc := fg.proc
				lastFullscreen.Store(&proc)
			}
		}

		isPaused := paused.Load()
		want := target{on: fg.on && !isPaused}
		if want.on && cfg.PerMonitor {
			want.mon = fg.mon
		}

		if want == applied {
			pending = applied
			// Explorer shows the taskbar again when it restarts and sometimes on
			// its own (e.g. opening Start). Hide it again while we own it.
			if applied.on && weHid && cfg.Reassert && anyVisible(taskbarsOn(applied.mon)) {
				hideTaskbars(applied.mon)
				if time.Since(lastReassertLog) > reassertLogGap {
					lastReassertLog = time.Now()
					log.Println("taskbar came back, hid it again")
				}
			}
			continue
		}
		if want != pending {
			pending = want
			pendingSince = time.Now()
		}
		switch {
		case want.on:
			pendingDelay = time.Duration(cfg.EnterDelayMs) * time.Millisecond
		case fg.shellUI || isPaused:
			pendingDelay = 0 // Start, Search, a flyout or Pause: show the taskbar now
		default:
			pendingDelay = time.Duration(cfg.LeaveDelayMs) * time.Millisecond
		}
		if time.Since(pendingSince) < pendingDelay {
			continue
		}

		// State has been stable long enough: apply it. Undo our previous hide
		// first (this also covers moving to another monitor in per_monitor mode).
		if applied.on && weHid {
			showTaskbars(applied.mon)
			weHid = false
			log.Println("taskbar restored")
		}
		applied = want
		if want.on {
			ts := taskbarsOn(want.mon)
			if len(ts) > 0 && !anyVisible(ts) {
				log.Println("taskbar already hidden by something else, leaving it alone")
			} else {
				// If no taskbar exists yet (Explorer not running), still take
				// ownership so reassert hides it once Explorer comes up.
				hideTaskbars(want.mon)
				weHid = true
				log.Printf("taskbar hidden for %s (class %q)", fg.proc, fg.class)
			}
		}
	}

	// Clean exit: never leave the taskbar hidden behind us.
	switch {
	case restoreOnExit.Load():
		// "Stop and restore taskbar" from the tray: same as --restore.
		log.Println("exiting, forcing every taskbar to show")
		showTaskbars(0)
	case weHid:
		log.Println("exiting, restoring taskbar")
		showTaskbars(applied.mon)
	}
	log.Println("stopped")
}

// ---------------------------------------------------------------- commands

func singleInstance() bool {
	h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16(mutexName))))
	if h == 0 {
		return true // could not create; do not block startup
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errorAlreadyExists {
		return false
	}
	return true // handle intentionally kept open for the life of the process
}

func sendStop() {
	h, _, _ := pOpenEventW.Call(eventModifyState, 0, uintptr(unsafe.Pointer(utf16(stopEventName))))
	if h == 0 {
		log.Println("quit: no running instance found")
		return
	}
	pSetEvent.Call(h)
	pCloseHandle.Call(h)
	log.Println("quit: stop signal sent")
}

func setAutostart(enable bool) {
	var cmd *exec.Cmd
	if enable {
		exe, _ := os.Executable()
		cmd = exec.Command("reg", "add", runKeyPath, "/v", runValueName, "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f")
	} else {
		cmd = exec.Command("reg", "delete", runKeyPath, "/v", runValueName, "/f")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("autostart change failed: %v %s", err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("autostart enabled=%v", enable)
}

func openLog() {
	path := filepath.Join(exeDir, logFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 1<<20 {
		_ = os.Remove(path) // simple size cap: start fresh past 1 MB
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
	log.SetFlags(log.Ldate | log.Ltime)
}

func main() {
	exe, _ := os.Executable()
	exeDir = filepath.Dir(exe)
	openLog()
	pSetProcessDPIAware.Call() // so window and monitor rects use the same coordinates

	cfgPath := filepath.Join(exeDir, "config.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		writeDefaultConfig(cfgPath)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--quit", "--stop": // --stop is the old name, kept so existing shortcuts still work
			sendStop()
		case "--restore":
			// A running watcher would hide the taskbar again, so stop it first.
			sendStop()
			showTaskbars(0)
			log.Println("--restore: taskbar shown")
		case "--install-autostart":
			setAutostart(true)
		case "--remove-autostart":
			setAutostart(false)
		case "--version":
			showVersion()
		default:
			log.Printf("unknown option %q (use --quit, --restore, --install-autostart, --remove-autostart, --version)", os.Args[1])
		}
		return
	}

	if !singleInstance() {
		if notifyRunningInstance() {
			log.Println("another instance is already running, asked it to show a notification, exiting")
		} else {
			log.Println("another instance is already running (without a tray icon), exiting")
		}
		return
	}
	run(cfgPath)
}
