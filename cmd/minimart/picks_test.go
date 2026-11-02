package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
)

// archiveRe is a GitHub archive of one commit, for a project with no
// release to pin: https://github.com/<repo>/archive/<full sha>.zip.
var archiveRe = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/archive/[0-9a-f]{40}\.zip$`)

// opinionRe catches judgements that have no place in an app's facts: the
// store states what can be checked and leaves the judging to the user.
var opinionRe = regexp.MustCompile(`(?i)\b(recommended|use with care|likely|probably|may not work|won't work well|too heavy|trustworthy|safe to)\b`)

// Every built-in app must be installable: a unique, safe folder and either a
// manifest or a zip pinned to a tagged release (or one commit's archive)
// with a SHA-256, version, date and size. Its facts are statements, not
// judgements.
func TestBuiltinAppsArePinned(t *testing.T) { checkApps(t, apps) }

// TestCatalogFile checks a catalogue tools/catalog wrote, when
// STORE_CATALOG names it: the store keeps every entry, and each passes the
// built-in list's checks.
func TestCatalogFile(t *testing.T) {
	p := os.Getenv("STORE_CATALOG")
	if p == "" {
		t.Skip("set STORE_CATALOG=FILE to check a catalogue")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw Catalog
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(b, "Community apps")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) != len(raw.Apps) || len(c.Apps) == 0 {
		t.Fatalf("%d of %d apps kept", len(c.Apps), len(raw.Apps))
	}
	checkApps(t, c.Apps)
}

// checkApps applies the rules above to a list of community apps.
func checkApps(t *testing.T, apps []App) {
	t.Helper()
	// MiniDeck's own apps' folders: a community app installed into one
	// would overwrite a MiniDeck app.
	seen := map[string]bool{}
	for _, f := range ownFolders {
		seen[strings.ToLower(f)] = true
	}
	for _, a := range apps {
		if a.Name == "" || validFolder(a.Folder) != nil || strings.IndexFunc(a.Name, unicode.IsControl) >= 0 {
			t.Errorf("%q: bad name or folder %q", a.Name, a.Folder)
		}
		if seen[strings.ToLower(a.Folder)] {
			t.Errorf("%q: folder %q is taken by another app", a.Name, a.Folder)
		}
		seen[strings.ToLower(a.Folder)] = true
		if a.Repo == "" || a.License == "" || a.Desc == "" {
			t.Errorf("%q: no repo, licence or description", a.Name)
		}
		if a.Own || len(a.Why) > 0 {
			t.Errorf("%q: built-in apps are community apps, with facts rather than why", a.Name)
		}
		if categoryOf(a) == otherCategory {
			t.Errorf("%q: category %q isn't one of the store's", a.Name, a.Category)
		}
		if a.NeedsFile != nil && validNeedsFile(a) != nil {
			t.Errorf("%q: %v", a.Name, validNeedsFile(a))
		}
		lists := append(append([]string{}, a.Facts...), a.Needs...)
		for _, f := range lists {
			if f == "" {
				t.Errorf("%q: empty fact or need", a.Name)
			}
		}
		for _, s := range append(lists, a.Desc, a.Note, a.Updates, a.System, a.Account) {
			if s != strings.TrimSpace(s) {
				t.Errorf("%q: untrimmed text %q", a.Name, s)
			}
			if m := opinionRe.FindString(s); m != "" {
				t.Errorf("%q: %q is a judgement, not a fact: %s", a.Name, m, s)
			}
		}
		if a.Strip != "" && validStrip(a.Strip) != nil {
			t.Errorf("%q: bad strip %q", a.Name, a.Strip)
		}
		if a.Secure != "" && validSecure(a.Secure) != nil {
			t.Errorf("%q: bad secure path %q", a.Name, a.Secure)
		}
		if a.Secure != "" && a.Account == "" {
			t.Errorf("%q: keeps a sign-in file but doesn't say what it asks for", a.Name)
		}
		if a.Manifest != "" {
			continue
		}
		if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != 32 {
			t.Errorf("%q: bad SHA-256 %q", a.Name, a.SHA256)
		}
		if a.Version == "" || a.Size <= 0 {
			t.Errorf("%q: no version or size", a.Name)
		}
		if _, err := time.Parse("2006-01-02", a.Released); err != nil {
			t.Errorf("%q: release date %q isn't YYYY-MM-DD", a.Name, a.Released)
		}
		release := strings.HasPrefix(a.Zip, "https://github.com/"+a.Repo+"/releases/download/")
		commit := archiveRe.MatchString(a.Zip) && strings.HasPrefix(a.Zip, "https://github.com/"+a.Repo+"/archive/")
		if !release && !commit {
			t.Errorf("%q: zip isn't a tagged release or a commit of %s: %s", a.Name, a.Repo, a.Zip)
		}
		if commit && !strings.HasPrefix(a.Version, "commit ") {
			t.Errorf("%q: a pinned commit's version should read \"commit <sha>\": %q", a.Name, a.Version)
		}
	}
}

// The built-in list survives the catalogue parser whole, as it would if it
// were published as a catalogue file.
func TestBuiltinAppsParse(t *testing.T) {
	b, err := json.Marshal(Catalog{Name: "Community apps", Apps: apps})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(b, "Community apps")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) != len(apps) {
		t.Fatalf("%d of %d apps kept", len(c.Apps), len(apps))
	}
	for i, a := range c.Apps {
		if a.Folder != apps[i].Folder || len(a.Facts) != len(apps[i].Facts) {
			t.Errorf("%s changed in parsing", a.Name)
		}
	}
}

// TestPickArchives checks the pinned downloads themselves, when
// MINIDECK_PICKS names a folder holding them as <Folder>.zip: each must
// match its SHA-256 and size, and unpack with the store's own rules into an
// App/<Folder> with a config.json that has a launch entry.
func TestPickArchives(t *testing.T) {
	dir := os.Getenv("MINIDECK_PICKS")
	if dir == "" {
		t.Skip("set MINIDECK_PICKS=DIR (holding <Folder>.zip for each app) to check the downloads")
	}
	n := 0
	for _, a := range apps {
		if a.Manifest != "" {
			continue
		}
		zp := filepath.Join(dir, a.Folder+".zip")
		fi, err := os.Stat(zp)
		if err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		if sum, err := sha256File(zp); err != nil || sum != a.SHA256 {
			t.Errorf("%s: SHA-256 %s, pinned %s", a.Name, sum, a.SHA256)
			continue
		}
		if fi.Size() != a.Size {
			t.Errorf("%s: %d bytes, listed as %d", a.Name, fi.Size(), a.Size)
		}
		root := filepath.Join(t.TempDir(), "App")
		if err := unpackApp(a, zp, root); err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		cfg, _ := os.ReadFile(filepath.Join(root, a.Folder, "config.json"))
		if launchRe.Find(cfg) == nil {
			t.Errorf("%s: config.json has no launch entry", a.Name)
		}
		if ents, _ := os.ReadDir(root); len(ents) != 1 {
			t.Errorf("%s: unpacked %d folders into App/", a.Name, len(ents))
		}
		n++
	}
	t.Logf("%d archives checked", n)
}

// ownFolders are the App folders of MiniDeck's apps.
var ownFolders = []string{"MiniScale", "MiniRack", "MiniTape", "MiniKey", "MiniTop", "MiniMart", "MiniBook", "MiniRadar", "MiniPulse"}
