package main

// Homebrew games: free games made for retro consoles and released by their
// authors under a licence that lets anyone download them. A game is listed
// only if its repository records a licence that allows sharing (see
// licenceOK); games with no recorded licence are left out, even when the
// repository hosts them. No commercial ROMs, ROM packs or BIOS files.
//
// Two kinds of "games" repository:
//
//	Homebrew Hub   its API (https://hh3.gbdev.io/api/): Game Boy, Game Boy
//	               Color, Game Boy Advance and NES homebrew, each entry with
//	               its licence
//	catalogue      a JSON file (see GameCatalog), for other systems
//
// A ROM goes straight into Roms/<system>/; from a zip, only the ROM files
// are kept. The store remembers what it installed (games-installed.json in
// its folder) and removes only those files.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
)

// system is a console Onion runs, by its Roms/ and Emu/ folder name
// (OnionUI/Onion static/packages/Emu/*/Emu/<Folder>/config.json).
type system struct {
	Folder string   // Roms/<Folder> and Emu/<Folder> on the card
	Name   string   // as Onion's Games menu shows it
	HH     string   // the Homebrew Hub platform, if it has one
	Exts   []string // ROM extensions the emulator opens (its extlist)
}

var systems = []system{
	{"GB", "Game Boy", "GB", []string{".gb", ".gbc", ".dmg", ".cgb"}},
	{"GBC", "Game Boy Color", "GBC", []string{".gbc", ".gb", ".cgb", ".dmg"}},
	{"GBA", "Game Boy Advance", "GBA", []string{".gba"}},
	{"FC", "NES", "NES", []string{".nes", ".fds", ".unf", ".unif"}},
	{"SFC", "SNES", "", []string{".sfc", ".smc"}},
	{"MD", "Mega Drive / Genesis", "", []string{".md", ".gen", ".smd", ".bin"}},
	{"PCE", "PC Engine / TurboGrafx-16", "", []string{".pce"}},
}

var systemAliases = map[string]string{
	"GB": "GB", "DMG": "GB", "GAMEBOY": "GB",
	"GBC": "GBC", "CGB": "GBC",
	"GBA": "GBA",
	"FC":  "FC", "NES": "FC", "FAMICOM": "FC",
	"SFC": "SFC", "SNES": "SFC", "SUPERFAMICOM": "SFC",
	"MD": "MD", "GEN": "MD", "GENESIS": "MD", "MEGADRIVE": "MD",
	"PCE": "PCE", "TG16": "PCE", "PCENGINE": "PCE", "TURBOGRAFX16": "PCE",
}

// systemFor finds a system by Onion folder name or a common alias ("NES",
// "SNES", "Genesis", "TG-16"...).
func systemFor(code string) (system, bool) {
	k := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(code))
	for _, s := range systems {
		if s.Folder == systemAliases[k] {
			return s, true
		}
	}
	return system{}, false
}

// romExt returns the extension a file gets in the system's folder, if the
// emulator opens it. .cgb becomes .gbc: Onion's Game Boy lists don't show
// .cgb files.
func (s system) romExt(name string) (string, bool) {
	ext := strings.ToLower(path.Ext(name))
	for _, e := range s.Exts {
		if e == ext {
			if ext == ".cgb" {
				ext = ".gbc"
			}
			return ext, true
		}
	}
	return "", false
}

func (s system) dir() string { return filepath.Join(sdcard(), "Roms", s.Folder) }

// EmulatorInstalled reports whether Onion has the system's emulator (it's
// added in Package Manager).
func (s system) EmulatorInstalled() bool {
	_, err := os.Stat(filepath.Join(sdcard(), "Emu", s.Folder, "config.json"))
	return err == nil
}

// Game is one installable game.
type Game struct {
	ID      string `json:"id"` // unique in its repository
	Title   string `json:"title"`
	Author  string `json:"author,omitempty"`
	System  string `json:"system"` // Onion folder name (or an alias, in a catalogue)
	License string `json:"license"`
	Assets  string `json:"assets_license,omitempty"` // when the art and music have their own
	Desc    string `json:"desc,omitempty"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256,omitempty"` // when the repository gives one
	Size    int64  `json:"size,omitempty"`
	WIP     bool   `json:"wip,omitempty"`
	Date    string `json:"date,omitempty"` // its release, YYYY-MM-DD, when known

	Source string `json:"-"` // the repository's name
	Repo   string `json:"-"` // and address
}

