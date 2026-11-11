//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"unsafe"
)

// version is set at build time:
//
//	go build -ldflags "-H=windowsgui -s -w -X main.version=v1.1.0" -o bin/ath.exe
//
// A build without -X reports "dev" plus the git commit it was built from.
var version = "dev"

var (
	pAttachConsole = kernel32.NewProc("AttachConsole")
	pMessageBoxW   = user32.NewProc("MessageBoxW")
)

const (
	attachParentProcess = 0xFFFFFFFF // (DWORD)-1: the console of the process that started us
	mbIconInformation   = 0x40
)

// versionString returns the version, e.g. "v1.1.0". A dev build adds the git
// commit Go recorded at build time, e.g. "dev (5023b7d)" or "dev (5023b7d-dirty)"
// when there were uncommitted changes.
func versionString() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return version + " (" + rev + ")"
}

// showVersion handles --version. The exe is a GUI program with no console of
// its own, so it borrows the console it was started from (cmd, PowerShell).
// Started from Explorer or a shortcut there is none, so it shows a message box.
func showVersion() {
	text := "ATH " + versionString()
	log.Printf("--version: %s", text)
	if r, _, _ := pAttachConsole.Call(attachParentProcess); r != 0 {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			fmt.Fprintln(f, text)
			f.Close()
			return
		}
	}
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("ATH"))), mbIconInformation)
}
