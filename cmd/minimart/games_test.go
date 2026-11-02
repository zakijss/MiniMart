package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zakijss/minideck/cache"
)

func TestLicenceOK(t *testing.T) {
	for l, want := range map[string]bool{
		"MIT": true, "Zlib": true, "ZLib": true, "0BSD": true, "Unlicense": true, "WTFPL-2.0": true,
		"GPL-3.0": true, "GPL-3.0-only": true, "GPL-3.0-or-later": true, "GPL-2.0+": true, "LGPL-2.1": true,
		"MPL-2.0": true, "Apache-2.0": true, "BSD-3-Clause": true, "CC0-1.0": true, "FSFAP": true,
		"CC-BY-4.0": true, "CC-BY-SA 4.0": true, "cc by-sa 4.0": true, "CC-BY-NC-ND-4.0": true, "CC-BY-NC-SA": true,
		"MIT / CC-BY-4.0 (Assets)": true, "MIT and CC0-1.0": true,
		"": false, "   ": false, "CC-BY ish": false, "Beta Cat": false, "Freeware": false, "All rights reserved": false,
		"MIT / proprietary": false, "Commercial": false, "Public domain": false, "GPL": false,
	} {
		if got := licenceOK(l); got != want {
			t.Errorf("licenceOK(%q) = %v, want %v", l, got, want)
		}
	}
}

func TestSystems(t *testing.T) {
	for in, want := range map[string]string{
		"GB": "GB", "gbc": "GBC", "GBA": "GBA", "NES": "FC", "FC": "FC", "SNES": "SFC", "SFC": "SFC",
		"Genesis": "MD", "Mega Drive": "MD", "MD": "MD", "PCE": "PCE", "TG-16": "PCE", "TurboGrafx-16": "PCE",
	} {
		if s, ok := systemFor(in); !ok || s.Folder != want {
			t.Errorf("systemFor(%q) = %q, %v; want %q", in, s.Folder, ok, want)
		}
	}
	if _, ok := systemFor("PS1"); ok {
		t.Error("unknown system accepted")
	}
	t.Setenv("STORE_SDCARD", "/mnt/SDCARD")
	gb, _ := systemFor("GB")
	if gb.dir() != "/mnt/SDCARD/Roms/GB" {
		t.Error(gb.dir())
	}
	if ext, ok := gb.romExt("files/Game.CGB"); !ok || ext != ".gbc" {
		t.Errorf("cgb: %q %v", ext, ok)
	}
	if _, ok := gb.romExt("game.zip"); ok {
		t.Error("zip taken as a ROM")
	}
	md, _ := systemFor("MD")
	if _, ok := md.romExt("x.gb"); ok {
		t.Error("a Game Boy ROM accepted for the Mega Drive")
	}
	if n := romName(`Bad: "name"/with*chars?.`, ".gb"); n != "Bad namewithchars.gb" {
		t.Errorf("romName: %q", n)
	}
	if romName("...", ".nes") != "game.nes" || romName(strings.Repeat("é", 200), ".gb") != strings.Repeat("é", 80)+".gb" {
		t.Error("romName edge cases")
	}
}

const hhReply = `{"results": 9, "page_total": 1, "entries": [
 {"slug": "good", "title": "Good Game", "developer": "Ann", "typetag": "game", "license": "MIT",
  "description": "A fine game.", "files": [{"filename": "src.zip"}, {"filename": "files/good game.gb", "playable": true, "default": true}],
  "baserepo": "https://github.com/gbdev/database", "basepath": "database-gb"},
 {"slug": "list-dev", "title": "Pair", "developer": ["Bo", {"name": "Cy", "link": "x"}], "typetag": "game", "gameLicense": "CC-BY-NC-ND-4.0",
  "files": [{"filename": "pair.cgb", "playable": true}], "baserepo": "https://example.org/db", "basepath": "database-gb",
  "wip": true},
 {"slug": "hashed", "title": "Hashed", "developer": {"name": "Di"}, "typetag": "game", "gameLicense": "GPL-3.0-or-later",
  "files": [{"filename": "h.gb", "playable": true, "default": true, "hash": {"sha256": "` + "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789" + `"}}],
  "baserepo": "https://github.com/gbdev/database"},
 {"slug": "nolicence", "title": "No Licence", "typetag": "game", "files": [{"filename": "n.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "freetext", "title": "Free Text", "typetag": "game", "license": "Beta Cat", "files": [{"filename": "f.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "demo", "title": "A Demo", "typetag": "demo", "license": "MIT", "files": [{"filename": "d.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "nsfw", "title": "Adult", "typetag": "game", "license": "MIT", "nsfw": true, "files": [{"filename": "a.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "assets", "title": "Art Reserved", "typetag": "game", "license": "MIT", "assetsLicense": "All rights reserved", "files": [{"filename": "r.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "odd", "title": "Odd", "typetag": "game", "license": "MIT", "wip": "yes", "files": [{"filename": "o.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "norom", "title": "No ROM", "typetag": "game", "license": "MIT", "files": [{"filename": "n.lzh"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "../up", "title": "Escape", "typetag": "game", "license": "MIT", "files": [{"filename": "e.gb"}], "baserepo": "https://github.com/gbdev/database"},
 {"slug": "dots", "title": "Dots", "typetag": "game", "license": "MIT", "files": [{"filename": "../e.gb"}], "baserepo": "https://github.com/gbdev/database"}
], "page_current": 1}`

