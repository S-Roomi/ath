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
