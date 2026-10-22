//go:build gui

package gui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// Theme preference values stored in gui.json.
const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

// settings is the user-scope gui.json.  It holds window geometry, theme
// preference, and the last sanitized config summary so the editor still works
// while the service is stopped.  A ConfigSummary carries only labels, flags,
// counts, hosts and IP literals — never an id or a credential-bearing URL
// (SPEC §15.2) — so caching it here is safe.  It is never an authorization
// input.
type settings struct {
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Theme  string `json:"theme,omitempty"`

	// Tray is the opt-in system tray icon.  It is off by default; the
	// behaviours below are only honoured while it is on.
	Tray           bool `json:"tray,omitempty"`
	MinimizeToTray bool `json:"minimize_to_tray,omitempty"`
	CloseToTray    bool `json:"close_to_tray,omitempty"`

	// StartWithWindows launches the tray at sign-in via the per-user logon task.
	StartWithWindows bool `json:"start_with_windows,omitempty"`

	// X and Y are the window's top-left in screen pixels, remembered on close
	// and restored on open.  Zero (omitted) means "let the OS place it" — a
	// genuine (0,0) window is not preserved.
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`

	Summary *ipc.ConfigSummary `json:"summary,omitempty"`
}

// defaultGuiPath returns %LOCALAPPDATA%\sigildns\gui.json, falling back to
// the per-user config dir off Windows.
func defaultGuiPath() (path string) {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "SigilDNS", "gui.json")
	}

	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "sigildns", "gui.json")
	}

	return filepath.Join(os.TempDir(), "sigildns", "gui.json")
}

// loadSettings reads gui.json.  A missing or corrupt file yields the zero
// value, so bad state can never keep the window from opening.
func loadSettings(path string) (s settings) {
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}

	_ = json.Unmarshal(data, &s)

	return s
}

// saveSettings writes gui.json, creating the parent directory if needed.
func saveSettings(path string, s settings) (err error) {
	if dir := filepath.Dir(path); dir != "" {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}