func (g Game) key() string { return g.Repo + "#" + g.System + "/" + g.ID }

// Licence is the game's licence for display, with the assets' if different.
func (g Game) Licence() string {
	if g.Assets != "" && !strings.EqualFold(g.Assets, g.License) {
		return g.License + ", assets " + g.Assets
	}
	return g.License
}

// licenceRe matches licences that let anyone share the game for free, as
// SPDX identifiers (normalised by licenceOK). The Creative Commons
// NonCommercial and NoDerivatives variants are allowed: the store gives the
// unmodified game away for free.
var licenceRe = regexp.MustCompile(`^(MIT(-0)?|0BSD|ISC|ZLIB|UNLICENSE|WTFPL(-2\.0)?|CC0(-1\.0)?|FSFAP|BSL-1\.0|BEERWARE|` +
	`BSD-[1-4]-CLAUSE(-[A-Z0-9-]+)?|APACHE-[12]\.[01]|[LA]?GPL-[123]\.[01](-ONLY|-OR-LATER|\+)?|MPL-[12]\.[01]|EPL-[12]\.0|` +
	`ARTISTIC-[12]\.0|CC-BY(-NC)?(-ND|-SA)?(-[1-4]\.[05])?)$`)

var (
	parenRe = regexp.MustCompile(`\([^)]*\)`)
	splitRe = regexp.MustCompile(`(?i)\s*[/,;&]\s*|\s+(and|or)\s+`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// licenceOK reports whether a recorded licence allows downloading the game.
// Several licences ("MIT / CC-BY-4.0 (assets)") must all be allowed.
// Anything unrecognised, free text or empty is refused.
func licenceOK(l string) bool {
	l = strings.TrimSpace(parenRe.ReplaceAllString(l, " "))
	if l == "" {
		return false
	}
	for _, p := range splitRe.Split(l, -1) {
		p = strings.ToUpper(spaceRe.ReplaceAllString(strings.TrimSpace(p), "-"))
		if !licenceRe.MatchString(p) {
			return false
		}
	}
	return true
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// GameCatalog is the game catalogue format:
//
//	{"name": "My games", "games": [{
//	    "id": "unique-id", "title": "...", "author": "...",
//	    "system": "MD" (Onion folder, or NES / SNES / Genesis / PCE...),
//	    "license": "an SPDX licence that allows sharing, e.g. MIT, CC-BY-4.0",
//	    "assets_license": "(optional)", "desc": "...",
//	    "url": "https://.../game.md (a ROM, or a zip holding it)",
//	    "sha256": "(optional, checked when given)", "size": 123456}]}
type GameCatalog struct {
	Name  string `json:"name"`
	Games []Game `json:"games"`
}

// ParseGameCatalog reads a catalogue and keeps only installable games with
// an allowed licence.
func ParseGameCatalog(b []byte, from, repo string) (*GameCatalog, error) {
	var c GameCatalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("not a game catalogue: %v", err)
	}
	var ok []Game
	for _, g := range c.Games {
		sys, known := systemFor(g.System)
		if g.Title == "" || !known || !licenceOK(g.License) || (g.Assets != "" && !licenceOK(g.Assets)) {
			continue
		}
		u, err := url.Parse(g.URL)
		if err != nil || !(u.Scheme == "https" || (u.Scheme == "http" && strings.HasSuffix(u.Hostname(), ".ts.net"))) {
			continue
		}
		if _, rom := sys.romExt(u.Path); !rom && !strings.EqualFold(path.Ext(u.Path), ".zip") {
			continue
		}
		if g.SHA256 != "" && !isSHA256(g.SHA256) {
			continue
		}
		if g.ID == "" {
			g.ID = slugify(g.Title)
		}
		if !idRe.MatchString(g.ID) {
			continue
		}
		g.System, g.Source, g.Repo = sys.Folder, from, repo
		ok = append(ok, g)
	}
	c.Games = ok
	return &c, nil
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range strings.ToLower(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// isHomebrewHub tells a Homebrew Hub API address from a catalogue file.
func isHomebrewHub(u string) bool { return strings.HasSuffix(u, "/api/") }

// hhEntry is the part of a Homebrew Hub entry the store uses.
type hhEntry struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Developer   hhDev  `json:"developer"`
	Typetag     string `json:"typetag"`
	Description string `json:"description"`
	License     string `json:"license"`
	GameLicense string `json:"gameLicense"`
	Assets      string `json:"assetsLicense"`
	Assets2     string `json:"assetLicense"`
	Date        string `json:"date"`
	NSFW        bool   `json:"nsfw"`
	WIP         bool   `json:"wip"`
	Files       []struct {
		Filename string `json:"filename"`
		Default  bool   `json:"default"`
		Playable bool   `json:"playable"`
		Hash     struct {
			SHA256 string `json:"sha256"`
		} `json:"hash"`
	} `json:"files"`
	Baserepo string `json:"baserepo"`
	Basepath string `json:"basepath"`
}

// hhDev is a developer: a name, a list of names, or {"name": ...} objects.
type hhDev string

func (d *hhDev) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*d = hhDev(strings.TrimSpace(s))
		return nil
	}
	var one struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &one) == nil && one.Name != "" {
		*d = hhDev(strings.TrimSpace(one.Name))
		return nil
	}
	var many []json.RawMessage
	if json.Unmarshal(b, &many) == nil {
		var names []string
		for _, m := range many {
			var x hhDev
			if x.UnmarshalJSON(m); x != "" {
				names = append(names, string(x))
			}
		}
		*d = hhDev(strings.Join(names, ", "))
	}
	return nil
}

