package main

// Ports: PC games ported to the Miyoo, from the MiyooMini-Ports list.
//
// tools/catalog pins each listed port's newest Onion release (a zip laid
// out as Roms/PORTS/{Games,Imgs,Shortcuts,Manuals}/...), records its
// SHA-256 and the games its shortcuts launch, and publishes the result as
// ports.json beside the community apps catalogue. The store installs a
// port only from such an entry, and only if the download matches.
//
// A port of a free game is playable once installed. A port of a
// commercial game is only the engine: the game's own data comes from the
// user's copy, which the store never downloads. Onion shows a port only
// while its shortcut ends in .port, and its import script names a
// shortcut .notfound while the game's data file (GameDataFile) is missing;
// the store does the same check after an install and whenever asked, so a
// game appears in Onion's Ports list as soon as its files are in place.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
)

// Port is one entry of a ports list. The first fields are the
// MiyooMini-Ports list's own; the rest are added by tools/catalog when it
// could pin an installable release.
type Port struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Status     string   `json:"status"`
	Assets     string   `json:"assets"` // "owned": needs your own game files
	Upstream   string   `json:"upstream"`
	Image      string   `json:"image"`
	Notes      string   `json:"notes"`
	Porter     []string `json:"porter"`

	Repo     string `json:"repo,omitempty"` // github.com/<Repo>
	License  string `json:"license,omitempty"`
	Version  string `json:"version,omitempty"`
	Released string `json:"released,omitempty"`
	Zip      string `json:"zip,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`     // download
	Unpacked int64  `json:"unpacked,omitempty"` // the files installed, together
	// Root is the folder in the zip that holds Games/, Shortcuts/... :
	// usually "Roms/PORTS/", "" when they sit at the zip's top.
	Root string `json:"root,omitempty"`
	// Loose: the zip has its shortcuts at Root's top rather than in
	// Shortcuts/ (made for another firmware); they go in Shortcuts/.
	Loose bool       `json:"loose,omitempty"`
	Games []PortGame `json:"games,omitempty"`
	// App: the release is an Onion app (App/<App>/), installed from Apps.
	App string `json:"app,omitempty"`
	// Why the port can't be installed from the store, when it can't.
	Why string `json:"why,omitempty"`

	Source string `json:"-"`
}

// PortGame is one game a port's shortcut launches.
type PortGame struct {
	// Shortcut: its path under Roms/PORTS/Shortcuts/, without the .port or
	// .notfound ending ("RPG/Undertale").
	Shortcut string `json:"shortcut"`
	Dir      string `json:"dir"`  // Roms/PORTS/Games/<Dir>
	Data     string `json:"data"` // the file Onion looks for there (GameDataFile)
	// Shipped: that file comes with the port, so Onion's check can't tell
	// whether the user's own game files are there. Dir and Data are empty
	// for a shortcut that is a plain launcher, which Onion doesn't check.
	Shipped bool `json:"shipped,omitempty"`
}

// launcher reports whether the shortcut is a plain launcher script.
func (g PortGame) launcher() bool { return g.Dir == "" && g.Data == "" }

// portsCatalog is the ports list tools/catalog publishes with the
// community apps catalogue.
const portsCatalog = "https://github.com/zakijss/MiniMart/releases/download/catalog/ports.json"

// upstreamPorts is the address the built-in ports source had before the
// store could install ports; a sources.json saved then still names it.
const upstreamPorts = "https://raw.githubusercontent.com/Producdevity/MiyooMini-Ports/master/ports.json"

func portsDir() string { return filepath.Join(sdcard(), "Roms", "PORTS") }

// portSegRe: one path segment of an installed file. Letters, digits,
// spaces and punctuation FAT allows; no control characters.
var portSegRe = regexp.MustCompile(`^[^\x00-\x1f"*:<>?\\|/]{1,120}$`)

// portAreas are the folders of Roms/PORTS a port may write to.
var portAreas = []string{"Games", "Imgs", "Shortcuts", "Manuals"}

