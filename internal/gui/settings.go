//go:build gui

package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Theme preference values stored in gui.json.
const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

// settings is the user-scope gui.json.  It holds window geometry and theme
// preference only — never secrets, never an authorization input (SPEC §15.2).
type settings struct {
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Theme  string `json:"theme,omitempty"`
}

// defaultGuiPath returns %LOCALAPPDATA%\dns-client\gui.json, falling back to
// the per-user config dir off Windows.
func defaultGuiPath() (path string) {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "dns-client", "gui.json")
	}

	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "dns-client", "gui.json")
	}

	return filepath.Join(os.TempDir(), "dns-client", "gui.json")
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