// game turns an entry into a Game, if it's a game with an allowed licence
// and a ROM the system's emulator opens.
func (e hhEntry) game(sys system, api string) (Game, bool) {
	lic := strings.TrimSpace(e.GameLicense)
	if lic == "" {
		lic = strings.TrimSpace(e.License)
	}
	assets := strings.TrimSpace(e.Assets)
	if assets == "" {
		assets = strings.TrimSpace(e.Assets2)
	}
	if e.Typetag != "game" || e.NSFW || !idRe.MatchString(e.Slug) || e.Title == "" ||
		!licenceOK(lic) || (assets != "" && !licenceOK(assets)) {
		return Game{}, false
	}
	// the default ROM, else the first playable one, else any ROM
	pick := -1
	for _, pass := range []func(i int) bool{
		func(i int) bool { return e.Files[i].Default && e.Files[i].Playable },
		func(i int) bool { return e.Files[i].Playable },
		func(i int) bool { return true },
	} {
		for i, f := range e.Files {
			if _, ok := sys.romExt(f.Filename); ok && pass(i) {
				pick = i
				break
			}
		}
		if pick >= 0 {
			break
		}
	}
	if pick < 0 {
		return Game{}, false
	}
	f := e.Files[pick]
	u, ok := hhFileURL(api, e.Baserepo, e.Basepath, e.Slug, f.Filename)
	if !ok {
		return Game{}, false
	}
	g := Game{ID: e.Slug, Title: strings.TrimSpace(e.Title), Author: string(e.Developer), System: sys.Folder,
		License: lic, Assets: assets, Desc: shorten(e.Description, 420), URL: u, WIP: e.WIP}
	if !releasedTime(e.Date).IsZero() {
		g.Date = e.Date
	}
	if isSHA256(f.Hash.SHA256) {
		g.SHA256 = strings.ToLower(f.Hash.SHA256)
	}
	return g, true
}

// hhFileURL is where an entry's file is: the database's GitHub repository
// (raw), or the Homebrew Hub server's copy for databases elsewhere.
func hhFileURL(api, baserepo, basepath, slug, file string) (string, bool) {
	var segs []string
	for _, s := range strings.Split(file, "/") {
		if s == "" || s == "." || s == ".." {
			return "", false
		}
		segs = append(segs, url.PathEscape(s))
	}
	rel := "entries/" + slug + "/" + strings.Join(segs, "/")
	if r, ok := strings.CutPrefix(strings.TrimSuffix(baserepo, "/"), "https://github.com/"); ok {
		if p := strings.Split(r, "/"); len(p) == 2 && idRe.MatchString(p[0]) && idRe.MatchString(p[1]) {
			return "https://raw.githubusercontent.com/" + p[0] + "/" + p[1] + "/HEAD/" + rel, true
		}
	}
	if idRe.MatchString(basepath) {
		return strings.TrimSuffix(api, "api/") + "static/" + basepath + "/" + rel, true
	}
	return "", false
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ,.;:") + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// parseHomebrewHub reads a Homebrew Hub search reply as a stream, keeping
// only the games that pass, so a 700 KB list never sits in memory whole.
func parseHomebrewHub(r io.Reader, sys system, api string) ([]Game, error) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a Homebrew Hub reply")
	}
	var out []Game
	found := false
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if t != "entries" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return nil, errors.New("not a Homebrew Hub reply")
		}
		found = true
		for dec.More() {
			var e hhEntry
			if err := dec.Decode(&e); err != nil {
				var te *json.UnmarshalTypeError
				if errors.As(err, &te) {
					continue // an odd entry: the decoder has read past it
				}
				return nil, err
			}
			if g, ok := e.game(sys, api); ok {
				out = append(out, g)
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("not a Homebrew Hub reply")
	}
	return out, nil
}

