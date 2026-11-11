package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zakijss/minideck/cache"
	"github.com/zakijss/minideck/ui"
)

func TestItemNames(t *testing.T) {
	repo := "https://raw.githubusercontent.com/OnionUI/Themes/main/"
	it := Item{Base: repo, Name: "Pixel by Jeltron", Icons: true}
	if it.Title() != "Pixel" || it.Author() != "Jeltron" {
		t.Fatal(it.Title(), it.Author())
	}
	if got := it.ZipURL(); got != repo+"release/icons/Pixel%20by%20Jeltron.zip" {
		t.Fatal(got)
	}
	if (Item{Name: "Hippo Stock"}).Author() != "" {
		t.Fatal("no author expected")
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	p := filepath.Join(t.TempDir(), "z.zip")
	f, _ := os.Create(p)
	w := zip.NewWriter(f)
	for n, c := range files {
		fw, _ := w.Create(n)
		fw.Write([]byte(c))
	}
	w.Close()
	f.Close()
	b, _ := os.ReadFile(p)
	return b
}

func TestInstallAndRemove(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	os.MkdirAll(filepath.Join(sd, "Themes"), 0755)
	os.MkdirAll(filepath.Join(sd, "Icons"), 0755)
	os.MkdirAll(filepath.Join(sd, ".tmp_update/config"), 0755)
	themeZip := makeZip(t, map[string]string{"Hippo Stock/config.json": "{}"})
	iconZip := makeZip(t, map[string]string{"Pixel by Jeltron/gb.png": "png"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release/Hippo Stock.zip":
			w.Write(themeZip)
		case "/release/icons/Pixel by Jeltron.zip":
			w.Write(iconZip)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}

	theme := Item{Name: "Hippo Stock"}
	if err := f.Download(srv.URL+"/release/Hippo%20Stock.zip", filepath.Join(sd, "Themes", theme.Name+".zip"), func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if !theme.Installed() {
		t.Fatal("theme not installed")
	}
	icons := Item{Name: "Pixel by Jeltron", Icons: true}
	tmp := filepath.Join(sd, "Icons", ".p.zip")
	if err := f.Download(srv.URL+"/release/icons/Pixel%20by%20Jeltron.zip", tmp, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if err := Unzip(tmp, filepath.Join(sd, "Icons")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sd, "Icons", "Pixel by Jeltron", "gb.png")); err != nil || !icons.Installed() {
		t.Fatal("icon pack not unpacked")
	}
	// the theme in use can't be removed
	os.WriteFile(filepath.Join(sd, ".tmp_update/config/active_theme"), []byte("/mnt/SDCARD/Themes/Hippo Stock/"), 0644)
	if Remove(theme) == nil {
		t.Fatal("removed the active theme")
	}
	os.WriteFile(filepath.Join(sd, ".tmp_update/config/active_theme"), []byte("/mnt/SDCARD/Themes/Silky by DiMo/"), 0644)
	if err := Remove(theme); err != nil || theme.Installed() {
		t.Fatal("not removed", err)
	}
	// a missing download fails cleanly and leaves nothing behind
	dst := filepath.Join(sd, "Themes", "Nope.zip")
	if f.Download(srv.URL+"/release/Nope.zip", dst, func(int64, int64) {}) == nil {
		t.Fatal("404 accepted")
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Fatal("left a .part file")
	}
}

func TestUnzipRefusesEscapes(t *testing.T) {
	z := filepath.Join(t.TempDir(), "evil.zip")
	os.WriteFile(z, makeZip(t, map[string]string{"../../escape.txt": "x"}), 0644)
	if Unzip(z, t.TempDir()) == nil {
		t.Fatal("zip slip not refused")
	}
}

func TestInstallAppChecksAndSecures(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	os.MkdirAll(filepath.Join(sd, "App"), 0755)
	z := makeZip(t, map[string]string{
		"MiyooFin/config.json": "{\n    \"label\": \"MiyooFin\",\n    \"launch\": \"./launch.sh\"\n}\n",
		"MiyooFin/launch.sh":   "#!/bin/sh\n./miyoofin\n",
	})
	sum := sha256.Sum256(z)
	good := hex.EncodeToString(sum[:])
	want := good
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprintf(w, `{"version":"0.3.2","assets":{"zip":{"url":"%s/MiyooFin.zip","sha256":"%s","size":%d}}}`, srvURL, want, len(z))
		case "/MiyooFin.zip":
			w.Write(z)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	var a App
	for _, b := range apps {
		if b.Folder == "MiyooFin" {
			a = b
		}
	}
	a.Manifest = srv.URL + "/manifest.json"

	// a wrong checksum: nothing installed
	want = strings.Repeat("0", 64)
	if _, err := f.InstallApp(a, func(int64, int64) {}); err == nil || a.Installed() {
		t.Fatalf("checksum mismatch accepted: %v", err)
	}
	want = good
	v, err := f.InstallApp(a, func(int64, int64) {})
	if err != nil || v != "0.3.2" || a.InstalledVersion() != "0.3.2" {
		t.Fatalf("install: %v %q", err, v)
	}
	cfg, _ := os.ReadFile(filepath.Join(sd, "App/MiyooFin/config.json"))
	if !strings.Contains(string(cfg), `"launch": "./secure-launch.sh"`) {
		t.Fatalf("config not pointed at the wrapper: %s", cfg)
	}
	w, _ := os.ReadFile(filepath.Join(sd, "App/MiyooFin/secure-launch.sh"))
	if !strings.Contains(string(w), "S='session.txt'") {
		t.Fatal("wrapper not written")
	}
	if _, err := os.Stat(filepath.Join(sd, ".tmp_update/startup/miyoofin-secure.sh")); err != nil {
		t.Fatal("boot hook not written")
	}
	if err := RemoveApp(a); err != nil || a.Installed() {
		t.Fatal("not removed")
	}
	if _, err := os.Stat(filepath.Join(sd, ".tmp_update/startup/miyoofin-secure.sh")); err == nil {
		t.Fatal("boot hook left behind")
	}
}

func TestSourcesAndCatalog(t *testing.T) {
	for _, tc := range []struct{ typ, in, want string }{
		{"themes", "someone/Themes", "https://raw.githubusercontent.com/someone/Themes/main/"},
		{"apps", "https://github.com/o/r/blob/main/apps.json", "https://raw.githubusercontent.com/o/r/main/apps.json"},
		{"apps", "example.org/apps.json", "https://example.org/apps.json"},
	} {
		if got, err := NormalizeURL(tc.typ, tc.in); err != nil || got != tc.want {
			t.Errorf("NormalizeURL(%q) = %q, %v", tc.in, got, err)
		}
	}
	if _, err := NormalizeURL("apps", "http://example.org/apps.json"); err == nil {
		t.Error("plain http accepted")
	}
	c, err := ParseCatalog([]byte(`{"name":"Mine","apps":[
		{"name":"Good","folder":"Good","zip":"https://x/good.zip","sha256":"`+strings.Repeat("a", 64)+`"},
		{"name":"NoSum","folder":"NoSum","zip":"https://x/nosum.zip"},
		{"name":"Escape","folder":"../etc","zip":"https://x/e.zip","sha256":"`+strings.Repeat("b", 64)+`"}]}`), "Mine")
	if err != nil || len(c.Apps) != 1 || c.Apps[0].Name != "Good" || c.Apps[0].Source != "Mine" {
		t.Fatalf("catalogue: %+v %v", c, err)
	}
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	ss := LoadSources()
	if len(ss) != len(defaultSources) {
		t.Fatal("defaults missing")
	}
	ss[0].Enabled = false
	ss = append(ss, Source{Name: "Mine", Type: "apps", URL: "https://x/apps.json", Enabled: true})
	if err := SaveSources(ss); err != nil {
		t.Fatal(err)
	}
	back := LoadSources()
	if back[0].Enabled || len(back) != len(defaultSources)+1 || back[len(back)-1].Name != "Mine" {
		t.Fatalf("reloaded: %+v", back)
	}
}

// Plain http is allowed by the host alone: this device or a tailnet
// address, wherever else the URL mentions one.
func TestPlainHTTPHost(t *testing.T) {
	for _, ok := range []string{
		"http://127.0.0.1:8080/c.json", "http://localhost/c.json", "http://LOCALHOST./c.json",
		"http://100.101.1.2/c.json", "http://100.64.0.1/", "http://100.127.255.254/",
		"http://nas.tail1234.ts.net/c.json", "http://[::1]/c.json", "http://[fd7a:115c:a1e0::1]/c.json",
		"https://example.org/c.json",
	} {
		if _, err := NormalizeURL("apps", ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://evil.example/cat.json?x=.ts.net", "http://127.0.0.1.evil.example/c.json",
		"http://evil.example/#://127.0.0.1", "http://evil.example/.ts.net/c.json",
		"http://100.128.0.1/c.json", "http://100.63.255.255/", "http://nas/c.json", "http://ts.net/c.json",
		"http://x.ts.net.evil.example/", "HTTP://evil.example/c.json", "ftp://example.org/c.json",
		"http://user@evil.example/?@127.0.0.1",
	} {
		if _, err := NormalizeURL("apps", bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// The theme in use is matched by its exact folder: "Pixel" isn't in use
// because "Pixel by Jeltron" is.
func TestActiveExactName(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	cfg := filepath.Join(sd, ".tmp_update/config")
	os.MkdirAll(cfg, 0755)
	os.MkdirAll(filepath.Join(sd, "Themes", "Pixel"), 0755)
	os.WriteFile(filepath.Join(cfg, "active_theme"), []byte("/mnt/SDCARD/Themes/Pixel by Jeltron/\n"), 0644)
	os.WriteFile(filepath.Join(cfg, "active_icon_pack"), []byte("/mnt/SDCARD/Icons/Pixel by Jeltron"), 0644)
	if !(Item{Name: "Pixel by Jeltron"}).Active() || !(Item{Name: "Pixel by Jeltron", Icons: true}).Active() {
		t.Error("the theme or icon pack in use isn't found")
	}
	for _, n := range []string{"Pixel", "by Jeltron", "Jeltron", "Pixel by"} {
		if (Item{Name: n}).Active() || (Item{Name: n, Icons: true}).Active() {
			t.Errorf("%q taken for the one in use", n)
		}
	}
	if err := Remove(Item{Name: "Pixel"}); err != nil || (Item{Name: "Pixel"}).Installed() {
		t.Errorf("Pixel not removed: %v", err)
	}
	os.WriteFile(filepath.Join(cfg, "active_theme"), nil, 0644)
	if (Item{Name: "Themes"}).Active() || activeName(false) != "" {
		t.Error("an empty active_theme names a theme")
	}
}

// Icon packs are checked a few at a time, and a "no download" answer is
// asked again once it is a day old.
func TestIconPacksParallelAndExpiry(t *testing.T) {
	var names []string
	for i := range 12 {
		names = append(names, fmt.Sprintf("Pack %d", i))
	}
	var mu sync.Mutex
	have := map[string]bool{}
	for i, n := range names {
		have["/release/icons/"+n+".zip"] = i%2 == 0
	}
	var heads, inFlight, maxInFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.github/data/icons.txt" {
			fmt.Fprint(w, strings.Join(names, "\n"))
			return
		}
		heads.Add(1)
		n := inFlight.Add(1)
		for m := maxInFlight.Load(); n > m && !maxInFlight.CompareAndSwap(m, n); m = maxInFlight.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		mu.Lock()
		ok := have[r.URL.Path]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	base := srv.URL + "/"
	packs, err := f.iconPacks(base)
	if err != nil || len(packs) != 6 {
		t.Fatalf("%d packs, %v", len(packs), err)
	}
	if m := maxInFlight.Load(); m < 2 || m > headWorkers {
		t.Errorf("%d checks at once, want 2 to %d", m, headWorkers)
	}
	if heads.Load() != 12 {
		t.Errorf("%d checks, want 12", heads.Load())
	}
	// cached: no checks
	heads.Store(0)
	if packs, _ := f.iconPacks(base); len(packs) != 6 || heads.Load() != 0 {
		t.Errorf("second load: %d packs, %d checks", len(packs), heads.Load())
	}
	// every pack has a zip now; the "no" answers are made two days old, and
	// one is in the form kept before answers had a time
	mu.Lock()
	for k := range have {
		have[k] = true
	}
	mu.Unlock()
	old := fmt.Sprintf("0 %d", time.Now().Add(-48*time.Hour).Unix())
	for i, n := range names {
		u := (Item{Base: base, Name: n, Icons: true}).ZipURL()
		switch {
		case i == 1:
			f.cache.Put("exists|"+u, []byte("0"))
		case i%2 == 1:
			f.cache.Put("exists|"+u, []byte(old))
		}
	}
	heads.Store(0)
	if packs, _ := f.iconPacks(base); len(packs) != 12 || heads.Load() != 6 {
		t.Errorf("after expiry: %d packs, %d checks", len(packs), heads.Load())
	}
}

// The browse rows show the state they were built with; they are built once
// per list (and for one item after its details), not per frame.
func TestBrowseRowAndAppState(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	os.MkdirAll(filepath.Join(sd, "Themes"), 0755)
	os.WriteFile(filepath.Join(sd, "Themes", "Hippo Stock.zip"), []byte("zip"), 0644)
	s := &store{}
	for _, tc := range []struct {
		it     Item
		active string
		want   string
	}{
		{Item{Name: "Hippo Stock"}, "", "Installed"},
		{Item{Name: "Hippo Stock"}, "Hippo Stock", "In use"},
		{Item{Name: "Silky by DiMo"}, "Hippo Stock", ""},
	} {
		if r := s.browseRow(tc.it, tc.active); r.Detail != tc.want || r.Label != tc.it.Title() {
			t.Errorf("%s (active %q): %q", tc.it.Name, tc.active, r.Detail)
		}
	}
	a := App{Folder: "Thing"}
	if installedState(a) != "" {
		t.Error("not installed, but has a state")
	}
	os.MkdirAll(a.dir(), 0755)
	os.WriteFile(filepath.Join(a.dir(), "config.json"), []byte("{}"), 0644)
	if installedState(a) != "Installed" {
		t.Error(installedState(a))
	}
	os.WriteFile(filepath.Join(a.dir(), ".store-version"), []byte("1.2\n"), 0644)
	if installedState(a) != "v1.2" {
		t.Error(installedState(a))
	}
}

func TestHandleJumps(t *testing.T) {
	l := ui.List{Items: make([]ui.Item, 10), Sel: 4}
	handle(&l, ui.Event{Button: ui.R2, Pressed: true})
	if l.Sel != 9 {
		t.Errorf("R2: Sel %d, want 9", l.Sel)
	}
	handle(&l, ui.Event{Button: ui.L2, Pressed: true})
	if l.Sel != 0 {
		t.Errorf("L2: Sel %d, want 0", l.Sel)
	}
	handle(&l, ui.Event{Button: ui.Right, Pressed: true})
	if l.Sel != 6 {
		t.Errorf("Right: Sel %d, want 6", l.Sel)
	}
	empty := ui.List{}
	handle(&empty, ui.Event{Button: ui.R2, Pressed: true})
	if empty.Sel != 0 {
		t.Errorf("R2 on an empty list: Sel %d", empty.Sel)
	}
}
