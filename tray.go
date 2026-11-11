//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	gdi32   = syscall.NewLazyDLL("gdi32.dll")

	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW          = shell32.NewProc("ShellExecuteW")
	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pDestroyWindow          = user32.NewProc("DestroyWindow")
	pPostQuitMessage        = user32.NewProc("PostQuitMessage")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	pDestroyIcon            = user32.NewProc("DestroyIcon")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap           = gdi32.NewProc("CreateBitmap")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmNull       = 0x0000
	wmDestroy    = 0x0002
	wmClose      = 0x0010
	wmLButtonUp  = 0x0202
	wmRButtonUp  = 0x0205
	wmApp        = 0x8000
	wmTrayNotify = wmApp + 1
	// Posted to the tray window by a second ATH launch (another process), so
	// it must not carry pointers.
	wmShowRunning = wmApp + 3

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10
	niifInfo   = 0x1 // "information" icon in the balloon

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	smCxSmIcon   = 49
	smCySmIcon   = 50
	swShowNormal = 1

	trayIconID = 1
)

// Menu command IDs.
const (
	cmdAutostart = iota + 1
	cmdOpenConfig
	cmdOpenLog
	cmdQuit
	cmdRestore
	cmdPause
	cmdIgnoreLast
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

// Tray state. Only the tray thread touches these, except restoreOnExit.
var (
	trayHwnd          uintptr
	trayIcon          uintptr
	trayStopEv        uintptr
	taskbarCreatedMsg uintptr
	restoreOnExit     atomic.Bool            // set by the "Quit and Restore taskbar" menu item
	paused            atomic.Bool            // set by the "Pause" menu item; read by the main loop
	reloadConfig      atomic.Bool            // set after the tray edits config.json; the main loop reloads at once
	lastFullscreen    atomic.Pointer[string] // process name of the last fullscreen app; written by the main loop
	trayClassName     = "ATH.TrayWindow"
)

// trayWndProc must be created exactly once (syscall.NewCallback slots are never freed).
var trayWndProc = syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayNotify:
		if lParam == wmRButtonUp || lParam == wmLButtonUp {
			showTrayMenu(hwnd)
		}
		return 0
	case wmShowRunning:
		showRunningBalloon()
		return 0
	case wmClose:
		removeTrayIcon()
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	// Explorer restarted: the old icon is gone, add it again.
	if taskbarCreatedMsg != 0 && msg == taskbarCreatedMsg {
		addTrayIcon()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
})

// startTray creates the notification-area icon on its own OS-locked thread
// with a message loop. Choosing Quit sets stopEv. It returns a func that
// removes the icon and ends the thread, and ok=false if the icon could not
// be created (the watcher keeps running without it).
func startTray(stopEv uintptr) (stop func(), ok bool) {
	trayStopEv = stopEv
	ready := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)

		hInst, _, _ := pGetModuleHandleW.Call(0)
		wc := wndClassEx{
			LpfnWndProc:   trayWndProc,
			HInstance:     hInst,
			LpszClassName: utf16(trayClassName),
		}
		wc.CbSize = uint32(unsafe.Sizeof(wc))
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		// A plain hidden top-level window (not message-only), because the popup
		// menu needs a window that can become the foreground window.
		hwnd, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(utf16(trayClassName))),
			uintptr(unsafe.Pointer(utf16("ATH"))),
			0, 0, 0, 0, 0, 0, 0, hInst, 0)
		if hwnd == 0 {
			ready <- false
			return
		}
		trayHwnd = hwnd
		taskbarCreatedMsg, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
		trayIcon = makeTrayIcon()
		if !addTrayIcon() {
			// Explorer may not be up yet; TaskbarCreated will add it later.
			log.Println("tray: could not add icon yet, will retry when the taskbar appears")
		}
		ready <- true

		var m msg
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if trayIcon != 0 {
			pDestroyIcon.Call(trayIcon)
		}
	}()
	if !<-ready {
		<-done
		return func() {}, false
	}
	return func() {
		pPostMessageW.Call(trayHwnd, wmClose, 0, 0)
		<-done
	}, true
}

func trayData() notifyIconData {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = trayHwnd
	nid.UID = trayIconID
	return nid
}

func addTrayIcon() bool {
	nid := trayData()
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTrayNotify
	nid.HIcon = trayIcon
	tip, _ := syscall.UTF16FromString("ATH - auto-hide taskbar")
	copy(nid.SzTip[:len(nid.SzTip)-1], tip)
	r, _, _ := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		// Already present (e.g. TaskbarCreated sent without a real restart): update it.
		r, _, _ = pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	}
	return r != 0
}