// hhSearchURL asks a Homebrew Hub for all of a system's entries.
func hhSearchURL(api string, sys system) string {
	return api + "search?platform=" + sys.HH + "&results=5000"
}

// hhList returns a system's games from a Homebrew Hub: the copy saved on
// the card (refreshed in the background for next time), or a fresh one.
func (f *fetcher) hhList(api string, sys system) ([]Game, error) {
	u := hhSearchURL(api, sys)
	key := "games2|" + u
	if b, ok := f.cache.Get(key); ok {
		var gs []Game
		if json.Unmarshal(b, &gs) == nil {
			go f.hhFetch(u, key, api, sys)
			return gs, nil
		}
	}
	return f.hhFetch(u, key, api, sys)
}

func (f *fetcher) hhFetch(u, key, api string, sys system) ([]Game, error) {
	resp, err := f.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode}
	}
	gs, err := parseHomebrewHub(io.LimitReader(resp.Body, 32<<20), sys, api)
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(gs); err == nil {
		f.cache.Put(key, b)
	}
	return gs, nil
}

// Games lists the games of every enabled game repository, by title. A game
// a second repository also lists (same system and title) is shown once.
func (f *fetcher) Games(ss []Source) ([]Game, []error) {
	var out []Game
	var errs []error
	from := map[string]string{} // system|title -> the repository listing it
	add := func(s Source, gs []Game) {
		for _, g := range gs {
			g.Source, g.Repo = s.Name, s.URL
			k := g.System + "|" + strings.ToLower(g.Title)
			if r, ok := from[k]; ok && r != s.URL {
				continue
			}
			from[k] = s.URL
			out = append(out, g)
		}
	}
	for _, s := range ss {
		if !s.Enabled || s.Type != "games" {
			continue
		}
		if isHomebrewHub(s.URL) {
			for _, sys := range systems {
				if sys.HH == "" {
					continue
				}
				gs, err := f.hhList(s.URL, sys)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
					continue
				}
				add(s, gs)
			}
			continue
		}
		b, err := f.cached(s.URL, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		c, err := ParseGameCatalog(b, s.Name, s.URL)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		add(s, c.Games)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out, errs
}

// GameSize asks the server how big the download is.
func (f *fetcher) GameSize(g Game) int64 {
	if g.Size > 0 {
		return g.Size
	}
	return f.headSize(g.URL)
}

// gameRecord is what the store installed for a game.
type gameRecord struct {
	System   string   `json:"system"`
	Files    []string `json:"files"`  // in Roms/<System>
	SHA256   string   `json:"sha256"` // of the download
	Verified bool     `json:"verified,omitempty"`
	URL      string   `json:"url"`
	Date     string   `json:"date"`
}

func recordsPath() string { return filepath.Join(appdir.Dir(), "games-installed.json") }

// loadRecords reads what the store installed. A file that can't be read is
// set aside (see loadJSON) rather than overwritten by the next save.
func loadRecords() map[string]gameRecord {
	var m map[string]gameRecord
	if err := loadJSON(recordsPath(), &m); err != nil {
		log.Print(err)
	}
	if m == nil {
		m = map[string]gameRecord{}
	}
	return m
}

func saveRecords(m map[string]gameRecord) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(recordsPath(), b, 0644)
}

