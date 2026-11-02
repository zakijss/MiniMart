package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zakijss/minideck/cache"
	"github.com/zakijss/minideck/ui"
)

func names(list []App) string {
	var n []string
	for _, a := range list {
		n = append(n, a.Name)
	}
	return strings.Join(n, ",")
}

func TestSortAndConfirm(t *testing.T) {
	list := []App{
		{Name: "zeta"},
		{Name: "MiniMart", Own: true},
		{Name: "alpha"},
		{Name: "Beta"},
		{Name: "MiniRack", Own: true},
	}
	sortApps(list)
	if got := names(list); got != "MiniMart,MiniRack,alpha,Beta,zeta" {
		t.Fatalf("sorted: %s", got)
	}
	// every community app asks; MiniDeck's own only when it changes files
	// outside its folder
	if !needsConfirm(App{}) || !needsConfirm(App{License: "MIT"}) || needsConfirm(App{Own: true}) ||
		!needsConfirm(App{Own: true, System: "Adds a boot script."}) {
		t.Error("needsConfirm")
	}
}

// A self-update is said to be outside the store's checks, and a change
// outside the folder to survive removing the app here.
func TestAttentionText(t *testing.T) {
	a := App{Folder: "X", Updates: "Its menu downloads new versions.", System: "Adds a boot hook."}
	if got := updatesText(a); got != "Its menu downloads new versions. The store doesn't check those updates." {
		t.Errorf("updates: %q", got)
	}
	if got := systemText(a); got != "Adds a boot hook. Removing it here deletes only App/X." {
		t.Errorf("system: %q", got)
	}
	if !(App{}).noLicence() || !(App{License: " none "}).noLicence() || (App{License: "MIT"}).noLicence() {
		t.Error("noLicence")
	}
}

func TestListLine(t *testing.T) {
	a := App{Name: "Carrier", Desc: "qBittorrent remote.", License: "MIT", Released: "2026-10-09", Source: "Community apps"}
	if got := listLine(a); got != "MIT · Oct 2026 · qBittorrent remote." {
		t.Errorf("plain: %q", got)
	}
	a.License, a.Updates, a.System, a.Source = "", "x", "y", "Mine"
	if got := listLine(a); got != "Mine · No licence · Oct 2026 · Updates itself · Writes outside its folder · qBittorrent remote." {
		t.Errorf("flagged: %q", got)
	}
	if got := listLine(App{Desc: "Stats.", Own: true, License: "MIT"}); got != "MiniDeck's own app · Stats." {
		t.Errorf("own: %q", got)
	}
	if releaseDate("2026-10-09") != "9 Oct 2026" || releaseDate("soon") != "" {
		t.Error("releaseDate")
	}
	if versionLabel("1.2") != "v1.2" || versionLabel("commit a1b2c3d") != "commit a1b2c3d" || versionLabel("") != "" {
		t.Error("versionLabel")
	}
}

// A word too long for its line is broken after a '/' rather than drawn
// past the edge.
func TestWrapLongWord(t *testing.T) {
	text := "github.com/snyderman3000/Carrier-onionOS"
	w := ui.Measure(ui.Small, "github.com/snyderman3000/")
	got := wrap(ui.Small, text, w)
	if strings.Join(got, "") != text || len(got) < 2 || got[0] != "github.com/snyderman3000/" {
		t.Fatalf("wrapped: %q", got)
	}
	for _, l := range got {
		if ui.Measure(ui.Small, l) > w {
			t.Errorf("%q is wider than %d", l, w)
		}
	}
	if got := wrap(ui.Small, "a b", 1000); strings.Join(got, "|") != "a b" {
		t.Errorf("short text: %q", got)
	}
}

// The warning is acknowledged once per version of its text, in a small file
// in the store's folder.
func TestWarningSeen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINIDECK_APPDIR", dir)
	if warningSeen() {
		t.Fatal("seen before it was shown")
	}
	if err := rememberWarning(); err != nil {
		t.Fatal(err)
	}
	if !warningSeen() {
		t.Fatal("not remembered")
	}
	// a new version of the text is shown again
	os.WriteFile(filepath.Join(dir, "apps-warning-seen"), []byte("0\n"), 0644)
	if warningSeen() {
		t.Error("an older acknowledgement counted")
	}
}

