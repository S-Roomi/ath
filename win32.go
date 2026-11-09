//go:build windows

package main

import (
	"syscall"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	pGetWindowRect              = user32.NewProc("GetWindowRect")
	pMonitorFromWindow          = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW            = user32.NewProc("GetMonitorInfoW")
	pGetClassNameW              = user32.NewProc("GetClassNameW")
	pGetWindowThreadProcessID   = user32.NewProc("GetWindowThreadProcessId")
	pIsWindowVisible            = user32.NewProc("IsWindowVisible")
	pIsIconic                   = user32.NewProc("IsIconic")
	pIsZoomed                   = user32.NewProc("IsZoomed")
	pFindWindowExW              = user32.NewProc("FindWindowExW")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pGetShellWindow             = user32.NewProc("GetShellWindow")
	pGetDesktopWindow           = user32.NewProc("GetDesktopWindow")
	pSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	pOpenProcess                = kernel32.NewProc("OpenProcess")
	pCloseHandle                = kernel32.NewProc("CloseHandle")
	pQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	pCreateMutexW               = kernel32.NewProc("CreateMutexW")
	pCreateEventW               = kernel32.NewProc("CreateEventW")
	pOpenEventW                 = kernel32.NewProc("OpenEventW")
	pSetEvent                   = kernel32.NewProc("SetEvent")
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

const (
	monitorDefaultToNearest = 2
	processQueryLimitedInfo = 0x1000
	eventModifyState        = 0x0002
	errorAlreadyExists      = 183
	createNoWindow          = 0x08000000
	waitObject0             = 0
	swHide                  = 0
	swShowNA                = 8 // show without activating, so the taskbar never steals focus
)

// utf16 converts s for a Win32 call. Convert the result with
// uintptr(unsafe.Pointer(p)) inside the Call(...) argument list itself, so the
// buffer is kept alive until the call returns.
func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func monitorOf(h uintptr) uintptr {
	m, _, _ := pMonitorFromWindow.Call(h, monitorDefaultToNearest)
	return m
}
