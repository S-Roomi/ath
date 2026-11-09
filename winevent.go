//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	pSetWinEventHook        = user32.NewProc("SetWinEventHook")
	pUnhookWinEvent         = user32.NewProc("UnhookWinEvent")
	pGetMessageW            = user32.NewProc("GetMessageW")
	pPostThreadMessageW     = user32.NewProc("PostThreadMessageW")
	pPeekMessageW           = user32.NewProc("PeekMessageW")
	pGetCurrentThreadId     = kernel32.NewProc("GetCurrentThreadId")
	pWaitForMultipleObjects = kernel32.NewProc("WaitForMultipleObjects")
)

const (
	eventSystemForeground  = 0x0003
	eventSystemMinimizeEnd = 0x0017
	winEventOutOfContext   = 0x0000
	winEventSkipOwnProcess = 0x0002
	wmQuit                 = 0x0012
	pmNoRemove             = 0x0000
)

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

// wakeEv is an auto-reset event signalled by the WinEvent callback.
var wakeEv uintptr

// winEventProc must be created exactly once (syscall.NewCallback slots are
// never freed; the limit is 2000 per process).
var winEventProc = syscall.NewCallback(func(hook, event, hwnd, idObject, idChild, thread, ms uintptr) uintptr {
	// Only top-level window events (OBJID_WINDOW == 0).
	if int32(idObject) == 0 {
		pSetEvent.Call(wakeEv)
	}
	return 0
})

// startForegroundWatcher installs out-of-context WinEvent hooks on a dedicated,
// OS-locked thread with its own message loop. It returns a stop func.
// If the hook cannot be installed, it returns ok=false and the caller just polls.
func startForegroundWatcher(ev uintptr) (stop func(), ok bool) {
	wakeEv = ev
	type res struct {
		tid uint32
		ok  bool
	}
	ready := make(chan res, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)

		// Force creation of this thread's message queue before anyone posts to it.
		var m msg
		pPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmNoRemove)
		tid, _, _ := pGetCurrentThreadId.Call()

		flags := uintptr(winEventOutOfContext | winEventSkipOwnProcess)
		h1, _, _ := pSetWinEventHook.Call(eventSystemForeground, eventSystemForeground, 0, winEventProc, 0, 0, flags)
		h2, _, _ := pSetWinEventHook.Call(eventSystemMinimizeEnd, eventSystemMinimizeEnd, 0, winEventProc, 0, 0, flags)
		if h1 == 0 {
			if h2 != 0 {
				pUnhookWinEvent.Call(h2)
			}
			ready <- res{ok: false}
			return
		}
		ready <- res{tid: uint32(tid), ok: true}

		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
				break
			}
			// No windows on this thread, so no Translate/Dispatch needed:
			// WinEvent callbacks are invoked from inside GetMessageW.
		}
		pUnhookWinEvent.Call(h1)
		if h2 != 0 {
			pUnhookWinEvent.Call(h2)
		}
	}()
	r := <-ready
	if !r.ok {
		<-done
		return func() {}, false
	}
	return func() {
		pPostThreadMessageW.Call(uintptr(r.tid), wmQuit, 0, 0)
		<-done
	}, true
}

// waitStopOrWake blocks up to ms for the stop event or the wake event.
// It returns true if stop was signalled.
func waitStopOrWake(stopEv, wake uintptr, ms int) bool {
	hs := [2]uintptr{stopEv, wake}
	r, _, _ := pWaitForMultipleObjects.Call(2, uintptr(unsafe.Pointer(&hs[0])), 0, uintptr(ms))
	return r == waitObject0
}
