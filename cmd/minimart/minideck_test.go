package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zakijss/minideck/cache"
)

func TestMinideckSourceBuiltin(t *testing.T) {
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	ss := LoadSources()
	if ss[0].Name != "MiniDeck" || ss[0].Type != "apps" || !ss[0].Builtin || !ss[0].Enabled ||
		ss[0].URL != "https://github.com/zakijss/MiniDeck/releases/latest/download/catalog.json" {
		t.Fatalf("first source: %+v", ss[0])
	}
	// turned off, it stays off after a reload
	ss[0].Enabled = false
	if err := SaveSources(ss); err != nil {
		t.Fatal(err)
	}
	if LoadSources()[0].Enabled {
		t.Fatal("minideck source turned itself back on")
	}
}

// catalogJSON is a catalogue as MiniDeck's tools/bundle writes it.
func catalogJSON(zipURL, sum string, size int) string {
	return fmt.Sprintf(`{"name": "minideck", "apps": [
  {"name": "MiniRack", "folder": "MiniRack", "desc": "Server stats", "repo": "zakijss/minideck",
   "license": "MIT", "zip": %q, "sha256": %q, "size": %d, "version": "0.2.0", "strip": "App/",
   "facts": ["Runs only while open."]},
  {"name": "MiniScale", "folder": "MiniScale", "desc": "Tailscale", "repo": "zakijss/minideck",
   "license": "MIT", "zip": %q, "sha256": %q, "size": %d, "version": "0.2.0", "strip": "App/",
   "system": "Adds a boot script.", "boot": ["miniscale.sh"]}]}`,
		zipURL, sum, size, zipURL, sum, size)
}

// offlineTransport fails every request.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

// The release address redirects (GitHub's latest/download does); a missing
// release is a 404 that leaves the other catalogues' apps and gives a calm
// note.
func TestMinideckCatalogRedirectAnd404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest/download/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/download/v0.2.0/catalog.json", http.StatusFound)
	})
	mux.HandleFunc("/releases/download/v0.2.0/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, catalogJSON("https://example.org/h.zip", strings.Repeat("a", 64), 10))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	src := Source{Name: "minideck", Type: "apps", URL: srv.URL + "/releases/latest/download/catalog.json", Enabled: true}
	list, errs := f.Apps([]Source{src})
	if len(errs) != 0 || len(list) != 2 || list[0].Source != "minideck" || list[1].Boot[0] != "miniscale.sh" {
		t.Fatalf("apps %+v errs %v", list, errs)
	}
	if list[0].Own {
		t.Error("a repository the user added counts as MiniDeck's own")
	}
	// the built-in MiniDeck source's apps are MiniDeck's own (read from the
	// cache here, with the network refresh failing)
	offline := &fetcher{http: &http.Client{Transport: offlineTransport{}}, cache: cache.New(t.TempDir())}
	offline.cache.Put(minideckCatalog, []byte(catalogJSON("https://example.org/h.zip", strings.Repeat("a", 64), 10)))
	list, errs = offline.Apps(defaultSources[:1])
	if len(errs) != 0 || len(list) != 2 || !list[0].Own || !list[1].Own {
		t.Fatalf("MiniDeck's own: %+v %v", list, errs)
	}

	missing := Source{Name: "minideck", Type: "apps", URL: srv.URL + "/nothing/catalog.json", Enabled: true}
	picks := Source{Name: "Community apps", Type: "apps", URL: builtinCatalog, Enabled: true}
	list, errs = f.Apps([]Source{missing, picks})
	if len(list) != len(apps) || len(errs) != 1 {
		t.Fatalf("with a 404: %d apps, errs %v", len(list), errs)
	}
	if got := sourcesNote(errs); got != "minideck: no release yet" {
		t.Errorf("note: %q", got)
	}
	if sourcesNote(nil) != "" {
		t.Error("note without errors")
	}
}

