//go:build windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

type Config struct {
	PollMs       int      `json:"poll_ms"`        // how often to check the foreground window
	EnterDelayMs int      `json:"enter_delay_ms"` // fullscreen must hold this long before hiding
	LeaveDelayMs int      `json:"leave_delay_ms"` // non-fullscreen must hold this long before showing
	PerMonitor   bool     `json:"per_monitor"`    // hide only the taskbar on the fullscreen app's monitor
	Reassert     bool     `json:"reassert"`       // hide the taskbar again if Explorer brings it back
	Ignore       []string `json:"ignore"`         // process names that never trigger hiding
	Whitelist    []string `json:"whitelist"`      // if non-empty, ONLY these processes trigger hiding
	TrayIcon     bool     `json:"tray_icon"`      // show a notification-area icon (read at startup only)
}

func defaultConfig() Config {
	return Config{
		PollMs:       300,
		EnterDelayMs: 500,
		LeaveDelayMs: 400,
		PerMonitor:   false,
		Reassert:     true,
		Ignore:       []string{"explorer.exe", "vlc.exe"},
		Whitelist:    []string{},
		TrayIcon:     true,
	}
}

// loadConfig reads path over the defaults, so missing keys keep their default
// value. Unknown keys (such as the old thide settings) are ignored.
func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), err
	}
	if cfg.PollMs < 50 {
		cfg.PollMs = 50
	}
	for i := range cfg.Ignore {
		cfg.Ignore[i] = strings.ToLower(cfg.Ignore[i])
	}
	for i := range cfg.Whitelist {
		cfg.Whitelist[i] = strings.ToLower(cfg.Whitelist[i])
	}
	return cfg, nil
}

// addToIgnore adds proc to the ignore list in the config file at path. It
// edits only the "ignore" key, so other keys (including unknown ones) keep
// their values; key order may change. If the file has no "ignore" key, the
// default list is kept and proc is added to it. It returns false if proc was
// already ignored.
func addToIgnore(path, proc string) (bool, error) {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// Refuse to rewrite a file we cannot parse, so the user's edits are not lost.
		if err := json.Unmarshal(data, &raw); err != nil {
			return false, err
		}
	case os.IsNotExist(err):
		data, _ = json.Marshal(defaultConfig())
		_ = json.Unmarshal(data, &raw)
	default:
		return false, err
	}

	list := defaultConfig().Ignore
	if v, ok := raw["ignore"]; ok {
		list = nil
		if err := json.Unmarshal(v, &list); err != nil {
			return false, err
		}
	}
	for _, p := range list {
		if strings.EqualFold(p, proc) {
			return false, nil
		}
	}
	list = append(list, proc)
	if raw["ignore"], err = json.Marshal(list); err != nil {
		return false, err
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	// Write a temp file and rename it over the config, so a reader never sees
	// a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func writeDefaultConfig(path string) {
	data, _ := json.MarshalIndent(defaultConfig(), "", "  ")
	_ = os.WriteFile(path, data, 0o644)
}

func modTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
