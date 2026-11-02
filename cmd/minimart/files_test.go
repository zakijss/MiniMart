package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Theme and icon pack names from a repository's list become paths on the
// card, and Remove deletes them: names that would leave the folder are
// dropped.
func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"Hippo Stock", true},
		{"Pixel by Jeltron", true},
		{"Café (remix)", true},
		{"", false},
		{"..", false},
		{"../../App", false},
		{".hidden", false},
		{"a/b", false},
		{`a\b`, false},
		{"a:b", false},
		{"tab\there", false},
		{strings.Repeat("x", 201), false},
	} {
		if got := validName(tc.name); got != tc.ok {
			t.Errorf("validName(%q) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// A records or sources file that doesn't parse is kept aside, not
// overwritten by the next save, and "null" gives an empty map rather than a
// nil one that would panic on the next install.
func TestLoadRecordsDamaged(t *testing.T) {
	for _, body := range []string{"null", "{not json", `["a list"]`, `{"k": 5}`} {
		dir := t.TempDir()
		t.Setenv("MINIDECK_APPDIR", dir)
		p := filepath.Join(dir, "games-installed.json")
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		m := loadRecords()
		if m == nil {
			t.Fatalf("%q: nil map", body)
		}
		m["x"] = gameRecord{System: "GB"}
		if body != "null" {
			if _, err := os.Stat(p + ".unreadable"); err != nil {
				t.Errorf("%q: not set aside: %v", body, err)
			}
		}
	}
	dir := t.TempDir()
	t.Setenv("MINIDECK_APPDIR", dir)
	os.WriteFile(filepath.Join(dir, "sources.json"), []byte("[{"), 0644)
	if got := LoadSources(); len(got) != len(defaultSources) {
		t.Errorf("damaged sources.json: %d sources, want the %d built-in ones", len(got), len(defaultSources))
	}
	if _, err := os.Stat(filepath.Join(dir, "sources.json.unreadable")); err != nil {
		t.Errorf("damaged sources.json not set aside: %v", err)
	}
}

// A second damaged file is set aside beside the first, never over it.
func TestLoadJSONKeepsEarlierCopies(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sources.json")
	for i, body := range []string{"first {", "second {", "third {"} {
		os.WriteFile(p, []byte(body), 0644)
		var v []Source
		if err := loadJSON(p, &v); err == nil {
			t.Fatalf("%d: damaged file accepted", i)
		}
	}
	for name, want := range map[string]string{
		"sources.json.unreadable":   "first {",
		"sources.json.unreadable.2": "second {",
		"sources.json.unreadable.3": "third {",
	} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v, want %q", name, b, err, want)
		}
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("the damaged file is still in place")
	}
}

// Removing a game whose record names an unknown system or a path outside
// its folder deletes nothing outside Roms/<system>.
func TestRemoveGameRecordsChecked(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	victim := filepath.Join(sd, "Roms", "keep.gb")
	os.MkdirAll(filepath.Dir(victim), 0755)
	os.WriteFile(victim, []byte("x"), 0644)
	g := Game{ID: "g", System: "GB", Repo: "r"}
	for _, rec := range []gameRecord{{System: "NOPE", Files: []string{"keep.gb"}}, {System: "GB", Files: []string{"../keep.gb"}}} {
		if err := saveRecords(map[string]gameRecord{g.key(): rec}); err != nil {
			t.Fatal(err)
		}
		RemoveGame(g)
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("record %+v deleted a file outside the system's folder", rec)
		}
	}
}

// Malformed catalogues, game lists and Homebrew Hub replies give an error
// or nothing, never a panic.
func TestParsersMalformed(t *testing.T) {
	inputs := []string{
		"", "null", "[]", "{}", `{"apps": null}`, `{"apps": [null, 1, "x", {}]}`,
		`{"games": [{"system": null}]}`, `{"entries": 5}`, `{"entries": [null, {"files": null}]}`,
		`{"entries": [{"typetag": "game", "slug": "a", "title": "A", "license": "MIT", "files": [{}]}]}`,
		`{"entries": [{"developer": [[[]]], "files": [{"filename": "../x.gb"}]}]}`,
		`{"apps": [{"name": "x", "folder": "..", "zip": "z", "sha256": "` + strings.Repeat("a", 64) + `"}]}`,
		strings.Repeat("[", 10000),
	}
	sys, _ := systemFor("GB")
	for _, in := range inputs {
		if c, err := ParseCatalog([]byte(in), "t"); err == nil && c == nil {
			t.Errorf("ParseCatalog(%.40q): nil catalogue without an error", in)
		}
		ParseGameCatalog([]byte(in), "t", "r")
		parseHomebrewHub(strings.NewReader(in), sys, "https://hh.example/api/")
	}
}