// showRunningBalloon shows an "ATH is already running" notification from the
// tray icon. It runs when ATH is launched a second time.
func showRunningBalloon() {
	nid := trayData()
	nid.UFlags = nifInfo
	nid.DwInfoFlags = niifInfo
	text := "Right-click the ATH icon in the notification area for options."
	if paused.Load() {
		text = "Hiding is paused. " + text
	}
	title, _ := syscall.UTF16FromString("ATH is already running")
	info, _ := syscall.UTF16FromString(text)
	copy(nid.SzInfoTitle[:len(nid.SzInfoTitle)-1], title)
	copy(nid.SzInfo[:len(nid.SzInfo)-1], info)
	if r, _, err := pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid))); r == 0 {
		log.Printf("tray: could not show the already-running notification: %v", err)
		return
	}
	log.Println("tray: launched again, showed the already-running notification")
}

// notifyRunningInstance is called by a second launch. It finds the running
// instance's tray window by class and asks it to show the notification.
// It returns false when there is no tray window (e.g. tray_icon is off).
func notifyRunningInstance() bool {
	h := findWindowEx(0, trayClassName)
	if h == 0 {
		return false
	}
	r, _, _ := pPostMessageW.Call(h, wmShowRunning, 0, 0)
	return r != 0
}

func removeTrayIcon() {
	nid := trayData()
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)

	appendItem := func(flags, id uintptr, text string) {
		pAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(utf16(text))))
	}
	appendItem(mfString|mfGrayed, 0, "ATH "+versionString())
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	pauseText := "Pause"
	if paused.Load() {
		pauseText = "Resume"
	}
	if hk := activeHotkey.Load(); hk != nil {
		pauseText += "\t" + *hk // a tab right-aligns the shortcut, like other menus
	}
	appendItem(mfString, cmdPause, pauseText)
	// Read the last fullscreen app now: the main loop recorded it before the
	// menu made ATH the foreground window.
	var lastProc string
	if p := lastFullscreen.Load(); p != nil {
		lastProc = *p
	}
	if lastProc != "" {
		// "&" marks a menu shortcut key, so double it to show it literally.
		appendItem(mfString, cmdIgnoreLast, "Never hide for "+strings.ReplaceAll(lastProc, "&", "&&"))
	} else {
		appendItem(mfString|mfGrayed, 0, "Never hide for this app")
	}
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	autoFlags := uintptr(mfString)
	if autostartEnabled() {
		autoFlags |= mfChecked
	}
	appendItem(autoFlags, cmdAutostart, "Start with Windows")
	appendItem(mfString, cmdOpenConfig, "Open config")
	appendItem(mfString, cmdOpenLog, "Open log")
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(mfString, cmdRestore, "Quit and Restore taskbar")
	appendItem(mfString, cmdQuit, "Quit")

	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Without this the menu does not close when the user clicks elsewhere.
	pSetForegroundWindow.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	pPostMessageW.Call(hwnd, wmNull, 0, 0)
	if cmd != 0 {
		handleTrayCommand(cmd, lastProc)
	}
}

// handleTrayCommand runs a menu command. lastProc is the app the "Never hide
// for ..." item was shown for.
func handleTrayCommand(cmd uintptr, lastProc string) {
	switch cmd {
	case cmdIgnoreLast:
		added, err := addToIgnore(filepath.Join(exeDir, "config.json"), lastProc)
		switch {
		case err != nil:
			log.Printf("tray: could not add %s to ignore: %v", lastProc, err)
			return
		case added:
			log.Printf("tray: added %s to ignore", lastProc)
		default:
			log.Printf("tray: %s is already in ignore", lastProc)
		}
		// The app is ignored now, so stop offering it in the menu. Then have
		// the main loop reload the config at once, which shows the taskbar
		// if this app is still in front.
		lastFullscreen.Store(nil)
		reloadConfig.Store(true)
		pSetEvent.Call(wakeEv)
	case cmdPause:
		togglePause("tray")
	case cmdAutostart:
		setAutostart(!autostartEnabled())
	case cmdOpenConfig:
		openFile(filepath.Join(exeDir, "config.json"))
	case cmdOpenLog:
		openFile(filepath.Join(exeDir, logFileName))
	case cmdRestore:
		log.Println("tray: quit and restore taskbar")
		restoreOnExit.Store(true)
		pSetEvent.Call(trayStopEv)
	case cmdQuit:
		log.Println("tray: quit")
		pSetEvent.Call(trayStopEv)
	}
}

// togglePause flips Pause/Resume. It is called from the tray menu and from
// the hotkey thread; source is only for the log.
func togglePause(source string) {
	// Swap via CompareAndSwap so two sources at once cannot both flip to the same value.
	for {
		old := paused.Load()
		if paused.CompareAndSwap(old, !old) {
			if old {
				log.Printf("%s: resumed", source)
			} else {
				log.Printf("%s: paused", source)
			}
			break
		}
	}
	// Wake the main loop so the change applies now, not at the next poll.
	pSetEvent.Call(wakeEv)
}