// Older catalogues still parse: a "grade" is ignored and "why" is read as
// facts; empty lines are dropped.
func TestCatalogFactsAndOldFormat(t *testing.T) {
	sum := strings.Repeat("a", 64)
	c, err := ParseCatalog([]byte(`{"apps":[
		{"name":"Old","folder":"Old","zip":"https://x/o.zip","sha256":"`+sum+`"},
		{"name":"Graded","folder":"Graded","zip":"https://x/n.zip","sha256":"`+sum+`","grade":"b","why":["  needs ffplay ", ""],"wrap":true,"size":10},
		{"name":"New","folder":"New","zip":"https://x/n.zip","sha256":"`+sum+`","released":"2026-10-09","updates":"Its menu updates it.",
		 "account":"An API key.","needs":[" Wi-Fi ",""],"facts":["Tested on Onion 4.4."],"why":["Old reason."]},
		{"name":"Both","folder":"Both","zip":"https://x/b.zip","sha256":"`+sum+`","wrap":true,"strip":"App/"},
		{"name":"Up","folder":"Up","zip":"https://x/u.zip","sha256":"`+sum+`","strip":"../"},
		{"name":"Sneaky","folder":"Sneaky","zip":"https://x/s.zip","sha256":"`+sum+`","secure":"../../etc/x"}]}`), "Mine")
	if err != nil || names(c.Apps) != "Old,Graded,New,Both" {
		t.Fatalf("parsed: %s %v", names(c.Apps), err)
	}
	g := c.Apps[1]
	if len(g.Facts) != 1 || g.Facts[0] != "needs ffplay" || g.Why != nil || !g.Wrap || g.Size != 10 {
		t.Errorf("old fields: %+v", g)
	}
	n := c.Apps[2]
	if n.Released != "2026-10-09" || n.Updates == "" || n.Account == "" || strings.Join(n.Needs, "|") != "Wi-Fi" ||
		strings.Join(n.Facts, "|") != "Tested on Onion 4.4.|Old reason." || n.Own {
		t.Errorf("new fields: %+v", n)
	}
}

