package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
)

// Source is a repository the store reads.
//
//	themes  a repository laid out like OnionUI/Themes (its raw base URL):
//	        themes and icon packs
//	apps    an app catalogue: a JSON file (see Catalog)
//	ports   the MiyooMini-Ports list (ports.json): browse only, since ports
//	        need files you own and have no checksums
//	games   homebrew games: a Homebrew Hub API address (ending in /api/) or
//	        a game catalogue JSON file (see GameCatalog in games.go)
type Source struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Builtin bool   `json:"builtin,omitempty"`
}

// communityCatalog is the "Community apps" catalogue: tools/catalog
// rebuilds it from GitHub every day and publishes it as an asset of this
// project's release tagged "catalog" (a pre-release, so the app's own
// releases stay the latest). When it can't be read, the list compiled into
// the app (picks.go, made by the same tool) is shown instead.
const communityCatalog = "https://github.com/zakijss/MiniMart/releases/download/catalog/community.json"

// builtinCatalog is the address the "Community apps" source had when its
// list lived only in the app; a sources.json saved then still names it.
const builtinCatalog = "builtin:"

// minideckCatalog is this project's own apps, from the newest release.
// GitHub answers the latest/download address with a redirect to the file,
// which the HTTP client follows. Before the first release it is a 404, and
// the Apps list says so in a line instead of failing.
const minideckCatalog = "https://github.com/zakijss/MiniDeck/releases/latest/download/catalog.json"

// defaultSources are the built-in repositories.
var defaultSources = []Source{
	{Name: "MiniDeck", Type: "apps", URL: minideckCatalog, Enabled: true, Builtin: true},
	{Name: "OnionUI Themes", Type: "themes", URL: "https://raw.githubusercontent.com/OnionUI/Themes/main/", Enabled: true, Builtin: true},
	{Name: "Community apps", Type: "apps", URL: communityCatalog, Enabled: true, Builtin: true},
	{Name: "MiyooMini Ports", Type: "ports", URL: "https://raw.githubusercontent.com/Producdevity/MiyooMini-Ports/master/ports.json", Enabled: true, Builtin: true},
	{Name: "Homebrew Hub", Type: "games", URL: "https://hh3.gbdev.io/api/", Enabled: true, Builtin: true},
}

func sourcesPath() string { return filepath.Join(appdir.Dir(), "sources.json") }

// LoadSources reads sources.json; built-in sources are always present
// (they can be turned off, not removed).
func LoadSources() []Source {
	var saved []Source
	if err := loadJSON(sourcesPath(), &saved); err != nil {
		log.Print(err)
	}
	out := append([]Source(nil), defaultSources...)
	for i := range out {
		for _, s := range saved {
			if s.Builtin && (s.URL == out[i].URL || s.URL == builtinCatalog && out[i].URL == communityCatalog) {
				out[i].Enabled = s.Enabled
			}
		}
	}
	for _, s := range saved {
		if !s.Builtin {
			out = append(out, s)
		}
	}
	return out
}

// SaveSources writes the repositories to sources.json.
func SaveSources(ss []Source) error {
	b, err := json.MarshalIndent(ss, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(sourcesPath(), b, 0644)
}

// NormalizeURL accepts a full URL or GitHub shorthand: "owner/repo" for a
// theme repository (its main branch), or a github.com/.../blob/... link to
// a catalogue file.
func NormalizeURL(typ, in string) (string, error) {
	u := strings.TrimSpace(in)
	if u == "" {
		return "", errors.New("enter an address")
	}
	if !strings.Contains(u, "://") {
		if parts := strings.Split(strings.Trim(u, "/"), "/"); typ == "themes" && len(parts) == 2 {
			return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/main/", nil
		}
		u = "https://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return "", errors.New("that isn't a web address")
	}
	switch pu.Scheme {
	case "https":
	case "http":
		if !plainHTTPOK(pu.Hostname()) {
			return "", errors.New("use an https:// address (plain http is only allowed on this device and your tailnet, by its 100.x address or full .ts.net name)")
		}
	default:
		return "", errors.New("use an https:// address")
	}
	// github.com/o/r/blob/branch/path -> raw
	if strings.HasPrefix(u, "https://github.com/") && strings.Contains(u, "/blob/") {
		u = strings.Replace(strings.Replace(u, "https://github.com/", "https://raw.githubusercontent.com/", 1), "/blob/", "/", 1)
	}
	if typ == "themes" && !strings.HasSuffix(u, "/") {
		u += "/"
	}
	if typ == "games" && strings.HasSuffix(u, "/api") {
		u += "/" // a Homebrew Hub
	}
	return u, nil
}

// tailnetV4 and tailnetV6 are the addresses Tailscale gives tailnet
// devices.
var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// plainHTTPOK reports whether a repository on host may be read over plain
// http: this device itself, or a tailnet address, whose traffic WireGuard
// already encrypts. Only the host decides, never the rest of the URL.
//
// A short MagicDNS name ("nas") is refused: without MiniScale running, the
// Wi-Fi's DNS answers it, and that may be any machine on the network. Its
// full name (nas.tailnet.ts.net) is accepted.
func plainHTTPOK(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || tailnetV4.Contains(ip) || tailnetV6.Contains(ip)
	}
	return strings.HasSuffix(host, ".ts.net") && len(host) > len(".ts.net")
}

