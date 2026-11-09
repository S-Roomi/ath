//go:build windows

package main

import (
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"
)

// The shortcut uses RegisterHotKey: Windows posts WM_HOTKEY to our thread when
// the combination is pressed. It is not a keyboard hook and sees no other keys,
// so it is safe alongside kernel anti-cheat.

var (
	pRegisterHotKey   = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey = user32.NewProc("UnregisterHotKey")
)

const (
	wmHotkey    = 0x0312
	wmHotkeySet = wmApp + 2 // thread message: register pendingHotkey instead

	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000 // holding the keys down does not toggle over and over

	hotkeyID = 1
)

var (
	pendingHotkey atomic.Pointer[string] // hotkey the thread should switch to on wmHotkeySet
	activeHotkey  atomic.Pointer[string] // display name of the registered hotkey, nil if none; shown in the tray menu
)

type hotkeySpec struct {
	mods, vk uint32
	name     string // e.g. "Ctrl+Alt+H"
}

// parseHotkey reads a shortcut such as "ctrl+alt+h", "Win+Shift+P" or
// "ctrl+f12". Modifiers: ctrl (or control), alt, shift, win. Key: a letter, a
// digit or F1-F24. Letters and digits need at least one modifier, so a plain
// key is never taken away from other apps. An empty string means no hotkey
// and returns a zero spec.
func parseHotkey(s string) (hotkeySpec, error) {
	var h hotkeySpec
	s = strings.TrimSpace(s)
	if s == "" {
		return h, nil
	}
	var names []string
	key := ""
	for _, part := range strings.Split(strings.ToLower(s), "+") {
		part = strings.TrimSpace(part)
		mod, name := uint32(0), ""
		switch part {
		case "ctrl", "control":
			mod, name = modControl, "Ctrl"
		case "alt":
			mod, name = modAlt, "Alt"
		case "shift":
			mod, name = modShift, "Shift"
		case "win":
			mod, name = modWin, "Win"
		case "":
			return hotkeySpec{}, fmt.Errorf("empty part (extra \"+\") in %q", s)
		default:
			if key != "" {
				return hotkeySpec{}, fmt.Errorf("more than one key in %q", s)
			}
			key = part
			continue
		}
		if h.mods&mod != 0 {
			return hotkeySpec{}, fmt.Errorf("%s twice in %q", name, s)
		}
		h.mods |= mod
		names = append(names, name)
	}

	switch {
	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		h.vk = uint32(key[0]-'a') + 'A'
	case len(key) == 1 && key[0] >= '0' && key[0] <= '9':
		h.vk = uint32(key[0])
	case len(key) >= 2 && key[0] == 'f':
		n, err := strconv.Atoi(key[1:])
		if err != nil || n < 1 || n > 24 {
			return hotkeySpec{}, fmt.Errorf("unknown key %q in %q", key, s)
		}
		h.vk = 0x70 + uint32(n-1) // VK_F1..VK_F24
	case key == "":
		return hotkeySpec{}, fmt.Errorf("no key in %q", s)
	default:
		return hotkeySpec{}, fmt.Errorf("unknown key %q in %q (use a letter, a digit or F1-F24)", key, s)
	}
	if h.mods == 0 && (h.vk < 0x70 || h.vk > 0x87) {
		return hotkeySpec{}, fmt.Errorf("%q needs ctrl, alt, shift or win", s)
	}
	h.name = strings.Join(append(names, strings.ToUpper(key)), "+")
	return h, nil
}

// startHotkey registers the shortcut on its own OS-locked thread, whose
// message queue receives WM_HOTKEY. set switches to another shortcut ("" turns
// it off); stop unregisters it and ends the thread.
func startHotkey(initial string) (set func(string), stop func()) {
	tidCh := make(chan uint32, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)

		// Create this thread's message queue before anyone posts to it.
		var m msg
		pPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmNoRemove)
		tid, _, _ := pGetCurrentThreadId.Call()

		registered := false
		register := func(s string) {
			if registered {
				pUnregisterHotKey.Call(0, hotkeyID)
				registered = false
				activeHotkey.Store(nil)
			}
			h, err := parseHotkey(s)
			switch {
			case err != nil:
				log.Printf("hotkey: %v; no hotkey", err)
				return
			case h.vk == 0:
				log.Println("hotkey: off")
				return
			}
			// hwnd 0: WM_HOTKEY is posted to this thread's queue.
			if r, _, err := pRegisterHotKey.Call(0, hotkeyID, uintptr(h.mods|modNoRepeat), uintptr(h.vk)); r == 0 {
				log.Printf("hotkey: could not register %s, probably in use by another app: %v", h.name, err)
				return
			}
			registered = true
			name := h.name
			activeHotkey.Store(&name)
			log.Printf("hotkey: %s toggles Pause", h.name)
		}
		register(initial)
		tidCh <- uint32(tid)

		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
				break
			}
			switch m.Message {
			case wmHotkey:
				if m.WParam == hotkeyID {
					togglePause("hotkey")
				}
			case wmHotkeySet:
				// Swap so several quick changes apply once, with the latest value.
				if p := pendingHotkey.Swap(nil); p != nil {
					register(*p)
				}
			}
		}
		if registered {
			pUnregisterHotKey.Call(0, hotkeyID)
		}
	}()
	tid := <-tidCh
	set = func(s string) {
		pendingHotkey.Store(&s)
		pPostThreadMessageW.Call(uintptr(tid), wmHotkeySet, 0, 0)
	}
	stop = func() {
		pPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
		<-done
	}
	return set, stop
}