func TestParseHomebrewHub(t *testing.T) {
	gb, _ := systemFor("GB")
	gs, err := parseHomebrewHub(strings.NewReader(hhReply), gb, "https://hh3.gbdev.io/api/")
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 3 {
		t.Fatalf("want 3 games, got %d: %+v", len(gs), gs)
	}
	g := gs[0]
	if g.ID != "good" || g.Author != "Ann" || g.License != "MIT" || g.System != "GB" || g.Desc != "A fine game." ||
		g.URL != "https://raw.githubusercontent.com/gbdev/database/HEAD/entries/good/files/good%20game.gb" {
		t.Errorf("good: %+v", g)
	}
	if g := gs[1]; g.Author != "Bo, Cy" || !g.WIP || g.Licence() != "CC-BY-NC-ND-4.0" ||
		g.URL != "https://hh3.gbdev.io/static/database-gb/entries/list-dev/pair.cgb" {
		t.Errorf("list-dev: %+v", g)
	}
	if g := gs[2]; g.Author != "Di" || g.SHA256 != strings.ToLower("ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789") {
		t.Errorf("hashed: %+v", g)
	}
	if _, err := parseHomebrewHub(strings.NewReader(`<html>404</html>`), gb, ""); err == nil {
		t.Error("HTML accepted")
	}
	if _, err := parseHomebrewHub(strings.NewReader(`{"detail": "x"}`), gb, ""); err == nil {
		t.Error("a reply without entries accepted")
	}
}

func TestParseGameCatalog(t *testing.T) {
	sum := strings.Repeat("a", 64)
	c, err := ParseGameCatalog([]byte(`{"name": "Mine", "games": [
		{"title": "Sonic-like", "system": "Genesis", "license": "GPL-3.0", "url": "https://x/sl.bin", "sha256": "`+sum+`"},
		{"title": "Zipped", "system": "SNES", "license": "MIT", "url": "https://x/z.zip"},
		{"title": "No licence", "system": "MD", "url": "https://x/n.md"},
		{"title": "Freeware", "system": "MD", "license": "freeware", "url": "https://x/f.md"},
		{"title": "Plain http", "system": "MD", "license": "MIT", "url": "http://x/p.md"},
		{"title": "PS1", "system": "PS1", "license": "MIT", "url": "https://x/p.bin"},
		{"title": "Wrong ext", "system": "PCE", "license": "MIT", "url": "https://x/w.gb"},
		{"title": "Bad sum", "system": "PCE", "license": "MIT", "url": "https://x/b.pce", "sha256": "123"},
		{"id": "../x", "title": "Bad id", "system": "PCE", "license": "MIT", "url": "https://x/i.pce"}]}`), "Mine", "https://x/games.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Games) != 2 {
		t.Fatalf("want 2 games, got %+v", c.Games)
	}
	if g := c.Games[0]; g.System != "MD" || g.ID != "sonic-like" || g.Source != "Mine" || g.SHA256 != sum {
		t.Errorf("%+v", g)
	}
	if c.Games[1].System != "SFC" {
		t.Errorf("%+v", c.Games[1])
	}
	if u, err := NormalizeURL("games", "hh3.gbdev.io/api"); err != nil || u != "https://hh3.gbdev.io/api/" || !isHomebrewHub(u) {
		t.Errorf("NormalizeURL: %q %v", u, err)
	}
}

