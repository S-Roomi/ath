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

// The shortcuts use RegisterHotKey: Windows posts WM_HOTKEY to our thread when
// a combination is pressed. It is not a keyboard hook and sees no other keys,
// so it is safe alongside kernel anti-cheat.

var (
	pRegisterHotKey   = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey = user32.NewProc("UnregisterHotKey")
)

const (
	wmHotkey    = 0x0312
	wmHotkeySet = wmApp + 2 // thread message: register pendingHotkeys instead

	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000 // holding the keys down does not toggle over and over

	hotkeyPauseID = 1
	hotkeyHideID  = 2
)

// hotkeys are the shortcut strings from the config, in hotkeySlots order:
// the "hotkey" key (Pause) and the "hide_hotkey" key (Keep taskbar hidden).
type hotkeys [2]string

var (
	pendingHotkeys    atomic.Pointer[hotkeys] // shortcuts the thread should switch to on wmHotkeySet
	activePauseHotkey atomic.Pointer[string]  // display name of the registered Pause shortcut, nil if none; shown in the tray menu
	activeHideHotkey  atomic.Pointer[string]  // same for Keep taskbar hidden
)

// hotkeySlot is one shortcut ATH can register.
type hotkeySlot struct {
	id     uintptr
	key    string // config key, for the log
	does   string // for the log
	active *atomic.Pointer[string]
	action func()
}

var hotkeySlots = [len(hotkeys{})]hotkeySlot{
	{hotkeyPauseID, "hotkey", "toggles Pause", &activePauseHotkey, func() { togglePause("hotkey") }},
	{hotkeyHideID, "hide_hotkey", "toggles Keep taskbar hidden", &activeHideHotkey, func() { toggleForceHide("hide_hotkey") }},
}

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

// startHotkeys registers the shortcuts on their own OS-locked thread, whose
// message queue receives WM_HOTKEY. set switches to other shortcuts ("" turns
// one off); stop unregisters them and ends the thread.
func startHotkeys(initial hotkeys) (set func(hotkeys), stop func()) {
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

		var registered [len(hotkeySlots)]bool
		unregisterAll := func() {
			for i, sl := range hotkeySlots {
				if registered[i] {
					pUnregisterHotKey.Call(0, sl.id)
					registered[i] = false
				}
				sl.active.Store(nil)
			}
		}
		registerAll := func(keys hotkeys) {
			unregisterAll()
			var specs [len(hotkeySlots)]hotkeySpec
			for i, sl := range hotkeySlots {
				h, err := parseHotkey(keys[i])
				switch {
				case err != nil:
					log.Printf("%s: %v; no shortcut", sl.key, err)
					continue
				case h.vk == 0:
					log.Printf("%s: off", sl.key)
					continue
				}
				dup := false
				for j := 0; j < i; j++ {
					if specs[j].vk == h.vk && specs[j].mods == h.mods {
						log.Printf("%s: %s is already used by %s; not registered", sl.key, h.name, hotkeySlots[j].key)
						dup = true
					}
				}
				if dup {
					continue
				}
				// hwnd 0: WM_HOTKEY is posted to this thread's queue.
				if r, _, err := pRegisterHotKey.Call(0, sl.id, uintptr(h.mods|modNoRepeat), uintptr(h.vk)); r == 0 {
					log.Printf("%s: could not register %s, probably in use by another app: %v", sl.key, h.name, err)
					continue
				}
				specs[i], registered[i] = h, true
				name := h.name
				sl.active.Store(&name)
				log.Printf("%s: %s %s", sl.key, h.name, sl.does)
			}
		}
		registerAll(initial)
		tidCh <- uint32(tid)

		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
				break
			}
			switch m.Message {
			case wmHotkey:
				for _, sl := range hotkeySlots {
					if m.WParam == sl.id {
						sl.action()
					}
				}
			case wmHotkeySet:
				// Swap so several quick changes apply once, with the latest value.
				if p := pendingHotkeys.Swap(nil); p != nil {
					registerAll(*p)
				}
			}
		}
		unregisterAll()
	}()
	tid := <-tidCh
	set = func(keys hotkeys) {
		pendingHotkeys.Store(&keys)
		pPostThreadMessageW.Call(uintptr(tid), wmHotkeySet, 0, 0)
	}
	stop = func() {
		pPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
		<-done
	}
	return set, stop
}
