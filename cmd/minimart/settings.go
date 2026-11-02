package main

import (
	"encoding/json"
	"log"
	"path/filepath"
	"sync"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
)

// settings are MiniMart's own choices, kept in settings.json in its
// folder: the order of each list, and where the user's own game files are.
type settings struct {
	// Sort: each list's order ("apps", "themes", "icons", "games"), one of
	// the order values.
	Sort map[string]order `json:"sort,omitempty"`
	// GameFiles: a folder address the user typed, on a server of their own,
	// holding game files they own (see gamefile.go). Empty: none.
	GameFiles string `json:"gamefiles,omitempty"`
}

func settingsPath() string { return filepath.Join(appdir.Dir(), "settings.json") }

var settingsMu sync.Mutex

// loadSettings reads settings.json; a missing or damaged file gives the
// defaults (a damaged one is set aside, see loadJSON).
func loadSettings() settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var st settings
	if err := loadJSON(settingsPath(), &st); err != nil {
		log.Print(err)
	}
	return st
}

// updateSettings reads settings.json, applies change and writes it back.
func updateSettings(change func(*settings)) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var st settings
	if err := loadJSON(settingsPath(), &st); err != nil {
		log.Print(err)
	}
	change(&st)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(settingsPath(), append(b, '\n'), 0644)
}

// listOrder is the saved order of a list, or its default.
func listOrder(list string, offered []order) order {
	return validOrder(loadSettings().Sort[list], offered)
}

// saveOrder remembers a list's order.
func saveOrder(list string, o order) {
	err := updateSettings(func(st *settings) {
		if st.Sort == nil {
			st.Sort = map[string]order{}
		}
		st.Sort[list] = o
	})
	if err != nil {
		log.Printf("saving the order: %v", err)
	}
}
