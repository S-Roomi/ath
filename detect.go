//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Window classes that are part of the shell and must never count as "fullscreen".
var shellClasses = map[string]bool{
	"Progman":                      true,
	"WorkerW":                      true,
	trayClass:                      true,
	secondaryTrayClass:             true,
	"XamlExplorerHostIslandWindow": true,
}

func foregroundWindow() uintptr {
	h, _, _ := pGetForegroundWindow.Call()
	return h
}

func className(h uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

func processName(h uintptr) string {
	var pid uint32
	pGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	ph, _, _ := pOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	if ph == 0 {
		return ""
	}
	defer pCloseHandle.Call(ph)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, _ := pQueryFullProcessImageNameW.Call(ph, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// shellUIProcs are shell flyouts that take the foreground when the user presses
// Win, opens Search, the notification/action center or quick settings. When one
// of these comes to the front the taskbar is shown at once, with no leave delay.
var shellUIProcs = map[string]bool{
	"startmenuexperiencehost.exe": true, // Start (Win10 1903+, Win11)
	"searchapp.exe":               true, // Search (Win10 2004+)
	"searchui.exe":                true, // Cortana/Search (Win10 before 2004)
	"searchhost.exe":              true, // Search (Win11)
	"shellexperiencehost.exe":     true, // action/notification center, quick settings, clock, network/volume flyouts
	"textinputhost.exe":           true, // touch keyboard / emoji panel
}

// shellUIClasses are Explorer-owned switcher and taskbar windows.
var shellUIClasses = map[string]bool{
	"MultitaskingViewFrame":        true, // Win10 Alt-Tab
	"XamlExplorerHostIslandWindow": true, // Win11 Alt-Tab / Task View / Snap
	"ForegroundStaging":            true, // transient during task switching
	"TaskListThumbnailWnd":         true, // taskbar thumbnail preview
	trayClass:                      true, // a taskbar that is still visible (per_monitor)
	secondaryTrayClass:             true,
}

// foreground describes the current foreground window.
type foreground struct {
	hwnd    uintptr
	proc    string
	class   string
	rect    rect
	on      bool    // covers its monitor and passes ignore/whitelist: hide the taskbar
	mon     uintptr // monitor it covers, when on
	shellUI bool    // Start, Search, a flyout or a task switcher: show the taskbar now
	note    string  // why a monitor-covering window does not count, for logging
}

func (f foreground) String() string {
	s := fmt.Sprintf("%s (class %q) rect %d,%d %dx%d", f.proc, f.class,
		f.rect.Left, f.rect.Top, f.rect.Right-f.rect.Left, f.rect.Bottom-f.rect.Top)
	switch {
	case f.shellUI:
		s += " [shell UI]"
	case f.note != "":
		s += " [" + f.note + "]"
	}
	return s + fmt.Sprintf(" fullscreen=%v", f.on)
}

// identify returns the process name and class of h. The last answer is cached,
// because the same window usually stays in front for many polls.
var identCache struct {
	h           uintptr
	proc, class string
}

func identify(h uintptr) (string, string) {
	if identCache.h != h {
		identCache.h, identCache.proc, identCache.class = h, processName(h), className(h)
	}
	return identCache.proc, identCache.class
}

// detectForeground looks at the foreground window and reports whether it
// covers its entire monitor (exclusive fullscreen and borderless-windowed games
// both look like this) and whether it is shell UI.
func detectForeground(cfg *Config) foreground {
	var f foreground
	h := foregroundWindow()
	if h == 0 {
		return f
	}
	f.hwnd = h
	f.proc, f.class = identify(h)
	rectOK, _, _ := pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&f.rect)))

	// Check shell UI before the size test: some of these windows can be as
	// large as the monitor and must never count as fullscreen.
	if shellUIClasses[f.class] || shellUIProcs[f.proc] {
		f.shellUI = true
		return f
	}
	if shell, _, _ := pGetShellWindow.Call(); h == shell {
		return f
	}
	if desk, _, _ := pGetDesktopWindow.Call(); h == desk {
		return f
	}
	if vis, _, _ := pIsWindowVisible.Call(h); vis == 0 {
		return f
	}
	if min, _, _ := pIsIconic.Call(h); min != 0 {
		return f
	}
	if shellClasses[f.class] || rectOK == 0 {
		return f
	}

	mon := monitorOf(h)
	if mon == 0 {
		return f
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return f
	}
	// Compare against the full monitor rect, not the work area: a normally
	// maximized window stops at the taskbar edge and will not match.
	r, m := f.rect, mi.RcMonitor
	if !(r.Left <= m.Left && r.Top <= m.Top && r.Right >= m.Right && r.Bottom >= m.Bottom) {
		return f
	}
	if contains(cfg.Ignore, f.proc) {
		f.note = "ignored"
		return f
	}
	if len(cfg.Whitelist) > 0 && !contains(cfg.Whitelist, f.proc) {
		f.note = "not in whitelist"
		return f
	}
	f.on, f.mon = true, mon
	return f
}
