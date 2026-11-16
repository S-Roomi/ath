# ATH

A small background process for Windows 10/11 that automatically hides the taskbar when a fullscreen or borderless app is in front.

## Features

- Hides the taskbar while a fullscreen or borderless app is in front, and brings it back when you leave
- Every monitor, or only the fullscreen app's monitor (`per_monitor`)
- Maximized windows don't count, even with "Automatically hide the taskbar" on
- Start, Search and flyouts still bring the taskbar back
- Pause, or keep the taskbar hidden, from the tray icon or a shortcut
- Never leaves the taskbar hidden: it only restores what it hid, and fixes it after a crash
- No install, no dependencies, safe with anti-cheat (no hooks or injection)

## Building From Source

- Requires [Go](https://go.dev/dl/) 1.22 or newer, to build

On Windows:

```
go build -ldflags "-H=windowsgui -s -w" -o bin/ath.exe
```

This puts the exe in `bin/`. `config.json`, `ath.log` and `hidden.marker` are created next to the exe, so they end up in `bin/` too. You can copy `ath.exe` anywhere else; its files follow it.

## Usage

| Command | What it does |
| --- | --- |
| `ath.exe` | Start the background watcher |
| `ath.exe --quit` | Quit the running instance |
| `ath.exe --restore` | Quit the running instance and force the taskbar to show |
| `ath.exe --install-autostart` | Start automatically at login |
| `ath.exe --remove-autostart` | Remove autostart |
| `ath.exe --version` | Show the version |

### Tray icon

While it runs, ATH shows an icon in the notification area. Hover over it to see what ATH is doing: `idle` (running, taskbar visible), `taskbar hidden for <app>`, `keeping taskbar hidden` or `paused`. Right-click or left-click it for:

| Item | What it does |
| --- | --- |
| Pause / Resume | Stops hiding the taskbar until you resume (Ctrl+Alt+H) |
| Keep taskbar hidden | Hides the taskbar whatever is in front, fullscreen or not (Ctrl+Alt+T) |
| Never hide for *app* | Adds the last fullscreen app to `ignore` in `config.json` |
| Start with Windows | Turns autostart on or off, like `--install-autostart` / `--remove-autostart` |
| Open config | Opens `config.json` |
| Open log | Opens `ath.log` |
| Quit and Restore taskbar | Same as `--restore` |
| Quit | Same as `--quit` |

## Config

`config.json` is created next to the exe on first run. Changes are picked up automatically.

| Setting | Default | Meaning |
| --- | --- | --- |
| `per_monitor` | `false` | Hide only the taskbar on the fullscreen app's monitor, instead of every taskbar |
| `reassert` | `true` | Hide the taskbar again if Explorer shows it while a fullscreen app is in front |
| `ignore` | `explorer.exe`, `vlc.exe` | Apps that never trigger hiding |
| `whitelist` | empty | If set, only these apps trigger hiding |
| `poll_ms` | 300 | How often to check the foreground window |
| `enter_delay_ms` | 500 | Wait before hiding |
| `leave_delay_ms` | 400 | Wait before showing |
| `tray_icon` | `true` | Show the icon in tray |
| `hotkey` | `"ctrl+alt+h"` | Shortcut that toggles Pause / Resume. If another app already uses the combination, it is not registered and the log says so |
| `hide_hotkey` | `"ctrl+alt+t"` | Shortcut that toggles Keep taskbar hidden |