func TestInstallGame(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	rom := "ROM v1"
	zipped := makeZip(t, map[string]string{"pack/readme.txt": "hi", "pack/game.sfc": "SNES ROM", "pack/game.exe": "no"})
	evil := makeZip(t, map[string]string{"../../escape.sfc": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.gb":
			w.Write([]byte(rom))
		case "/game.zip":
			w.Write(zipped)
		case "/evil.zip":
			w.Write(evil)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	nop := func(int64, int64) {}
	romDir := filepath.Join(sd, "Roms", "GB")
	os.MkdirAll(romDir, 0755)
	os.WriteFile(filepath.Join(romDir, "GB_cache6.db"), []byte("x"), 0644)

	g := Game{ID: "good", Title: "Good: Game", System: "GB", License: "MIT", URL: srv.URL + "/good.gb", Repo: "r"}
	// a checksum that doesn't match: nothing installed
	bad := g
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := f.DownloadGame(bad, nop); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	sum := sha256.Sum256([]byte(rom))
	ok := g
	ok.SHA256 = hex.EncodeToString(sum[:])
	p, err := f.DownloadGame(ok, nop)
	if err != nil || !p.verified || p.Changed {
		t.Fatalf("download: %v %+v", err, p)
	}
	names, err := p.Install()
	if err != nil || len(names) != 1 || names[0] != "Good Game.gb" {
		t.Fatalf("install: %v %q", err, names)
	}
	if b, _ := os.ReadFile(filepath.Join(romDir, "Good Game.gb")); string(b) != rom {
		t.Fatal("ROM not in place")
	}
	if _, err := os.Stat(filepath.Join(romDir, "GB_cache6.db")); err == nil {
		t.Error("Onion's list cache not cleared")
	}
	if installedFile(loadRecords(), g) != "Roms/GB/Good Game.gb" {
		t.Error("not recorded")
	}
	if ents, _ := os.ReadDir(stagingDir()); len(ents) != 0 {
		t.Errorf("staging left behind: %v", ents)
	}
	// a later download that differs is flagged (no checksum in the repository)
	rom = "ROM v2"
	p, err = f.DownloadGame(g, nop)
	if err != nil || !p.Changed || p.Before == "" {
		t.Fatalf("change not noticed: %v %+v", err, p)
	}
	if _, err := p.Install(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(romDir, "Good Game.gb")); string(b) != "ROM v2" {
		t.Fatal("not replaced")
	}
	// a file the store didn't install is never replaced
	other := Game{ID: "other", Title: "Good Game", System: "GB", License: "MIT", URL: srv.URL + "/good.gb", Repo: "r2"}
	if p, err = f.DownloadGame(other, nop); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Install(); err == nil {
		t.Fatal("overwrote a file the store doesn't own")
	}
	// zips: only the ROM is kept, named after the game
	z := Game{ID: "z", Title: "Zip Game", System: "SFC", License: "MIT", URL: srv.URL + "/game.zip", Repo: "r"}
	if p, err = f.DownloadGame(z, nop); err != nil {
		t.Fatal(err)
	}
	if names, err = p.Install(); err != nil || len(names) != 1 || names[0] != "Zip Game.sfc" {
		t.Fatalf("zip: %v %q", err, names)
	}
	ents, _ := os.ReadDir(filepath.Join(sd, "Roms", "SFC"))
	if len(ents) != 1 {
		t.Errorf("extra files from the zip: %v", ents)
	}
	// zip slip is refused
	e := Game{ID: "evil", Title: "Evil", System: "SFC", License: "MIT", URL: srv.URL + "/evil.zip", Repo: "r"}
	if p, err = f.DownloadGame(e, nop); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Install(); err == nil {
		t.Fatal("zip slip not refused")
	}
	if _, err := os.Stat(filepath.Join(sd, "escape.sfc")); err == nil {
		t.Fatal("file written outside")
	}
	// removing deletes only what the store installed
	os.WriteFile(filepath.Join(romDir, "Mine.gb"), []byte("user's"), 0644)
	if err := RemoveGame(g); err != nil || installedFile(loadRecords(), g) != "" {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(romDir, "Good Game.gb")); err == nil {
		t.Error("ROM not removed")
	}
	if _, err := os.Stat(filepath.Join(romDir, "Mine.gb")); err != nil {
		t.Error("removed the user's own ROM")
	}
	// a missing download leaves nothing behind
	if _, err := f.DownloadGame(Game{ID: "gone", Title: "Gone", System: "GB", URL: srv.URL + "/gone.gb"}, nop); err == nil {
		t.Error("404 accepted")
	}
	if ents, _ := os.ReadDir(stagingDir()); len(ents) != 0 {
		t.Errorf("staging left behind: %v", ents)
	}
}

func TestGamesFromSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search" && r.URL.Query().Get("platform") == "GB":
			w.Write([]byte(hhReply))
		case r.URL.Path == "/api/search":
			w.Write([]byte(`{"entries": []}`))
		case r.URL.Path == "/games.json":
			w.Write([]byte(`{"games": [{"title": "Good Game", "system": "GB", "license": "MIT", "url": "https://x/dupe.gb"},
				{"title": "Megaman-ish", "system": "MD", "license": "MIT", "url": "https://x/m.md"}]}`))
		}
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	gs, errs := f.Games([]Source{
		{Name: "Hub", Type: "games", URL: srv.URL + "/api/", Enabled: true},
		{Name: "Mine", Type: "games", URL: srv.URL + "/games.json", Enabled: true},
		{Name: "Off", Type: "games", URL: srv.URL + "/off.json"},
	})
	if len(errs) != 0 || len(gs) != 4 {
		t.Fatalf("%d games, %v", len(gs), errs)
	}
	if gs[0].Title != "Good Game" || gs[0].Source != "Hub" || gs[1].Title != "Hashed" || gs[2].Title != "Megaman-ish" {
		t.Errorf("order or dedupe: %+v", gs)
	}
	// the compact list is kept on the card
	gb, _ := systemFor("GB")
	if _, ok := f.cache.Get("games2|" + hhSearchURL(srv.URL+"/api/", gb)); !ok {
		t.Error("list not cached")
	}
}