func TestValidSecure(t *testing.T) {
	for _, ok := range []string{"session.txt", "data/config.json", "data.db", "a-b_c/d.e/f", "data/settings.cfg"} {
		if err := validSecure(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "..", "../x", "data/../x", "data/..", "/etc/passwd", "/data/x", ".secret",
		"data/.x", `data\x`, "a b", "a;b", "a$b", "a'b", `a"b`, "a//b", "data/", "./x", "a\nb", "a#b", "a&b"} {
		if validSecure(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUnzipWrapped(t *testing.T) {
	d := t.TempDir()
	zp := filepath.Join(d, "htop.zip")
	os.WriteFile(zp, makeZip(t, map[string]string{
		"config.json":            "{}",
		"launch.sh":              "#!/bin/sh\n",
		"terminfo/x/xterm":       "ti",
		"__MACOSX/._config.json": "junk",
		".DS_Store":              "junk",
		"terminfo/._xterm":       "junk",
	}), 0644)
	out := filepath.Join(d, "App")
	if err := unzipWrapped(zp, out, "Htop"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Htop/config.json", "Htop/launch.sh", "Htop/terminfo/x/xterm"} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	for _, junk := range []string{"Htop/__MACOSX", "Htop/.DS_Store", "Htop/terminfo/._xterm", "config.json", "__MACOSX"} {
		if _, err := os.Stat(filepath.Join(out, junk)); err == nil {
			t.Errorf("%s unpacked", junk)
		}
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 {
		t.Errorf("only Htop should be in App, got %d entries", len(ents))
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if unzipWrapped(zp, out, bad) == nil {
			t.Errorf("folder %q accepted", bad)
		}
	}
}

// A zip slip refuses the whole zip: nothing is written, not even the safe
// entries before it.
func TestUnzipZipSlipRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		wrap  bool
		strip string
	}{
		{"wrap ..", map[string]string{"config.json": "{}", "../escape.txt": "x"}, true, ""},
		{"wrap nested ..", map[string]string{"config.json": "{}", "bin/../../escape.txt": "x"}, true, ""},
		{"wrap absolute", map[string]string{"config.json": "{}", "/escape.txt": "x"}, true, ""},
		{"root folder ..", map[string]string{"Foo/config.json": "{}", "Foo/../escape.txt": "x"}, false, ""},
		{"strip ..", map[string]string{"App/Foo/config.json": "{}", "App/Foo/../../escape.txt": "x"}, false, "App/"},
	} {
		d := t.TempDir()
		zp := filepath.Join(d, "evil.zip")
		os.WriteFile(zp, makeZip(t, tc.files), 0644)
		out := filepath.Join(d, "sd", "App")
		os.MkdirAll(out, 0755)
		var err error
		switch {
		case tc.wrap:
			err = unzipWrapped(zp, out, "Foo")
		case tc.strip != "":
			err = unzipApp(zp, out, tc.strip, "Foo")
		default:
			err = extractApp(zp, out, "", "Foo", false)
		}
		if err == nil {
			t.Errorf("%s: zip slip not refused", tc.name)
		}
		filepath.Walk(filepath.Join(d, "sd"), func(p string, fi os.FileInfo, _ error) error {
			if fi != nil && !fi.IsDir() {
				t.Errorf("%s: %s written", tc.name, p)
			}
			return nil
		})
		if _, err := os.Stat(filepath.Join(d, "escape.txt")); err == nil {
			t.Errorf("%s: escaped", tc.name)
		}
	}
}

// Zips with <Folder>/ at their root (no strip) install only that folder:
// no stray files or __MACOSX in App/.
func TestRootFolderOnly(t *testing.T) {
	d := t.TempDir()
	zp := filepath.Join(d, "a.zip")
	os.WriteFile(zp, makeZip(t, map[string]string{
		"DotCleanPlus/config.json":            "{}",
		"DotCleanPlus/launch.sh":              "#!/bin/sh\n",
		"usage.txt":                           "stray",
		"__MACOSX/DotCleanPlus/._config.json": "junk",
		"Other/x":                             "other",
	}), 0644)
	out := filepath.Join(d, "App")
	if err := extractApp(zp, out, "", "DotCleanPlus", false); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 || ents[0].Name() != "DotCleanPlus" {
		t.Fatalf("App has %v", ents)
	}
	if _, err := os.Stat(filepath.Join(out, "DotCleanPlus/launch.sh")); err != nil {
		t.Fatal("launch.sh missing")
	}
}

// A wrapped zip with its key in a sub-folder: installed into App/<Folder>,
// with the wrapper and boot hook pointing at the relative path.
func TestInstallWrappedWithSecureSubpath(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	os.MkdirAll(filepath.Join(sd, "App"), 0755)
	z := makeZip(t, map[string]string{
		"config.json": "{\n  \"label\": \"Thing\",\n  \"launch\": \"launch.sh\"\n}\n",
		"launch.sh":   "#!/bin/sh\n./thing\n",
		"README.txt":  "hi",
	})
	sum := sha256.Sum256(z)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(z) }))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	a := App{Name: "Thing", Folder: "Thing", Zip: srv.URL + "/thing.zip", SHA256: hex.EncodeToString(sum[:]),
		Version: "1.0", Wrap: true, Secure: "data/config.json"}
	if _, err := f.InstallApp(a, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"App/Thing/config.json", "App/Thing/README.txt", "App/Thing/secure-launch.sh"} {
		if _, err := os.Stat(filepath.Join(sd, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	w, _ := os.ReadFile(filepath.Join(sd, "App/Thing/secure-launch.sh"))
	if !strings.Contains(string(w), "S='data/config.json'\n") || !strings.Contains(string(w), `[ ! -f "$S" ]`) {
		t.Errorf("wrapper: %s", w)
	}
	h, _ := os.ReadFile(filepath.Join(sd, ".tmp_update/startup/thing-secure.sh"))
	if !strings.Contains(string(h), "S='data/config.json'\n") || !strings.Contains(string(h), `[ -f "$A/$S" ]`) {
		t.Errorf("hook: %s", h)
	}
	// a bad secure path is refused before anything is unpacked
	os.RemoveAll(filepath.Join(sd, "App/Thing"))
	a.Secure = "../../.tmp_update/x"
	if _, err := f.InstallApp(a, func(int64, int64) {}); err == nil {
		t.Fatal("bad secure path accepted")
	}
	if _, err := os.Stat(filepath.Join(sd, "App/Thing")); err == nil {
		t.Error("unpacked despite a bad secure path")
	}
	// a bad strip prefix is refused
	a.Secure, a.Strip = "", "../"
	if _, err := f.InstallApp(a, func(int64, int64) {}); err == nil {
		t.Fatal("bad strip accepted")
	}
}

// With wrap and strip, the files under the prefix (a commit archive's
// folder, say) go into App/<Folder>, whatever the folder there is called.
func TestUnpackWrapWithStrip(t *testing.T) {
	d := t.TempDir()
	zp := filepath.Join(d, "a.zip")
	os.WriteFile(zp, makeZip(t, map[string]string{
		"repo-0123abc/App/config.json": "{}",
		"repo-0123abc/App/launch.sh":   "#!/bin/sh\n",
		"repo-0123abc/App/bin/tool":    "x",
		"repo-0123abc/README.md":       "stray",
		"repo-0123abc/App/._launch.sh": "junk",
		"other/App/config.json":        "other",
	}), 0644)
	root := filepath.Join(d, "App")
	a := App{Folder: "CueMaker", Wrap: true, Strip: "repo-0123abc/App/"}
	if err := unpackApp(a, zp, root); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CueMaker/config.json", "CueMaker/launch.sh", "CueMaker/bin/tool"} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "CueMaker")); len(ents) != 3 {
		t.Errorf("CueMaker has %d entries, want 3", len(ents))
	}
	if ents, _ := os.ReadDir(root); len(ents) != 1 {
		t.Errorf("App has %d entries", len(ents))
	}
	a.Strip = "nothing/"
	if err := unpackApp(a, zp, filepath.Join(d, "App2")); err == nil {
		t.Error("an empty prefix accepted")
	}
	a.Strip = "../"
	if err := unpackApp(a, zp, filepath.Join(d, "App3")); err == nil {
		t.Error("a bad prefix accepted")
	}
}