// portPath checks a path relative to Roms/PORTS and returns it cleaned, or
// "" if it isn't a plain path inside one of portAreas.
func portPath(rel string) string {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return ""
	}
	segs := strings.Split(strings.TrimSuffix(rel, "/"), "/")
	if len(segs) < 2 || len(segs) > 12 || !slices.Contains(portAreas, segs[0]) {
		return ""
	}
	for _, s := range segs {
		if s == "." || s == ".." || !portSegRe.MatchString(s) || strings.TrimSpace(s) == "" {
			return ""
		}
	}
	return strings.Join(segs, "/")
}

// validPortGame checks a game's shortcut, folder and data file names.
func validPortGame(g PortGame) error {
	if portPath("Shortcuts/"+g.Shortcut+".port") == "" {
		return fmt.Errorf("bad shortcut %q", g.Shortcut)
	}
	if g.launcher() {
		return nil
	}
	if portPath("Games/"+g.Dir) == "" || strings.Contains(g.Dir, "/") {
		return fmt.Errorf("bad game folder %q", g.Dir)
	}
	if !portSegRe.MatchString(g.Data) || strings.HasPrefix(g.Data, ".") || strings.ContainsAny(g.Data, "/[]") {
		return fmt.Errorf("bad game data file %q", g.Data)
	}
	return nil
}

// Installable reports whether the store can install the port itself.
func (p Port) Installable() bool { return p.installErr() == nil }

func (p Port) installErr() error {
	switch {
	case p.App != "":
		return errors.New("it's an app: install it from Apps")
	case p.Zip == "":
		if p.Why != "" {
			return errors.New(p.Why)
		}
		return errors.New("no release MiniMart can install")
	case !isSHA256(p.SHA256):
		return errors.New("the list gives no checksum for it")
	case len(p.Games) == 0:
		return errors.New("its release has no shortcut for Onion's Ports list")
	}
	if u, err := url.Parse(p.Zip); err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("its download address isn't https")
	}
	if p.Root != "" && p.Root != "Roms/PORTS/" && p.Root != "PORTS/" {
		return fmt.Errorf("unexpected folder %q in its release", p.Root)
	}
	for _, g := range p.Games {
		if err := validPortGame(g); err != nil {
			return err
		}
	}
	return nil
}

// key names the port in the store's records.
func (p Port) key() string {
	if p.Repo != "" {
		return p.Repo + "#" + p.Name
	}
	return p.Upstream + "#" + p.Name
}

// needsOwnFiles reports whether the port needs files from the user's own
// copy of the game.
func (p Port) needsOwnFiles() bool { return p.Assets == "owned" }