func TestRomsFromZipNames(t *testing.T) {
	z := filepath.Join(t.TempDir(), "g.download")
	os.WriteFile(z, makeZip(t, map[string]string{"a/game.sfc": "1", "b/game.SFC": "2", "__MACOSX/a/._game.sfc": "x", "c/notes.md": "md"}), 0644)
	sfc, _ := systemFor("SNES")
	roms, err := romsFromZip(z, sfc, "Title")
	if err != nil || len(roms) != 2 {
		t.Fatalf("%v %+v", err, roms)
	}
	if !(roms[0].name == "game.sfc" && roms[1].name == "game (2).sfc") {
		t.Errorf("names: %+v", roms)
	}
	gb, _ := systemFor("GB")
	if _, err := romsFromZip(z, gb, "Title"); err == nil {
		t.Error("a zip without Game Boy ROMs accepted for the Game Boy")
	}
}

// A game zip's directory is checked before anything is unpacked: too many
// ROMs, or ROMs claiming more than maxGameUnpacked, write nothing.
func TestRomsFromZipLimits(t *testing.T) {
	gb, _ := systemFor("GB")
	dir := t.TempDir()
	write := func(name string, build func(w *zip.Writer)) string {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		build(w)
		w.Close()
		p := filepath.Join(dir, name)
		os.WriteFile(p, buf.Bytes(), 0644)
		return p
	}
	nothingUnpacked := func(zp string) {
		t.Helper()
		if _, err := os.Stat(zp + ".unzip"); err == nil {
			t.Errorf("%s: unpacked before refusing", filepath.Base(zp))
		}
	}

	many := write("many.download", func(w *zip.Writer) {
		for i := range maxGameRoms + 1 {
			e, _ := w.Create(fmt.Sprintf("g%d.gb", i))
			e.Write([]byte("rom"))
		}
	})
	if _, err := romsFromZip(many, gb, "T"); err == nil {
		t.Error("too many ROMs accepted")
	}
	nothingUnpacked(many)

	// a ROM whose header claims 300 MB (a zip bomb needn't hold the bytes to
	// be refused; one that did would take this long to build)
	big := write("big.download", func(w *zip.Writer) {
		e, _ := w.CreateRaw(&zip.FileHeader{Name: "g.gb", Method: zip.Store, CompressedSize64: 1, UncompressedSize64: 300 << 20})
		e.Write([]byte("x"))
	})
	if _, err := romsFromZip(big, gb, "T"); err == nil || !strings.Contains(err.Error(), "too big") {
		t.Errorf("a 300 MB ROM: %v", err)
	}
	nothingUnpacked(big)

	slip := write("slip.download", func(w *zip.Writer) {
		e, _ := w.Create("../../g.gb")
		e.Write([]byte("rom"))
	})
	if _, err := romsFromZip(slip, gb, "T"); err == nil {
		t.Error("a path out of the folder accepted")
	}
	nothingUnpacked(slip)

	// only the ROM is unpacked, under a plain name
	one := write("one.download", func(w *zip.Writer) {
		for _, n := range []string{"docs/readme.txt", "art/cover.png", "sub/game.gb"} {
			e, _ := w.Create(n)
			e.Write([]byte(n))
		}
	})
	roms, err := romsFromZip(one, gb, "My Game")
	if err != nil || len(roms) != 1 || roms[0].name != "My Game.gb" {
		t.Fatalf("%v %+v", err, roms)
	}
	ents, _ := os.ReadDir(one + ".unzip")
	if len(ents) != 1 {
		t.Errorf("%d files unpacked, want 1", len(ents))
	}
	if b, _ := os.ReadFile(roms[0].from); string(b) != "sub/game.gb" {
		t.Errorf("unpacked %q", b)
	}

	// the bytes written are counted, whatever the directory says
	r, _ := zip.OpenReader(one)
	defer r.Close()
	if _, err := unpackFile(r.File[0], filepath.Join(dir, "x"), 3); err == nil {
		t.Error("a file over the limit unpacked")
	}
}