// Catalog is the app catalogue format:
//
//	{"name": "My apps", "apps": [{
//	    "name": "...", "folder": "App folder name", "desc": "...",
//	    "repo": "owner/repo",     (on GitHub; shown so users can read it)
//	    "license": "MIT",         (SPDX name; "None" when there's no
//	                               licence file)
//	    "manifest": "https://.../manifest.json"          (MiyooFin-style), or
//	    "zip": "https://.../app.zip", "sha256": "...", "version": "1.0",
//	    "released": "2026-10-09", (optional: the release's date)
//	    "size": 123456,           (optional: the download's size in bytes)
//	    "strip": "App/",          (optional: the zip has App/<folder>/...)
//	    "wrap": true,             (optional: the zip has the app's files
//	                               with no folder of their own, at its
//	                               root or under strip)
//	    "secure": "data/config.json",  (optional: a file, relative to the
//	                               app's folder, holding a sign-in or key;
//	                               the store keeps it encrypted)
//	    "account": "...",         (optional: the sign-in, API key or
//	                               password it asks for)
//	    "updates": "...",         (optional: how it updates itself from the
//	                               internet, outside the store's checks)
//	    "system": "...",          (optional: what it changes outside its
//	                               folder; the store says so first)
//	    "needs": ["Wi-Fi", ...],  (optional: what it needs to work)
//	    "facts": ["...", ...],    (optional: other statements a user can
//	                               check, one short sentence each)
//	    "note": "...",            (optional: how to set it up or use it)
//	    "boot": ["name.sh"],      (optional: boot scripts the zip has in
//	                               .tmp_update/startup/, installed too)
//	    "category": "Media",      (optional: one of categories, sort.go)
//	    "needsfile": {...}}]}     (optional: a game file the user supplies
//	                               from their own copy; see NeedsFile)
//
// Without strip or wrap, the zip has <folder>/ at its root. Only the app's
// folder is unpacked: stray files and __MACOSX are left out. Every app needs
// a SHA-256, from its manifest or the catalogue itself. All fields but name,
// folder and a checksum are optional, so older catalogues still work: their
// "why" list is shown as facts, and a "grade" is ignored, since the store
// shows only what a user can check.
type Catalog struct {
	Name string `json:"name"`
	Apps []App  `json:"apps"`
}

// ParseCatalog reads a catalogue and keeps only installable apps.
func ParseCatalog(b []byte, from string) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("not an app catalogue: %v", err)
	}
	var ok []App
	for _, a := range c.Apps {
		// For an app with Secure, the folder goes into the commands of a
		// boot hook that runs as root and the name into its comments. They
		// are quoted and cleaned there too, but an entry with a folder
		// outside folderRe or a name with control characters is dropped.
		if a.Name = strings.TrimSpace(a.Name); a.Name == "" || strings.IndexFunc(a.Name, unicode.IsControl) >= 0 || validFolder(a.Folder) != nil {
			continue
		}
		if a.Manifest == "" && (a.Zip == "" || len(a.SHA256) != 64) {
			continue // no checksum: never installed
		}
		if (a.Strip != "" && validStrip(a.Strip) != nil) || (a.Secure != "" && validSecure(a.Secure) != nil) || validBoot(a.Boot) != nil {
			continue // the store couldn't install it as described
		}
		if a.NeedsFile != nil && validNeedsFile(a) != nil {
			continue // nor put the user's game file where it says
		}
		a.Facts = cleanLines(append(a.Facts, a.Why...))
		a.Why = nil
		a.Needs = cleanLines(a.Needs)
		a.Source = from
		ok = append(ok, a)
	}
	c.Apps = ok
	return &c, nil
}

// Apps lists the apps of every enabled catalogue.
func (f *fetcher) Apps(ss []Source) ([]App, []error) {
	var out []App
	var errs []error
	for _, s := range ss {
		if !s.Enabled || s.Type != "apps" {
			continue
		}
		if s.URL == builtinCatalog || s.URL == communityCatalog {
			out = append(out, f.communityApps(s)...)
			continue
		}
		b, err := f.cached(s.URL, true)
		if err != nil {
			errs = append(errs, sourceError{s.Name, err})
			continue
		}
		c, err := ParseCatalog(b, s.Name)
		if err != nil {
			errs = append(errs, sourceError{s.Name, err})
			continue
		}
		// Only the built-in MiniDeck source counts as MiniDeck's own: a
		// repository the user adds can't claim it by its name or URL.
		own := s.Builtin && s.URL == minideckCatalog
		for _, a := range c.Apps {
			a.Own = own
			out = append(out, a)
		}
	}
	return out, errs
}