// recordedName reports whether a file name from the records is a plain
// name in the system's folder, safe to replace or delete.
func recordedName(f string) bool {
	return f != "" && !strings.ContainsAny(f, `/\`) && !strings.HasPrefix(f, ".")
}

// installedFile returns where the store put the game ("" if it isn't
// installed, or its file has gone).
func installedFile(recs map[string]gameRecord, g Game) string {
	r, ok := recs[g.key()]
	if !ok || len(r.Files) == 0 {
		return ""
	}
	sys, _ := systemFor(r.System)
	p := filepath.Join(sys.dir(), r.Files[0])
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return "Roms/" + sys.Folder + "/" + r.Files[0]
}

// romName makes a file name from the title, safe on FAT32.
func romName(title, ext string) string {
	n := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`"*/:<>?\|`, r) {
			return -1
		}
		return r
	}, title)
	n = strings.Join(strings.Fields(n), " ")
	if r := []rune(n); len(r) > 80 {
		n = string(r[:80])
	}
	n = strings.Trim(n, " .")
	if n == "" {
		n = "game"
	}
	return n + ext
}

// refreshList makes Onion re-read the system's game list, as its own
// scripts do (reset_list.sh): it deletes the list cache.
func refreshList(sys system) {
	for _, v := range []string{"_cache6.db", "_cache2.db"} {
		os.Remove(filepath.Join(sys.dir(), sys.Folder+v))
	}
}

func stagingDir() string { return filepath.Join(sdcard(), "Roms", ".store-tmp") }

const maxGameDownload = 128 << 20

// pendingGame is a checked download waiting to be installed.
type pendingGame struct {
	g        Game
	tmp      string
	sum      string
	verified bool   // matched the repository's SHA-256
	Changed  bool   // differs from the copy installed before
	Before   string // when that was
}

// DownloadGame downloads a game into the staging folder and checks it
// against the repository's SHA-256, if it gives one. Without one, the
// download's SHA-256 is compared with the one recorded at the last install.
func (f *fetcher) DownloadGame(g Game, progress func(done, total int64)) (*pendingGame, error) {
	if _, ok := systemFor(g.System); !ok {
		return nil, errors.New("unknown system")
	}
	if err := os.MkdirAll(stagingDir(), 0755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(stagingDir(), slugify(g.ID)+".download")
	if err := f.fetchTo(g.URL, tmp, maxGameDownload, progress); err != nil {
		return nil, err
	}
	sum, err := sha256File(tmp)
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	p := &pendingGame{g: g, tmp: tmp, sum: sum}
	if g.SHA256 != "" {
		if !strings.EqualFold(sum, g.SHA256) {
			os.Remove(tmp)
			return nil, errors.New("the download doesn't match the checksum in the repository: not installed")
		}
		p.verified = true
	}
	if r, ok := loadRecords()[g.key()]; ok && r.SHA256 != "" && r.SHA256 != sum {
		p.Changed, p.Before = true, r.Date
	}
	return p, nil
}

// Discard deletes the download.
func (p *pendingGame) Discard() {
	os.Remove(p.tmp)
	os.RemoveAll(p.tmp + ".unzip")
}

type staged struct{ from, name string }

func isZip(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	_, err = io.ReadFull(f, b)
	return err == nil && string(b) == "PK\x03\x04"
}

// Limits on a game's zip, checked against its directory before anything
// is unpacked: a zip can claim, and hold, far more than its download size.
const (
	maxGameRoms     = 8
	maxGameUnpacked = 256 << 20 // the ROM files together
)

// romsFromZip unpacks the zip's ROM files for the system and returns them;
// everything else in it is dropped. The zip's directory is checked first:
// it must hold 1 to maxGameRoms ROMs, together at most maxGameUnpacked
// bytes, which the card has room for. Each ROM is written under a plain
// numbered name, so no path from the zip is used on the card, and the
// bytes written are counted too, in case the directory lies.
func romsFromZip(zipPath string, sys system, title string) ([]staged, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var files []*zip.File
	var total uint64
	for _, zf := range r.File {
		name := zf.Name
		if path.IsAbs(name) || slices.Contains(strings.Split(name, "/"), "..") {
			return nil, fmt.Errorf("unsafe path in zip: %s", name)
		}
		base := path.Base(name)
		if zf.FileInfo().IsDir() || !zf.Mode().IsRegular() || strings.HasPrefix(base, ".") || strings.Contains(name, "__MACOSX") {
			continue
		}
		if _, ok := sys.romExt(base); ok {
			files = append(files, zf)
			total += zf.UncompressedSize64
		}
	}
	switch {
	case len(files) == 0:
		return nil, fmt.Errorf("the zip has no %s game in it", sys.Name)
	case len(files) > maxGameRoms:
		return nil, errors.New("the zip holds too many files to be one game")
	case total > maxGameUnpacked:
		return nil, fmt.Errorf("the zip's game is too big to unpack (%d MB)", total>>20)
	}
	if free := freeSpace(filepath.Dir(zipPath)); free >= 0 && uint64(free) < total {
		return nil, fmt.Errorf("not enough space on the card to unpack the game (%d MB free): make room first (MiniTop › Storage)", free>>20)
	}
	slices.SortFunc(files, func(a, b *zip.File) int { return strings.Compare(a.Name, b.Name) })

	dir := zipPath + ".unzip"
	os.RemoveAll(dir)
	fail := func(err error) ([]staged, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fail(err)
	}
	left := int64(maxGameUnpacked)
	var roms []staged
	for i, zf := range files {
		to := filepath.Join(dir, fmt.Sprintf("%d.rom", i))
		n, err := unpackFile(zf, to, left)
		if err != nil {
			return fail(err)
		}
		left -= n
		base := path.Base(zf.Name)
		ext, _ := sys.romExt(base)
		roms = append(roms, staged{to, romName(strings.TrimSuffix(base, path.Ext(base)), ext)})
	}
	if len(roms) == 1 { // name it after the game
		roms[0].name = romName(title, path.Ext(roms[0].name))
	}
	taken := map[string]bool{}
	for i, r := range roms { // the same name in two folders of the zip
		ext := path.Ext(r.name)
		for n := 2; taken[strings.ToLower(roms[i].name)]; n++ {
			roms[i].name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(r.name, ext), n, ext)
		}
		taken[strings.ToLower(roms[i].name)] = true
	}
	return roms, nil
}

