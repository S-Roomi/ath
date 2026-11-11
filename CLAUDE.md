# CLAUDE.md

Notes for Claude Code (and other AI coding agents) working on this repo. User-facing docs are in `README.md`.

## What this is

ATH is a Windows-only Go background process (no visible window, no console; just a notification-area icon). It hides the taskbar while a fullscreen or borderless app is in front and shows it again when the user leaves. It has no dependencies outside the standard library and talks to Win32 directly through `syscall.NewLazyDLL`.

## Layout

| File | Contents |
| --- | --- |
| `main.go` | `run()` main loop (debounce, reassert, crash marker), command-line options, logging, single instance |
| `detect.go` | `detectForeground()`: fullscreen test, shell-UI classification (`shellUIProcs`, `shellUIClasses`), ignore/whitelist |
| `taskbar.go` | Finds `Shell_TrayWnd` / `Shell_SecondaryTrayWnd`, hides with `SW_HIDE`, shows with `SW_SHOWNA` |
| `winevent.go` | Out-of-context `SetWinEventHook` (foreground, minimize-end) on a locked OS thread with its own message loop; `waitStopOrWake` |
| `tray.go` | Notification-area icon and right-click menu on its own locked OS thread with a hidden window; icon drawn at runtime (`drawIcon`); autostart state read from the registry |
| `hotkey.go` | Pause shortcut: `parseHotkey()` and `RegisterHotKey` on its own locked OS thread with a message loop (hwnd 0, so `WM_HOTKEY` goes to that thread); re-registers on config reload |
| `win32.go` | DLL/proc declarations, structs, constants, `utf16()`, `monitorOf()` |
| `config.go` | `Config`, defaults, loading, file-change time |

Every `.go` file starts with `//go:build windows`.

## Build and check

You cannot run the exe from WSL or Linux. Cross-compile and vet:

```sh
gofmt -l .                                    # must print nothing
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o ath.exe
```

Go 1.22 (`go.mod`). The user compiles and tests on Windows themselves, so hand changes back with a clear manual test list.

## How the main loop works

- The loop waits on the stop event and the WinEvent wake event, with `poll_ms` as a fallback timeout. While a state change is pending, the timeout shrinks so `enter_delay_ms` / `leave_delay_ms` apply exactly.
- Each tick computes a `target{on, mon}` (`mon` is 0 for "every taskbar", unless `per_monitor` is on). A new target must hold for its delay before it is applied. Shell UI (Start, Search, flyouts, Alt-Tab) uses a delay of 0.
- `weHid` tracks whether this process hid the taskbar. Never show a taskbar this process didn't hide, and never hide one that something else already hid.
- `reassert` re-hides the taskbar while the applied state is unchanged and Explorer has shown it again (e.g. after Explorer restarts).
- `hidden.marker` next to the exe records "taskbar hidden". On startup, a leftover marker means a previous run died while hidden, so all taskbars are shown.
- `--quit` (old name `--stop`, still accepted) sets a named event. `--restore` stops the running instance, then shows all taskbars.
- A second launch (mutex already taken) finds the running tray window by class (`ATH.TrayWindow`) and posts it `wmShowRunning`; the tray thread shows a balloon (`NIF_INFO`). The message crosses processes, so it carries no pointers. Without a tray window it only logs and exits.
- The tray menu's Quit sets the same stop event. "Quit and Restore taskbar" also sets `restoreOnExit`, so `run()` shows every taskbar on the way out.
- The tray menu's Pause/Resume and the `hotkey` shortcut both call `togglePause()`, which flips the `paused` atomic and sets the wake event. The hotkey uses `RegisterHotKey` (allowed with anti-cheat; it is not a keyboard hook). While paused, the loop's target is "off" with a delay of 0, so a taskbar ATH hid comes back at once and nothing new is hidden.
- "Never hide for <app>": the main loop stores the last fullscreen process in `lastFullscreen`, because opening the menu makes ATH the foreground window. `addToIgnore` edits only the `ignore` key (unknown keys kept, unparseable file left alone) via temp file + rename, then the tray sets `reloadConfig` and the wake event so the loop reloads at once.

## Rules

- **Win32 strings:** convert with `uintptr(unsafe.Pointer(utf16(s)))` inside the `.Call(...)` argument list itself, so the buffer stays alive for the call. Don't store the converted `uintptr` in a variable first.
- **Callbacks:** `syscall.NewCallback` slots are never freed (limit 2000). Create callbacks once at package level.
- **Anti-cheat:** the user plays games with kernel anti-cheat (Riot Vanguard). Don't add keyboard or mouse hooks (`WH_KEYBOARD_LL`, `WH_MOUSE_LL`), in-context WinEvent hooks, DLL injection, or anything that reads or writes other processes' memory.
- **Detection:** match windows by process name or class, never by title (titles are localized). A maximized window whose frame hangs past the monitor edges is not fullscreen (with taskbar auto-hide on, maximized windows cover the monitor); real fullscreen windows fit the monitor exactly.
- **Expensive events:** don't hook `EVENT_OBJECT_LOCATIONCHANGE` or other high-volume events system-wide. They fire on every mouse move.
- **No thide:** the tool used to call thide. It no longer does; don't reintroduce it or any other external tool.
- **Data files:** `config.json`, `ath.log` and `hidden.marker` always live next to the exe. Don't add a fallback directory such as `%LOCALAPPDATA%`; the user removed one on purpose.
- **Config:** new settings go in `Config` and `defaultConfig()`, with a JSON tag and a row in the README config table. Missing keys keep their defaults and unknown keys are ignored, so old config files must keep working.
- **Tray:** the icon must be re-added on the `TaskbarCreated` message (Explorer restarts are common here) and removed with `NIM_DELETE` on exit, or a dead icon is left behind. There is no `.ico` resource (stdlib-only build), so the icon is drawn in code.
- **Logging:** the tray menu is the only UI, so `ath.log` is the main way to see what it did. Log state changes, not every tick.

## Git

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `refactor:`, `chore:`, `!` plus a `BREAKING CHANGE:` footer when config or behaviour breaks).
- Prefer several focused commits over one large one.
- Don't add attribution lines to commit messages.
- Commit or push only when asked.