// Installing (or updating) a first-party app keeps the user's files in its
// folder, adds its boot script unless Tasks turned it off, and removing it
// takes the boot script too.
func TestInstallFirstPartyKeepsData(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	z := makeZip(t, map[string]string{
		"App/MiniScale/config.json":            `{"label":"MiniScale","launch":"launch.sh"}`,
		"App/MiniScale/launch.sh":              "#!/bin/sh\nnew\n",
		"App/MiniScale/miniscale.conf.example": "TS_HOSTNAME=miyoo\n",
		"App/MiniScale/version.txt":            "0.2.0\n",
		".tmp_update/startup/miniscale.sh":     "#!/bin/sh\nboot\n",
		"README.txt":                           "stray",
	})
	sum := sha256.Sum256(z)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(z) }))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	c, err := ParseCatalog([]byte(catalogJSON(srv.URL+"/m.zip", hex.EncodeToString(sum[:]), len(z))), "minideck")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Apps[1]

	dir := filepath.Join(sd, "App", "MiniScale")
	os.MkdirAll(dir, 0755)
	user := map[string]string{
		"miniscale.conf": "TS_HOSTNAME=mine\n", "forwards.conf": "8096 nas:8096\n",
		"state.enc": "sealed", "autostart": "", "launch.sh": "#!/bin/sh\nold\n",
	}
	for n, c := range user {
		os.WriteFile(filepath.Join(dir, n), []byte(c), 0644)
	}
	if a.InstalledVersion() != "" {
		t.Fatal("version before install")
	}
	v, err := f.InstallApp(a, func(int64, int64) {})
	if err != nil || v != "0.2.0" {
		t.Fatalf("install: %v %v", v, err)
	}
	for n, want := range user {
		if n == "launch.sh" {
			want = "#!/bin/sh\nnew\n" // shipped files are updated
		}
		if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != want {
			t.Errorf("%s: %q, want %q", n, b, want)
		}
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, "*.store-new")); len(ents) != 0 {
		t.Errorf("temporary files left: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(sd, "README.txt")); err == nil {
		t.Error("stray file unpacked")
	}
	hook := filepath.Join(sd, ".tmp_update", "startup", "miniscale.sh")
	if b, _ := os.ReadFile(hook); string(b) != "#!/bin/sh\nboot\n" {
		t.Fatalf("boot script: %q", b)
	}

	// turned off in MiniTop: an update leaves it off
	os.Rename(hook, hook+".off")
	if _, err := f.InstallApp(a, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hook); err == nil {
		t.Error("update turned the boot script back on")
	}

	// a version.txt from a hand-unzipped release counts as the version
	os.Remove(filepath.Join(dir, ".store-version"))
	if v := a.InstalledVersion(); v != "0.2.0" {
		t.Errorf("version.txt: %q", v)
	}

	if err := RemoveApp(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hook + ".off"); err == nil {
		t.Error("boot script left after removing")
	}

	// unsafe boot names never make it into the list
	if c, _ := ParseCatalog([]byte(strings.Replace(catalogJSON("https://x/z.zip", strings.Repeat("a", 64), 1),
		`"miniscale.sh"`, `"../../etc/profile"`, 1)), "x"); len(c.Apps) != 1 {
		t.Errorf("unsafe boot script accepted: %d apps", len(c.Apps))
	}
}

// A download that stops arriving is given up after stallTimeout, however
// long the whole download may take.
func TestDownloadStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = 300 * time.Millisecond
	defer func() { stallTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	start := time.Now()
	err := f.Download(srv.URL+"/big.zip", filepath.Join(t.TempDir(), "big.zip"), func(int64, int64) {})
	if err != errStalled || time.Since(start) > 3*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

// TestReleaseCatalog checks the catalogue MiniDeck publishes, when
// MINIDECK_DIST names a folder holding catalog.json and the apps' zips (as
// MiniDeck's tools/bundle leaves them): every app must survive ParseCatalog
// (the store drops entries it can't install), and must install from its
// zip, checksum and all, onto an empty card, with its boot scripts.
func TestReleaseCatalog(t *testing.T) {
	dist := os.Getenv("MINIDECK_DIST")
	if dist == "" {
		t.Skip("set MINIDECK_DIST=dist to check a release")
	}
	b, err := os.ReadFile(filepath.Join(dist, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Version string
		Apps    []map[string]any
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(b, "minideck")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) == 0 || len(c.Apps) != len(raw.Apps) {
		t.Fatalf("%d of %d apps kept", len(c.Apps), len(raw.Apps))
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dist)))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	for _, a := range c.Apps {
		if a.License != "MIT" || a.Version == "" || a.Strip != "App/" || a.Size <= 0 ||
			!strings.HasPrefix(a.Zip, "https://github.com/"+a.Repo+"/releases/download/v"+a.Version+"/") ||
			len(a.Facts) == 0 || a.Desc == "" || !strings.HasPrefix(a.Repo, "zakijss/") {
			t.Errorf("%s: %+v", a.Name, a)
		}
		if (len(a.Boot) > 0) != (a.System != "") {
			t.Errorf("%s: boot scripts and the system note go together", a.Name)
		}
		local := a
		local.Zip = srv.URL + "/" + path.Base(a.Zip)
		v, err := f.InstallApp(local, func(int64, int64) {})
		if err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		if v != a.Version || local.InstalledVersion() != a.Version {
			t.Errorf("%s: installed %q, reads %q", a.Name, v, local.InstalledVersion())
		}
		for _, n := range []string{"config.json", "launch.sh", "icon.png", "version.txt"} {
			if _, err := os.Stat(filepath.Join(sd, "App", a.Folder, n)); err != nil {
				t.Errorf("%s: %v", a.Name, err)
			}
		}
		for _, n := range a.Boot {
			if _, err := os.Stat(filepath.Join(sd, ".tmp_update", "startup", n)); err != nil {
				t.Errorf("%s: boot script: %v", a.Name, err)
			}
		}
	}
	t.Logf("%d apps checked from %s", len(c.Apps), dist)
}
