//go:build windows

package main

import "unsafe"

const (
	trayClass          = "Shell_TrayWnd"          // taskbar on the primary monitor
	secondaryTrayClass = "Shell_SecondaryTrayWnd" // one per additional monitor
)

func findWindowEx(after uintptr, class string) uintptr {
	cls := utf16(class)
	h, _, _ := pFindWindowExW.Call(0, after, uintptr(unsafe.Pointer(cls)), 0)
	return h
}

// taskbarWindows looks up every taskbar window. It is called fresh each time
// because Explorer creates new windows (new handles) when it restarts.
func taskbarWindows() []uintptr {
	var list []uintptr
	if h := findWindowEx(0, trayClass); h != 0 {
		list = append(list, h)
	}
	for after := uintptr(0); ; {
		h := findWindowEx(after, secondaryTrayClass)
		if h == 0 {
			break
		}
		list = append(list, h)
		after = h
	}
	return list
}

// taskbarsOn returns the taskbars on monitor mon, or every taskbar when mon is 0.
func taskbarsOn(mon uintptr) []uintptr {
	all := taskbarWindows()
	if mon == 0 {
		return all
	}
	var list []uintptr
	for _, h := range all {
		if monitorOf(h) == mon {
			list = append(list, h)
		}
	}
	return list
}

func anyVisible(hs []uintptr) bool {
	for _, h := range hs {
		if v, _, _ := pIsWindowVisible.Call(h); v != 0 {
			return true
		}
	}
	return false
}

func setVisible(hs []uintptr, visible bool) {
	cmd := uintptr(swHide)
	if visible {
		cmd = swShowNA
	}
	for _, h := range hs {
		pShowWindow.Call(h, cmd)
	}
}