// ParsePorts reads a ports list, in the MiyooMini-Ports format with or
// without the fields tools/catalog adds. Entries with no name, or with
// control characters in the fields shown, are dropped; entries whose
// install fields don't check out are kept as guide entries.
func ParsePorts(b []byte, from string) ([]Port, error) {
	var doc struct {
		Ports []Port `json:"ports"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("not a ports list: %v", err)
	}
	ctl := func(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
	var out []Port
	for _, p := range doc.Ports {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" || ctl(p.Name) || ctl(p.Status) || len(p.Name) > 120 {
			continue
		}
		if p.Zip != "" && p.installErr() != nil && p.App == "" {
			if p.Why == "" {
				p.Why = "its entry in the list didn't pass the store's checks"
			}
			p.Zip, p.SHA256, p.Games = "", "", nil
		}
		if p.App != "" && validFolder(p.App) != nil {
			p.App = ""
		}
		p.Source = from
		out = append(out, p)
	}
	return out, nil
}

// Ports lists the ports of every enabled ports source. The built-in list
// falls back to the copy compiled into the store when it can't be read.
func (f *fetcher) Ports(ss []Source) ([]Port, error) {
	var out []Port
	var firstErr error
	seen := map[string]bool{}
	for _, s := range ss {
		if !s.Enabled || s.Type != "ports" {
			continue
		}
		var ps []Port
		b, err := f.cached(s.URL, true)
		if err == nil {
			ps, err = ParsePorts(b, s.Name)
		}
		if err != nil && s.URL == portsCatalog {
			log.Printf("ports list: %v; using the built-in copy", err)
			ps, err = ParsePorts(builtinPorts, s.Name)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", s.Name, err)
			}
			continue
		}
		for _, p := range ps {
			if !seen[p.key()] {
				seen[p.key()] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, firstErr
	}
	// Ports that install from here first, then apps, then the guide.
	group := func(p Port) int {
		switch {
		case p.Installable():
			return 0
		case p.App != "":
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if gi, gj := group(out[i]), group(out[j]); gi != gj {
			return gi < gj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// portRecord is what the store installed for a port.
type portRecord struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	SHA256    string   `json:"sha256"`
	Files     []string `json:"files"`     // relative to Roms/PORTS
	Shortcuts []string `json:"shortcuts"` // as PortGame.Shortcut
	Date      string   `json:"date"`
}

func portRecordsPath() string { return filepath.Join(appdir.Dir(), "ports-installed.json") }

func loadPortRecords() map[string]portRecord {
	var m map[string]portRecord
	if err := loadJSON(portRecordsPath(), &m); err != nil {
		log.Print(err)
	}
	if m == nil {
		m = map[string]portRecord{}
	}
	return m
}

func savePortRecords(m map[string]portRecord) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(portRecordsPath(), b, 0644)
}

// shortcutFile returns the shortcut's file on the card and whether it is
// active (.port), or "" if neither .port nor .notfound is there.
func shortcutFile(shortcut string) (string, bool) {
	base := filepath.Join(portsDir(), "Shortcuts", filepath.FromSlash(shortcut))
	if fileExists(base + ".port") {
		return base + ".port", true
	}
	if fileExists(base + ".notfound") {
		return base + ".notfound", false
	}
	return "", false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// PortState is what is on the card for a port.
type PortState struct {
	Installed bool   // the store installed it
	ByHand    bool   // a shortcut of it is there, but the store didn't put it there
	Version   string // the version the store installed
	Missing   []PortGame
	// Unchecked: games whose own files the store can't check for (see
	// PortGame.Shipped), of a port that needs the user's copy.
	Unchecked []PortGame
}

// Ready reports whether every game of the port can be played.
func (st PortState) Ready() bool {
	return (st.Installed || st.ByHand) && len(st.Missing) == 0 && len(st.Unchecked) == 0
}

// State looks at the card.
func (p Port) State(recs map[string]portRecord) PortState {
	var st PortState
	if r, ok := recs[p.key()]; ok {
		st.Installed, st.Version = true, r.Version
	}
	for _, g := range p.Games {
		f, _ := shortcutFile(g.Shortcut)
		if f == "" {
			continue
		}
		if !st.Installed {
			st.ByHand = true
		}
		switch {
		case !gameDataPresent(g):
			st.Missing = append(st.Missing, g)
		case g.Shipped && p.needsOwnFiles():
			st.Unchecked = append(st.Unchecked, g)
		}
	}
	return st
}

// gameDataPresent is Onion's check (Emu/PORTS/import.sh): a file named
// g.Data, in any case, in Games/<Dir>/ or one folder below it.
func gameDataPresent(g PortGame) bool {
	if g.launcher() {
		return true
	}
	dir := filepath.Join(portsDir(), "Games", g.Dir)
	match := func(d string) (bool, []string) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return false, nil
		}
		var subs []string
		for _, e := range ents {
			if e.IsDir() {
				subs = append(subs, filepath.Join(d, e.Name()))
			} else if strings.EqualFold(e.Name(), g.Data) && e.Type().IsRegular() {
				return true, nil
			}
		}
		return false, subs
	}
	ok, subs := match(dir)
	if ok {
		return true
	}
	for _, s := range subs {
		if ok, _ := match(s); ok {
			return true
		}
	}
	return false
}

// activate names each game's shortcut .port when its data is there and
// .notfound when it isn't, as Onion's import does, and clears Onion's list
// cache if anything changed. It returns the games still missing data.
func activate(games []PortGame) []PortGame {
	var missing []PortGame
	changed := false
	for _, g := range games {
		if g.launcher() {
			continue // Onion runs it as it is
		}
		base := filepath.Join(portsDir(), "Shortcuts", filepath.FromSlash(g.Shortcut))
		have := gameDataPresent(g)
		if !have {
			missing = append(missing, g)
		}
		from, to := base+".notfound", base+".port"
		if !have {
			from, to = to, from
		}
		if !fileExists(from) {
			continue
		}
		if fileExists(to) {
			os.Remove(to)
		}
		if err := os.Rename(from, to); err != nil {
			log.Printf("ports: %v", err)
			continue
		}
		changed = true
	}
	if changed {
		resetPortsList()
	}
	return missing
}

// syncShortcuts names the port's shortcuts for whether their games' files
// are there now (see activate), if any is wrong: the user may have copied
// the files in, or deleted them, since. It does nothing for a port that
// isn't on the card.
func syncShortcuts(p Port) {
	var stale []PortGame
	for _, g := range p.Games {
		if g.launcher() {
			continue
		}
		f, active := shortcutFile(g.Shortcut)
		if f != "" && active != gameDataPresent(g) {
			stale = append(stale, g)
		}
	}
	if len(stale) > 0 {
		activate(stale)
	}
}

// resetPortsList makes Onion read the Ports list again, as its
// reset_list.sh does: it deletes the list's cache.
func resetPortsList() {
	for _, v := range []string{"Shortcuts_cache6.db", "Shortcuts_cache2.db"} {
		os.Remove(filepath.Join(portsDir(), "Shortcuts", v))
	}
}

// Limits on a port's zip, checked against its directory before anything
// is unpacked.
const (
	maxPortFiles    = 20000
	maxPortUnpacked = 2 << 30
	maxPortFile     = 1 << 30
	maxPortDownload = 1 << 30
)

// portEntry is one file of a port's zip and where it goes.
type portEntry struct {
	zf  *zip.File
	rel string // relative to Roms/PORTS
}

// portEntries lists the zip's files that the port installs: those under
// p.Root in one of portAreas. Any other path under them that isn't plain
// refuses the whole zip. Folders, links, macOS clutter and files elsewhere
// in the zip (READMEs, tools for a computer) are left out.
func portEntries(zr *zip.Reader, p Port) ([]portEntry, int64, error) {
	var out []portEntry
	var total int64
	for _, zf := range zr.File {
		name := strings.ReplaceAll(zf.Name, `\`, "/") // zips made on Windows
		if junk(name) || !strings.HasPrefix(name, p.Root) {
			continue
		}
		rel := strings.TrimPrefix(name, p.Root)
		if p.Loose && !strings.Contains(rel, "/") && strings.EqualFold(path.Ext(rel), ".port") {
			rel = "Shortcuts/" + rel
		}
		top, _, _ := strings.Cut(rel, "/")
		if !slices.Contains(portAreas, top) {
			continue
		}
		if zf.FileInfo().IsDir() || strings.HasSuffix(rel, "/") {
			continue
		}
		clean := portPath(rel)
		if clean == "" {
			return nil, 0, fmt.Errorf("unsafe path in zip: %s", zf.Name)
		}
		if !zf.Mode().IsRegular() {
			continue
		}
		if zf.UncompressedSize64 > maxPortFile {
			return nil, 0, fmt.Errorf("a file in the zip is too big (%s)", path.Base(clean))
		}
		total += int64(zf.UncompressedSize64)
		out = append(out, portEntry{zf, clean})
	}
	switch {
	case len(out) == 0:
		return nil, 0, errors.New("the zip has no port in it")
	case len(out) > maxPortFiles:
		return nil, 0, errors.New("the zip holds too many files")
	case total > maxPortUnpacked:
		return nil, 0, fmt.Errorf("the port is too big to unpack (%d MB)", total>>20)
	}
	seen := map[string]bool{}
	for _, e := range out {
		k := strings.ToLower(e.rel) // FAT ignores case
		if seen[k] {
			return nil, 0, fmt.Errorf("the zip has %s twice", e.rel)
		}
		seen[k] = true
	}
	return out, total, nil
}

// portNeeds is how much room installing the port takes: the download and
// the files unpacked from it, plus a margin for the file system.
func portNeeds(p Port) int64 { return p.Size + p.Unpacked + p.Unpacked/20 + 4<<20 }

// pendingPort is a checked download waiting to be installed.
type pendingPort struct {
	p   Port
	tmp string
	zr  *zip.ReadCloser
	es  []portEntry
	// Replaces: files on the card the store didn't install that the port
	// would replace (a copy installed by hand, say).
	Replaces []string
}

// DownloadPort downloads the port's zip into the staging folder, checks it
// against the list's SHA-256 and lists what it would install.
func (f *fetcher) DownloadPort(p Port, progress func(done, total int64)) (*pendingPort, error) {
	if err := p.installErr(); err != nil {
		return nil, err
	}
	if free := freeSpace(sdcard()); free >= 0 && free < portNeeds(p) {
		return nil, fmt.Errorf("not enough space on the card (it needs about %s, %s free): make room first (MiniTop › Storage)", humanSize(portNeeds(p)), humanSize(free))
	}
	if err := os.MkdirAll(stagingDir(), 0755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(stagingDir(), "port-"+slugify(p.Name)+".zip")
	if err := f.fetchTo(p.Zip, tmp, maxPortDownload, progress); err != nil {
		return nil, err
	}
	fail := func(err error) (*pendingPort, error) { os.Remove(tmp); return nil, err }
	sum, err := sha256File(tmp)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(sum, p.SHA256) {
		return fail(errors.New("the download doesn't match the checksum in the ports list: not installed"))
	}
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return fail(errors.New("the download isn't a valid zip"))
	}
	es, total, err := portEntries(&zr.Reader, p)
	if err != nil {
		zr.Close()
		return fail(err)
	}
	if free := freeSpace(portsDirOrCard()); free >= 0 && free < total+total/20+(2<<20) {
		zr.Close()
		return fail(fmt.Errorf("not enough space on the card to unpack it (it needs %s, %s free): make room first (MiniTop › Storage)", humanSize(total), humanSize(free)))
	}
	pp := &pendingPort{p: p, tmp: tmp, zr: zr, es: es}
	ours := map[string]bool{}
	for _, f := range loadPortRecords()[p.key()].Files {
		ours[strings.ToLower(f)] = true
	}
	for _, e := range es {
		if !ours[strings.ToLower(e.rel)] && fileExists(filepath.Join(portsDir(), filepath.FromSlash(e.rel))) {
			pp.Replaces = append(pp.Replaces, e.rel)
		}
	}
	return pp, nil
}

func portsDirOrCard() string {
	if _, err := os.Stat(portsDir()); err == nil {
		return portsDir()
	}
	return sdcard()
}

// Discard deletes the download.
func (pp *pendingPort) Discard() {
	if pp.zr != nil {
		pp.zr.Close()
		pp.zr = nil
	}
	os.Remove(pp.tmp)
}

// Install unpacks the port into Roms/PORTS, removes files an earlier
// version installed that this one doesn't have, records what it wrote, and
// names each shortcut for whether the game's data is there. Each file is
// written beside its final name and renamed into place. It returns the
// games still missing data.
func (pp *pendingPort) Install(progress func(done, total int64)) ([]PortGame, error) {
	defer pp.Discard()
	p := pp.p
	recs := loadPortRecords()
	prev := recs[p.key()]
	var total, done int64
	for _, e := range pp.es {
		total += int64(e.zf.UncompressedSize64)
	}
	var written []string
	made := map[string]bool{}
	for _, e := range pp.es {
		dst := filepath.Join(portsDir(), filepath.FromSlash(e.rel))
		if strings.HasPrefix(e.rel, "Shortcuts/") {
			// One shortcut, whichever ending the zip gives it: the other
			// is removed, and activate() picks the ending.
			stem := strings.TrimSuffix(strings.TrimSuffix(dst, ".port"), ".notfound")
			if stem != dst {
				os.Remove(stem + ".port")
				os.Remove(stem + ".notfound")
			}
		}
		n, err := writePortFile(e.zf, dst, made)
		done += n
		progress(done, total)
		if err != nil {
			// Record what was written so far, so Remove can clean up.
			recs[p.key()] = portRecord{Name: p.Name, Version: p.Version, SHA256: p.SHA256, Files: mergeFiles(prev.Files, written), Shortcuts: shortcutsOf(p), Date: time.Now().Format("2 Jan 2006")}
			savePortRecords(recs)
			return nil, fmt.Errorf("unpacking %s: %w", path.Base(e.rel), err)
		}
		written = append(written, e.rel)
	}
	now := map[string]bool{}
	for _, f := range written {
		now[strings.ToLower(f)] = true
	}
	for _, f := range prev.Files {
		if !now[strings.ToLower(f)] {
			removePortFile(f)
		}
	}
	recs[p.key()] = portRecord{Name: p.Name, Version: p.Version, SHA256: p.SHA256, Files: written, Shortcuts: shortcutsOf(p), Date: time.Now().Format("2 Jan 2006")}
	if err := savePortRecords(recs); err != nil {
		return nil, err
	}
	missing := activate(p.Games)
	resetPortsList()
	return missing, nil
}

func shortcutsOf(p Port) []string {
	var out []string
	for _, g := range p.Games {
		out = append(out, g.Shortcut)
	}
	return out
}

func mergeFiles(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, f := range b {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// writePortFile writes one zip entry to dst, refusing more bytes than the
// zip's directory claims. A file that is already there (an update, which
// may be running) is written beside it and renamed over it; a new one is
// written in place, which on Onion's card (mounted dirsync, so every
// directory change is a synchronous write) takes half the time. made
// holds the folders already created in this install. It returns the bytes
// written.
func writePortFile(zf *zip.File, dst string, made map[string]bool) (int64, error) {
	if dir := filepath.Dir(dst); !made[dir] {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return 0, err
		}
		made[dir] = true
	}
	in, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + ".store-new"
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err == nil {
		tmp = dst
	} else if errors.Is(err, os.ErrExist) {
		out, err = os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	}
	if err != nil {
		return 0, err
	}
	limit := int64(zf.UncompressedSize64)
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("the file is bigger than the zip says")
	}
	if err == nil && tmp != dst {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return n, err
}

// removePortFile deletes a file the store installed (a path relative to
// Roms/PORTS, checked again here) and then any folders it leaves empty,
// up to but not including the area folder (Games/, Imgs/...).
func removePortFile(rel string) {
	clean := portPath(rel)
	if clean == "" {
		return
	}
	p := filepath.Join(portsDir(), filepath.FromSlash(clean))
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		log.Printf("ports: %v", err)
	}
	if strings.HasPrefix(clean, "Shortcuts/") { // it may have been renamed
		stem := strings.TrimSuffix(strings.TrimSuffix(p, ".port"), ".notfound")
		os.Remove(stem + ".port")
		os.Remove(stem + ".notfound")
	}
	pruneEmpty(filepath.Dir(p))
}

// pruneEmpty removes dir and its parents while they are empty, stopping at
// the area folders of Roms/PORTS.
func pruneEmpty(dir string) {
	stop := map[string]bool{portsDir(): true}
	for _, a := range portAreas {
		stop[filepath.Join(portsDir(), a)] = true
	}
	for !stop[dir] && strings.HasPrefix(dir, portsDir()+string(os.PathSeparator)) {
		if os.Remove(dir) != nil { // not empty, or already gone
			return
		}
		dir = filepath.Dir(dir)
	}
}

// RemovePort deletes the files the store installed for the port. Files the
// user added (the game's data, saves, settings the game wrote) stay, and
// so do the folders holding them.
func RemovePort(p Port) error {
	recs := loadPortRecords()
	r, ok := recs[p.key()]
	if !ok {
		return errors.New("it wasn't installed by MiniMart")
	}
	// Deepest first, so folders empty out before their parents are tried.
	files := append([]string(nil), r.Files...)
	sort.Slice(files, func(i, j int) bool { return strings.Count(files[i], "/") > strings.Count(files[j], "/") })
	for _, f := range files {
		removePortFile(f)
	}
	for _, s := range r.Shortcuts {
		if portPath("Shortcuts/"+s+".port") != "" {
			removePortFile("Shortcuts/" + s + ".port")
		}
	}
	delete(recs, p.key())
	resetPortsList()
	return savePortRecords(recs)
}

// userFilesLeft lists the files left in the port's game folders after it
// was removed: what the user added.
func userFilesLeft(p Port) int {
	n := 0
	for _, g := range p.Games {
		filepath.WalkDir(filepath.Join(portsDir(), "Games", g.Dir), func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
	}
	return n
}
