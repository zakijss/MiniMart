package main

import (
	"image"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/zakijss/minideck/cache"
	"github.com/zakijss/minideck/ui"
)

// TestScreens draws the home list and the Apps screens to PNG files for
// review. It runs only with MINIDECK_GALLERY=DIR set.
func TestScreens(t *testing.T) {
	dir := os.Getenv("MINIDECK_GALLERY")
	if dir == "" {
		t.Skip("set MINIDECK_GALLERY=DIR to draw the screens")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	pick := func(folder string) App {
		for _, a := range apps {
			if a.Folder == folder {
				a.Source = "Community apps"
				return a
			}
		}
		t.Fatalf("no app %s", folder)
		return App{}
	}
	own := []App{
		{Name: "MiniRack", Folder: "MiniRack", Desc: "Live server stats from Glances, Beszel or Vantage", Repo: "zakijss/MiniDeck",
			License: "MIT", Version: "1.0.0", Released: "2026-11-01", Size: 3100000, Source: "MiniDeck", Own: true,
			Facts: []string{"Runs only while open; passwords are encrypted with the device key."}},
		{Name: "MiniScale", Folder: "MiniScale", Desc: "Puts the device on your tailnet (unofficial Tailscale client)", Repo: "zakijss/MiniDeck",
			License: "MIT", Version: "1.0.0", Released: "2026-11-01", Size: 9800000, Source: "MiniDeck", Own: true,
			System: "Adds a boot script (.tmp_update/startup/miniscale.sh) that starts Tailscale at boot while it is switched on."},
	}
	list := append([]App{}, own...)
	for _, a := range apps {
		a.Source = "Community apps"
		list = append(list, a)
	}
	sortApps(list)
	// one app installed, so the list shows its version
	carrier := pick("Carrier")
	os.MkdirAll(carrier.dir(), 0o755)
	os.WriteFile(filepath.Join(carrier.dir(), "config.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(carrier.dir(), ".store-version"), []byte(carrier.Version+"\n"), 0o644)
	state := map[string]string{}
	for _, a := range list {
		state[a.Folder] = installedState(a)
	}

	// a README on the card for one app, as if it had been read before
	c := cache.New(t.TempDir())
	rm := pick("RomManager")
	c.Put("https://raw.githubusercontent.com/"+rm.Repo+"/"+readmeRef(rm)+"/README.md", []byte(sampleReadme))
	shot := func(name string, f func(*store)) {
		t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "store-"+name+".png"))
		scr, err := ui.Open()
		if err != nil {
			t.Fatal(err)
		}
		in := make(chan ui.Event)
		close(in) // screens that wait for input draw once and return
		f(&store{scr: scr, input: in, poke: make(chan struct{}, 1),
			f:      &fetcher{http: &http.Client{Transport: offlineTransport{}}, cache: c},
			thumbs: map[string]image.Image{}, loading: map[string]bool{}, sem: make(chan struct{}, 2)})
	}
	// an app that needs the user's own game file
	var port App
	for _, a := range apps {
		if a.NeedsFile != nil {
			port = a
			port.Source = "Community apps"
			break
		}
	}
	os.MkdirAll(filepath.Join(sd, "Roms", "N64"), 0o755)
	os.WriteFile(filepath.Join(sd, "Roms", "N64", "My game.z64"), make([]byte, 8<<20), 0o644)
	shot("home", func(s *store) { s.home() })
	shot("apps-1-warning", func(s *store) { s.warning(true) })
	shot("apps-2-list", func(s *store) {
		l := ui.List{}
		s.drawAppList(&l, list, state, appListSub(len(list), byName, ""), "", true, false)
	})
	shot("apps-3-list-scrolled", func(s *store) {
		l := ui.List{Sel: 5}
		s.drawAppList(&l, list, state, appListSub(len(list), byName, ""), "MiniDeck: no release yet", true, false)
	})
	shot("apps-3b-list-category", func(s *store) {
		media := append([]App{}, list...)
		sortAppsBy(media, byNewest, func(App) bool { return false })
		media = filterApps(media, "Media")
		l := ui.List{}
		s.drawAppList(&l, media, state, appListSub(len(media), byNewest, "Media"), "", true, false)
	})
	shot("apps-3c-list-by-category", func(s *store) {
		bycat := append([]App{}, list...)
		sortAppsBy(bycat, byCategory, func(App) bool { return false })
		l := ui.List{}
		s.drawAppList(&l, bycat, state, appListSub(len(bycat), byCategory, ""), "", true, true)
	})
	shot("apps-3d-sort-menu", func(s *store) {
		counts := map[string]int{}
		for _, a := range list {
			counts[categoryOf(a)]++
		}
		s.chooseAppView(byNewest, "", counts)
	})
	shot("apps-4-detail", func(s *store) { s.appDetail(pick("RomManager")) })
	shot("apps-4b-readme", func(s *store) {
		// the page scrolled down to its README
		w := ui.W - 56
		rs := readmeRows(markdownText(sampleReadme, readmeLimit), nil, true, w)
		s.textScreen(rm.Name, rm.Source, rs, "")
	})
	if port.NeedsFile != nil {
		shot("apps-4c-detail-port", func(s *store) { s.appDetail(port) })
		shot("apps-4d-game-file", func(s *store) {
			s.textScreen("Your copy of the game", port.Name, gameFileText(port, ui.W-56), "Pick a file")
		})
		shot("apps-4e-picker", func(s *store) { s.pickIn("Pick your copy", filepath.Join(sd, "Roms", "N64"), true, port.NeedsFile) })
	}
	shot("themes-sort-menu", func(s *store) { s.chooseOrder("Sort all themes", themeOrders, byNewest, true) })
	shot("apps-5-detail-installed", func(s *store) { s.appDetail(carrier) })
	shot("apps-6-confirm", func(s *store) { s.confirmInstall(pick("RomManager"), false) })
	shot("apps-7-confirm-plain", func(s *store) { s.confirmInstall(pick("PocketFeeds"), false) })
	shot("apps-8-detail-own", func(s *store) { s.appDetail(own[1]) })

	// Ports, from the list compiled in (the published one is offline here)
	ps, err := ParsePorts(builtinPorts, "MiyooMini Ports")
	if err != nil {
		t.Fatal(err)
	}
	portNamed := func(n string) Port {
		for _, p := range ps {
			if p.Name == n {
				return p
			}
		}
		t.Fatalf("no port %s", n)
		return Port{}
	}
	ut := portNamed("Undertale") // installed, waiting for the user's data.win
	recs := map[string]portRecord{ut.key(): {Name: ut.Name, Version: ut.Version, Shortcuts: shortcutsOf(ut)}}
	savePortRecords(recs)
	nf := filepath.Join(sd, "Roms/PORTS/Shortcuts", ut.Games[0].Shortcut+".notfound")
	os.MkdirAll(filepath.Dir(nf), 0o755)
	os.WriteFile(nf, []byte("x"), 0o644)
	withSrc := func(f func(*store)) func(*store) {
		return func(s *store) { s.src = defaultSources; f(s) }
	}
	shot("ports-1-list", withSrc(func(s *store) { s.ports() }))
	shot("ports-2-free", withSrc(func(s *store) { s.portDetail(portNamed("SkiFree")) }))
	shot("ports-3-owned-installed", withSrc(func(s *store) { s.portDetail(ut) }))
	shot("ports-4-owned-new", withSrc(func(s *store) { s.portDetail(portNamed("Fallout")) }))
	shot("ports-5-guide", withSrc(func(s *store) { s.portDetail(portNamed("Stardew Valley")) }))
	shot("ports-6-app", withSrc(func(s *store) { s.portDetail(portNamed("PocketStream")) }))
	shot("ports-7-confirm", withSrc(func(s *store) { s.installPort(portNamed("Captain Claw"), PortState{}) }))
}

// sampleReadme is a README with the parts the conversion handles.
const sampleReadme = `<h1 align="center">RomManager</h1>
<p align="center"><img src="docs/shot.png" alt="Screenshot"></p>

[![Release](https://img.shields.io/github/v/release/x/y)](https://github.com/x/y/releases)

> [!IMPORTANT]
> It ships no sources: you add your own.

## Install

1. Download the zip from [Releases](https://github.com/x/y/releases).
2. Unzip it onto the **root** of the card.

## Features

- Browse sources you add yourself
- Downloads to the right ` + "`Roms`" + ` folder
- Checks each update against GitHub's SHA-256

` + "```sh\nunzip -o RomManager.zip -d /Volumes/SDCARD\n```" + `
`
