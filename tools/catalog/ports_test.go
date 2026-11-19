package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestShortcutGame(t *testing.T) {
	cases := []struct {
		src       string
		dir, data string
		err       bool
	}{
		{"GameDir=\"Undertale\"\nGameExecutable=\"butterscotch\"\nGameDataFile=\"data.win\"\n", "Undertale", "data.win", false},
		// Onion's fallbacks: RomDir for GameDir, GameExecutable for the data file
		{"RomDir=\"Doom\"\nGameExecutable=\"prboom\"\n", "Doom", "prboom", false},
		{"GameDir=\"X\"\nRomFile=\"x.wad\"\n", "X", "x.wad", false},
		// a plain launcher: nothing to check
		{"#!/bin/sh\ncd Games/T && ./T\n", "", "", false},
		{"Core=\"prboom\"\n", "", "", true},
		{"GameDir=\"$HOME\"\nGameDataFile=\"a\"\n", "", "", true},
		{"GameDir=\"A\"\nGameDataFile=\"sub/a\"\n", "", "", true},
		{"GameDir=\"A\"\nGameDataFile=\"*.wad\"\n", "", "", true},
		{"GameDir=\"../..\"\nGameDataFile=\"a\"\n", "", "", true},
		{"GameDir=\"A\"\n", "", "", true},
	}
	for _, c := range cases {
		g, err := shortcutGame([]byte(c.src))
		if (err != nil) != c.err || err == nil && (g.Dir != c.dir || g.Data != c.data) {
			t.Errorf("%q: got %+v, %v", c.src, g, err)
		}
	}
}

func TestPortLayout(t *testing.T) {
	sc := "GameDir=\"G\"\nGameDataFile=\"data.win\"\n"
	t.Run("onion", func(t *testing.T) {
		var p Port
		err := portLayout(zipOf(t, map[string]string{
			"Roms/PORTS/Games/G/engine":            "12345",
			"Roms/PORTS/Shortcuts/RPG/G.port":      sc,
			"Roms/PORTS/Shortcuts/RPG/G.notfound":  sc,
			"Roms/PORTS/Imgs/G.png":                "png",
			"README.md":                            "readme, not installed",
			"__MACOSX/Roms/PORTS/Games/G/._engine": "junk",
		}), &p)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root != "Roms/PORTS/" || p.Loose || len(p.Games) != 1 || p.Games[0].Shortcut != "RPG/G" || p.Games[0].Shipped {
			t.Fatalf("%+v", p)
		}
		if want := int64(len("12345") + 2*len(sc) + len("png")); p.Unpacked != want {
			t.Fatalf("unpacked %d, want %d", p.Unpacked, want)
		}
	})
	t.Run("shipped data", func(t *testing.T) {
		var p Port
		if err := portLayout(zipOf(t, map[string]string{
			"Roms/PORTS/Games/G/bin/Data.Win": "x",
			"Roms/PORTS/Shortcuts/G.port":     sc,
		}), &p); err != nil {
			t.Fatal(err)
		}
		if !p.Games[0].Shipped {
			t.Fatal("data file one folder down, other case, not seen as shipped")
		}
	})
	t.Run("loose windows", func(t *testing.T) {
		var p Port
		if err := portLayout(zipOf(t, map[string]string{
			`Games\I\I`: "bin",
			`I.port`:    "#!/bin/sh\n./I\n",
		}), &p); err != nil {
			t.Fatal(err)
		}
		if p.Root != "" || !p.Loose || len(p.Games) != 1 || p.Games[0].Shortcut != "I" || !p.Games[0].Shipped {
			t.Fatalf("%+v", p)
		}
	})
	for name, files := range map[string]map[string]string{
		"no shortcuts": {"Roms/PORTS/Games/G/x": "x"},
		"slip":         {"Roms/PORTS/Shortcuts/G.port": sc, "Roms/PORTS/Games/../../evil": "x"},
		"case clash":   {"Roms/PORTS/Shortcuts/G.port": sc, "Roms/PORTS/Games/G/a": "1", "Roms/PORTS/Games/g/A": "2"},
		"core":         {"Roms/PORTS/Shortcuts/G.port": "Core=\"x\"\n"},
	} {
		var p Port
		if err := portLayout(zipOf(t, files), &p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPortZips(t *testing.T) {
	got := portZips([]asset{
		{Name: "game-spruce.zip", Size: 1},
		{Name: "game-source.zip", Size: 1},
		{Name: "game.zip", Size: 1},
		{Name: "game-miyoo.zip", Size: 9},
		{Name: "game-OnionOS.zip", Size: 50},
		{Name: "game.7z", Size: 1},
	})
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "game-OnionOS.zip,game-miyoo.zip,game.zip" {
		t.Fatal(names)
	}
}

func TestSignedZips(t *testing.T) {
	as := []asset{{Name: "EndlessSky-Miyoo-1.5.zip"}, {Name: "EndlessSky-Miyoo-1.5.zip.minisig"}, {Name: "other.zip"}, {Name: "other.minisig"}}
	got := signedZips(portZips(as), as)
	if len(got) != 1 || got[0].Name != "EndlessSky-Miyoo-1.5.zip" {
		t.Fatal(got)
	}
}

func TestPortsOwn(t *testing.T) {
	b, err := os.ReadFile("ports-own.json")
	if err != nil {
		t.Fatal(err)
	}
	var own []Port
	if err := json.Unmarshal(b, &own); err != nil {
		t.Fatal(err)
	}
	for _, p := range own {
		if !p.Signed || githubRepoRe.FindStringSubmatch(p.Upstream) == nil || portPath(p.VersionFile) == "" {
			t.Errorf("%s: %+v", p.Name, p)
		}
	}
}

func TestOwnerSkipsAndOwnRepos(t *testing.T) {
	h := sha256.Sum256([]byte("someone/skipped"))
	ownerSkips[hex.EncodeToString(h[:])] = true
	defer delete(ownerSkips, hex.EncodeToString(h[:]))
	g := &gen{}
	if _, err := g.repoApps("Someone/Skipped"); !errors.Is(err, errOwnerSkip) {
		t.Fatalf("skipped repository: %v", err)
	}
	if _, err := g.repoApps("ZakiJSS/MiniDeck"); err == nil || !strings.Contains(err.Error(), "owner's own") {
		t.Fatalf("own repository: %v", err)
	}
	if ownerSkipped("someone/else") || ownRepo("zakijss-fan/x") {
		t.Fatal("too broad")
	}
}