// communityApps are the apps of the built-in "Community apps" source: the
// published catalogue (the copy on the card, refreshed in the background),
// or the list compiled into the app when that can't be read or is empty.
func (f *fetcher) communityApps(s Source) []App {
	if s.URL == communityCatalog {
		if b, err := f.cached(s.URL, true); err == nil {
			if c, err := ParseCatalog(b, s.Name); err == nil && len(c.Apps) > 0 {
				return c.Apps
			}
		}
	}
	out := make([]App, 0, len(apps))
	for _, a := range apps {
		a.Source = s.Name
		out = append(out, a)
	}
	return out
}

// cleanLines trims each line and drops empty ones.
func cleanLines(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sourceError is a catalogue that couldn't be read.
type sourceError struct {
	name string
	err  error
}

func (e sourceError) Error() string { return e.name + ": " + e.err.Error() }
func (e sourceError) Unwrap() error { return e.err }

// sourcesNote is one calm line about catalogues that couldn't be read, for
// under the Apps list: the other catalogues' apps still show. A missing
// file (404) is usually a project with no release yet.
func sourcesNote(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	var se sourceError
	name, why := "A catalogue", "couldn't be loaded"
	if errors.As(errs[0], &se) {
		name = se.name
	}
	var st statusError
	if errors.As(errs[0], &st) && st.code == http.StatusNotFound {
		why = "no release yet"
	} else if errors.As(errs[0], &st) {
		why = "not available just now (" + st.Error() + ")"
	} else {
		why = "couldn't be reached; showing the others"
	}
	note := name + ": " + why
	if len(errs) > 1 {
		note += fmt.Sprintf(" (and %d more)", len(errs)-1)
	}
	return note
}

// Port is one entry of the MiyooMini-Ports list.
type Port struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Status     string   `json:"status"`
	Assets     string   `json:"assets"` // "owned": needs your own game files
	Upstream   string   `json:"upstream"`
	Image      string   `json:"image"`
	Notes      string   `json:"notes"`
	Porter     []string `json:"porter"`
}

// Ports lists the ports of every enabled ports source.
func (f *fetcher) Ports(ss []Source) ([]Port, error) {
	var out []Port
	for _, s := range ss {
		if !s.Enabled || s.Type != "ports" {
			continue
		}
		b, err := f.cached(s.URL, true)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Ports []Port `json:"ports"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: not a ports list", s.Name)
		}
		out = append(out, doc.Ports...)
	}
	return out, nil
}

// Validate checks that a new source answers in the expected format, and
// names it.
func (f *fetcher) Validate(s Source) (string, error) {
	switch s.Type {
	case "themes":
		b, err := f.get(s.URL + ".github/data/custom.txt")
		if err != nil {
			return "", fmt.Errorf("no theme list at %s.github/data/custom.txt (%v)", s.URL, err)
		}
		if len(lines(b)) == 0 {
			return "", errors.New("the theme list is empty")
		}
		parts := strings.Split(strings.TrimPrefix(s.URL, "https://raw.githubusercontent.com/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1], nil
		}
		return s.URL, nil
	case "apps":
		b, err := f.get(s.URL)
		if err != nil {
			return "", err
		}
		c, err := ParseCatalog(b, "")
		if err != nil {
			return "", err
		}
		if len(c.Apps) == 0 {
			return "", errors.New("the catalogue has no apps with a checksum")
		}
		if c.Name != "" {
			return c.Name, nil
		}
		return s.URL, nil
	case "games":
		if isHomebrewHub(s.URL) {
			for _, sys := range systems {
				if sys.HH == "" {
					continue
				}
				resp, err := f.http.Get(hhSearchURL(s.URL, sys))
				if err != nil {
					return "", err
				}
				if resp.StatusCode != http.StatusOK {
					resp.Body.Close()
					return "", fmt.Errorf("no Homebrew Hub at %s (%v)", s.URL, statusError{resp.StatusCode})
				}
				gs, err := parseHomebrewHub(io.LimitReader(resp.Body, 32<<20), sys, s.URL)
				resp.Body.Close()
				if err != nil {
					return "", fmt.Errorf("no Homebrew Hub at %s (%v)", s.URL, err)
				}
				if len(gs) > 0 {
					return "Homebrew Hub (" + strings.TrimSuffix(strings.TrimPrefix(s.URL, "https://"), "/api/") + ")", nil
				}
			}
			return "", errors.New("it lists no games with a licence that allows downloading")
		}
		b, err := f.get(s.URL)
		if err != nil {
			return "", err
		}
		c, err := ParseGameCatalog(b, "", s.URL)
		if err != nil {
			return "", err
		}
		if len(c.Games) == 0 {
			return "", errors.New("the catalogue has no games with a licence that allows downloading")
		}
		if c.Name != "" {
			return c.Name, nil
		}
		return s.URL, nil
	}
	return "", errors.New("unknown repository type")
}