// unpackFile writes one zip entry to dst, refusing it once it passes limit
// bytes. It returns the bytes written.
func unpackFile(zf *zip.File, dst string, limit int64) (int64, error) {
	in, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("the zip's game is too big to unpack")
	}
	return n, err
}

// Install moves the ROM(s) into Roms/<system>/, records them, and makes
// Onion refresh the list. A file of the same name that the store didn't
// install is never replaced.
func (p *pendingGame) Install() ([]string, error) {
	defer p.Discard()
	g := p.g
	sys, _ := systemFor(g.System)
	var roms []staged
	if isZip(p.tmp) {
		var err error
		if roms, err = romsFromZip(p.tmp, sys, g.Title); err != nil {
			return nil, err
		}
	} else {
		name := g.URL
		if u, err := url.Parse(g.URL); err == nil {
			name = u.Path
		}
		ext, ok := sys.romExt(name)
		if !ok {
			return nil, fmt.Errorf("the download isn't a %s game", sys.Name)
		}
		roms = []staged{{p.tmp, romName(g.Title, ext)}}
	}
	recs := loadRecords()
	prev := recs[g.key()]
	ours := map[string]bool{}
	for _, f := range prev.Files {
		ours[f] = true
	}
	dir := sys.dir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	var names []string
	for _, r := range roms {
		if _, err := os.Stat(filepath.Join(dir, r.name)); err == nil && !ours[r.name] {
			return nil, fmt.Errorf("there's already a %s in Roms/%s: rename or delete it first", r.name, sys.Folder)
		}
		names = append(names, r.name)
	}
	for _, r := range roms {
		if err := os.Rename(r.from, filepath.Join(dir, r.name)); err != nil {
			return nil, err
		}
	}
	for _, f := range prev.Files {
		if recordedName(f) && !slices.Contains(names, f) {
			os.Remove(filepath.Join(dir, f))
		}
	}
	recs[g.key()] = gameRecord{System: sys.Folder, Files: names, SHA256: p.sum, Verified: p.verified, URL: g.URL, Date: time.Now().Format("2 Jan 2006")}
	refreshList(sys)
	if err := saveRecords(recs); err != nil {
		return names, err
	}
	return names, nil
}

// RemoveGame deletes the files the store installed for the game. Saves and
// save states (in Saves/) stay.
func RemoveGame(g Game) error {
	recs := loadRecords()
	r, ok := recs[g.key()]
	if !ok {
		return errors.New("it wasn't installed by the store")
	}
	sys, ok := systemFor(r.System)
	if !ok {
		return fmt.Errorf("unknown system %q in the store's records", r.System)
	}
	for _, f := range r.Files {
		if !recordedName(f) {
			continue
		}
		if err := os.Remove(filepath.Join(sys.dir(), f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	delete(recs, g.key())
	refreshList(sys)
	return saveRecords(recs)
}
