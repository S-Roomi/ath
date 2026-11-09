# ATH

A small background process for Windows that automatically hides the taskbar when a fullscreen or borderless app is in front, and brings it back when you leave. It hides the taskbar windows directly through the Win32 API, with no other tools needed.

## How it works

- Windows tells it the moment the foreground window changes. It also checks every 300 ms as a fallback, for example for a window that resizes into fullscreen.
- If the window covers its whole monitor, it hides the taskbar (on every monitor, or only that monitor with `per_monitor`).
- When you switch away, it shows the taskbar again. If you open Start, Search, the notification center, quick settings or Alt-Tab, the taskbar comes back straight away, without the leave delay.
- Every foreground change is written to the log with the window's process, class and size.
- It only restores the taskbar if it was the one that hid it, and leaves a taskbar alone if something else already hid it.
- If Explorer brings the taskbar back while a fullscreen app is still in front (for example after Explorer restarts), it hides it again.
- If it crashes while the taskbar is hidden, the next start shows the taskbar again.

## Requirements

- Windows 10/11
- [Go](https://go.dev/dl/) 1.22 or newer, to build

## Build

On Windows:

```
go build -ldflags "-H=windowsgui -s -w" -o ath.exe
```

From Linux or WSL:

```
GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o ath.exe
```

## Usage

| Command | What it does |
| --- | --- |
| `ath.exe` | Start the background watcher |
| `ath.exe --stop` | Stop the running instance |
| `ath.exe --restore` | Stop the running instance and force the taskbar to show |
| `ath.exe --install-autostart` | Start automatically at login |
| `ath.exe --remove-autostart` | Remove autostart |

The exe has no window and prints nothing. Check `ath.log` (in the same folder as the exe).

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

Older config files that still have the `thide_*` settings keep working; those keys are ignored.
