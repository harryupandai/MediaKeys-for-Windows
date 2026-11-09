package main

// Settings kept in %APPDATA%\MediaKeys\settings.json.
// (Start with Windows lives in the registry, where Windows looks for it.)

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type appSettings struct {
	Welcomed  bool `json:"welcomed"`
	ArrowsOn  bool `json:"arrowKeysControlMedia"`
	ShowCards bool `json:"showOnScreenCards"`
	// pointer so settings files from before this option default to on
	Typing       *bool `json:"typingSounds,omitempty"`
	TypingSounds bool  `json:"-"`
}

var settings = appSettings{ArrowsOn: true, ShowCards: true, TypingSounds: true}

func dataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "MediaKeys")
}

func loadSettings() {
	b, err := os.ReadFile(filepath.Join(dataDir(), "settings.json"))
	if err != nil {
		return
	}
	s := appSettings{ArrowsOn: true, ShowCards: true}
	if json.Unmarshal(b, &s) == nil {
		s.TypingSounds = s.Typing == nil || *s.Typing
		settings = s
	}
}

func saveSettings() {
	d := dataDir()
	if d == "" {
		return
	}
	os.MkdirAll(d, 0755)
	t := settings.TypingSounds
	settings.Typing = &t
	b, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(filepath.Join(d, "settings.json"), b, 0644)
}

// setArrows is the one place the arrow-key setting changes (tray, window, Scroll Lock).
func setArrows(on bool, announce bool) {
	arrowsOn = on
	settings.ArrowsOn = on
	saveSettings()
	refreshTray(false)
	refreshSettingsWindow()
	if announce {
		if on {
			showMessage("Arrow keys on", "← → skip  ·  ↑ ↓ volume")
		} else {
			showMessage("Arrow keys off", "Scroll Lock turns them back on")
		}
	}
}

func setShowCards(on bool) {
	settings.ShowCards = on
	saveSettings()
	refreshSettingsWindow()
}

func setTypingSounds(on bool) {
	settings.TypingSounds = on
	saveSettings()
	refreshSettingsWindow()
	if on {
		playClick() // a sample, so you know what it sounds like
	}
}