// openFile opens path with its associated app, falling back to Notepad when
// nothing is associated (common for .json and .log).
func openFile(path string) {
	r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16("open"))),
		uintptr(unsafe.Pointer(utf16(path))), 0, 0, swShowNormal)
	if r > 32 {
		return
	}
	r, _, _ = pShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16("open"))),
		uintptr(unsafe.Pointer(utf16("notepad.exe"))),
		uintptr(unsafe.Pointer(utf16(`"`+path+`"`))), 0, swShowNormal)
	if r <= 32 {
		log.Printf("tray: could not open %s (code %d)", path, r)
	}
}

// autostartEnabled reports whether the Run key starts THIS exe. An entry that
// points somewhere else (e.g. the exe was moved) counts as off, so ticking the
// item rewrites it to the current path.
func autostartEnabled() bool {
	var k syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, utf16(runSubKey), 0, syscall.KEY_QUERY_VALUE, &k) != nil {
		return false
	}
	defer syscall.RegCloseKey(k)
	var buf [1024]uint16
	n := uint32(len(buf) * 2)
	var typ uint32
	if syscall.RegQueryValueEx(k, utf16(runValueName), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return false
	}
	val := strings.Trim(syscall.UTF16ToString(buf[:]), `"`)
	exe, _ := os.Executable()
	return strings.EqualFold(val, exe)
}

// makeTrayIcon draws a small monitor with a taskbar strip, since the exe has
// no icon resource. Returns 0 on failure (Windows then shows a blank icon).
func makeTrayIcon() uintptr {
	w, _, _ := pGetSystemMetrics.Call(smCxSmIcon)
	h, _, _ := pGetSystemMetrics.Call(smCySmIcon)
	if w == 0 || h == 0 {
		w, h = 16, 16
	}
	bi := bitmapInfoHeader{BiWidth: int32(w), BiHeight: -int32(h), BiPlanes: 1, BiBitCount: 32}
	bi.BiSize = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	color, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0
	}
	defer pDeleteObject.Call(color)
	px := unsafe.Slice((*uint32)(bits), int(w*h))
	drawIcon(px, int(w), int(h))

	mask := make([]byte, ((int(w)+15)/16)*2*int(h)) // all zero: alpha decides
	hMask, _, _ := pCreateBitmap.Call(w, h, 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	if hMask == 0 {
		return 0
	}
	defer pDeleteObject.Call(hMask)

	ii := iconInfo{FIcon: 1, HbmMask: hMask, HbmColor: color}
	icon, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return icon
}

// drawIcon renders the icon into px (BGRA, top-down) with 4x4 supersampling.
func drawIcon(px []uint32, w, h int) {
	type shape struct {
		x0, y0, x1, y1, r float64
		c                 [3]float64 // RGB 0..255
	}
	shapes := []shape{ // later shapes draw on top; coordinates are 0..1
		{0.40, 0.76, 0.60, 0.90, 0, [3]float64{96, 110, 135}},     // stand
		{0.24, 0.86, 0.76, 0.95, 0.04, [3]float64{96, 110, 135}},  // base
		{0.04, 0.08, 0.96, 0.80, 0.12, [3]float64{37, 99, 235}},   // screen
		{0.14, 0.62, 0.86, 0.71, 0.03, [3]float64{219, 234, 254}}, // taskbar strip
	}
	inside := func(s shape, x, y float64) bool {
		if x < s.x0 || x > s.x1 || y < s.y0 || y > s.y1 {
			return false
		}
		cx := min(max(x, s.x0+s.r), s.x1-s.r)
		cy := min(max(y, s.y0+s.r), s.y1-s.r)
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= s.r*s.r
	}
	const ss = 4
	for py := 0; py < h; py++ {
		for pxi := 0; pxi < w; pxi++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(pxi) + (float64(sx)+0.5)/ss) / float64(w)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(h)
					for i := len(shapes) - 1; i >= 0; i-- {
						if inside(shapes[i], x, y) {
							c := shapes[i].c
							r, g, b, a = r+c[0], g+c[1], b+c[2], a+1
							break
						}
					}
				}
			}
			if a == 0 {
				px[py*w+pxi] = 0
				continue
			}
			// Straight (non-premultiplied) alpha, which is what icons use.
			alpha := uint32(a / (ss * ss) * 255)
			px[py*w+pxi] = alpha<<24 | uint32(r/a)<<16 | uint32(g/a)<<8 | uint32(b/a)
		}
	}
}
