package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zakijss/minideck/cache"
)

// TestFindAndApplyUpdates: an app catalogue and a ports list served
// locally; installed items with an older version are listed (MiniMart
// itself last), the rest aren't, the catalogue is read fresh rather than
// from the cache, and an update installs the new version and keeps the
// user's files.
func TestFindAndApplyUpdates(t *testing.T) {
	sd := portCard(t)
	appZip := makeZip(t, map[string]string{
		"App/Rack/config.json": `{"label":"Rack","launch":"launch.sh"}`,
		"App/Rack/launch.sh":   "#!/bin/sh\nnew\n",
		"App/Rack/version.txt": "1.0.1\n",
	})
	selfZip := makeZip(t, map[string]string{"App/MiniMart/config.json": `{"label":"MiniMart","launch":"launch.sh"}`})
	portZ := portZip(t, [2]string{"Roms/PORTS/Games/G/g", "v2"}, [2]string{"Roms/PORTS/Shortcuts/G.port", "GameDir=\"G\"\nGameDataFile=\"g\"\n"})
	var srvURL string
	catalog := func(rackVersion string) []byte {
		b, _ := json.Marshal(map[string]any{"name": "Test", "apps": []map[string]any{
			{"name": "Rack", "folder": "Rack", "zip": srvURL + "/rack.zip", "sha256": sumOf(appZip), "version": rackVersion, "strip": "App/", "size": len(appZip)},
			{"name": "Same", "folder": "Same", "zip": srvURL + "/same.zip", "sha256": strings.Repeat("a", 64), "version": "2.0"},
			{"name": "Unknown", "folder": "Unknown", "zip": srvURL + "/u.zip", "sha256": strings.Repeat("a", 64), "version": "2.0"},
			{"name": "NotHere", "folder": "NotHere", "zip": srvURL + "/n.zip", "sha256": strings.Repeat("a", 64), "version": "2.0"},
			{"name": "MiniMart", "folder": "MiniMart", "zip": srvURL + "/self.zip", "sha256": sumOf(selfZip), "version": "1.1.0", "strip": "App/"},
		}})
		return b
	}
	rack := "1.0.0" // what the server lists now
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json":
			w.Write(catalog(rack))
		case "/ports.json":
			b, _ := json.Marshal(map[string]any{"ports": []Port{{Name: "G", Repo: "a/g", Version: "2", Zip: srvURL + "/g.zip", SHA256: sumOf(portZ), Root: "Roms/PORTS/",
				Games: []PortGame{{Shortcut: "G", Dir: "G", Data: "g"}}}}})
			w.Write(b)
		case "/rack.zip":
			w.Write(appZip)
		case "/self.zip":
			w.Write(selfZip)
		case "/g.zip":
			w.Write(portZ)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	src := []Source{{Name: "Test", Type: "apps", URL: srv.URL + "/catalog.json", Enabled: true}, {Name: "P", Type: "ports", URL: srv.URL + "/ports.json", Enabled: true}}

	inst := func(folder, version string) {
		d := filepath.Join(sd, "App", folder)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "config.json"), []byte("{}"), 0o644)
		if version != "" {
			os.WriteFile(filepath.Join(d, "version.txt"), []byte(version+"\n"), 0o644)
		}
	}
	inst("Rack", "1.0.0")
	os.WriteFile(filepath.Join(sd, "App/Rack/settings.json"), []byte("mine"), 0o644)
	inst("Same", "v2.0")
	inst("Unknown", "")
	inst("MiniMart", "1.0.0")
	savePortRecords(map[string]portRecord{"a/g#G": {Name: "G", Version: "1"}})

	// The first look caches the catalogue with Rack at 1.0.0 (no update).
	ups, errs := f.findUpdates(src)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := updateNames(ups); got != "Port G 1->2,Community app MiniMart 1.0.0->1.1.0" {
		t.Fatalf("first check: %s", got)
	}
	// A release comes out: the next check reads the catalogue again.
	rack = "1.0.1"
	ups, _ = f.findUpdates(src)
	if got := updateNames(ups); got != "Community app Rack 1.0.0->1.0.1,Port G 1->2,Community app MiniMart 1.0.0->1.1.0" {
		t.Fatalf("after the release: %s", got)
	}
	s := &store{f: f, src: src}
	for _, u := range ups[:2] {
		if v, err := s.apply(u, func(int64, int64) {}); err != nil || v != u.Available {
			t.Fatalf("%s: %q %v", u.Name, v, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(sd, "App/Rack/launch.sh")); string(b) != "#!/bin/sh\nnew\n" {
		t.Fatal("Rack not updated")
	}
	if b, _ := os.ReadFile(filepath.Join(sd, "App/Rack/settings.json")); string(b) != "mine" {
		t.Fatal("update lost the user's settings")
	}
	ups, _ = f.findUpdates(src)
	if got := updateNames(ups); got != "Community app MiniMart 1.0.0->1.1.0" || !ups[0].self() {
		t.Fatalf("after updating: %s", got)
	}
}

func updateNames(us []update) string {
	var out []string
	for _, u := range us {
		out = append(out, u.Kind+" "+u.Name+" "+u.Installed+"->"+u.Available)
	}
	return strings.Join(out, ",")
}

func TestSameVersion(t *testing.T) {
	for _, c := range [][2]string{{"1.0", "v1.0"}, {" V2.1 ", "2.1"}} {
		if !sameVersion(c[0], c[1]) {
			t.Errorf("%q vs %q", c[0], c[1])
		}
	}
	if sameVersion("1.0.0", "1.0.1") {
		t.Error("different versions equal")
	}
}

func TestUpdateNote(t *testing.T) {
	sd := portCard(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if it := updatesItem(loadNote(), now); it.Detail != "" || !strings.HasPrefix(it.Sub, "Check for updates") {
		t.Fatalf("never checked: %+v", it)
	}
	inst := func(folder, v string) {
		d := filepath.Join(sd, "App", folder)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "config.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(d, "version.txt"), []byte(v+"\n"), 0o644)
	}
	inst("MiniRack", "1.0.0")
	inst("MiniTape", "1.0.0")
	savePortRecords(map[string]portRecord{"a/g#G": {Version: "1"}})
	ups := []update{
		{Kind: "MiniDeck app", Name: "MiniRack", Available: "1.0.1", app: &App{Folder: "MiniRack"}},
		{Kind: "MiniDeck app", Name: "MiniTape", Available: "1.0.1", app: &App{Folder: "MiniTape"}},
		{Kind: "Port", Name: "G", Available: "2", port: &Port{Name: "G", Repo: "a/g"}},
	}
	if err := saveNote(ups, now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if it := updatesItem(loadNote(), now); it.Detail != "3 new" || it.Sub != "3 updates · checked 3 h ago" {
		t.Fatalf("three: %+v", it)
	}
	// MiniTape updated from its own page: the count drops without a check
	inst("MiniTape", "1.0.1")
	if it := updatesItem(loadNote(), now); it.Detail != "2 new" {
		t.Fatalf("two: %+v", it)
	}
	inst("MiniRack", "v1.0.1")
	savePortRecords(map[string]portRecord{"a/g#G": {Version: "2"}})
	if it := updatesItem(loadNote(), now); it.Detail != "" || it.Sub != "Up to date · checked 3 h ago" {
		t.Fatalf("none: %+v", it)
	}
}

// The background check runs at most once per checkEvery, keeps what it
// found, and keeps nothing when no repository answered.
func TestCheckInBackground(t *testing.T) {
	portCard(t)
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	s := &store{f: f, poke: make(chan struct{}, 1), src: []Source{{Name: "T", Type: "apps", URL: srv.URL + "/c.json", Enabled: true}}}
	now := time.Now()
	s.checkInBackground(now)
	waitPoke := func() bool {
		select {
		case <-s.poke:
			return true
		case <-time.After(3 * time.Second):
			return false
		}
	}
	if waitPoke() || !loadNote().Checked.IsZero() {
		t.Fatal("a check that reached nothing was kept")
	}
	// one that worked is kept, and the next start within a day doesn't check
	saveNote(nil, now.Add(-time.Hour))
	s.checkInBackground(now)
	if waitPoke() {
		t.Fatal("checked again within a day")
	}
	saveNote(nil, now.Add(-25*time.Hour))
	s.src = nil // nothing to read: an empty, successful check
	s.checkInBackground(now)
	if !waitPoke() || now.Sub(loadNote().Checked) > time.Minute+time.Since(now) {
		t.Fatal("a day-old check wasn't redone")
	}
}
