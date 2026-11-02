package main

// Ports mode: reads the MiyooMini-Ports list and pins, for each listed
// port on GitHub, its newest release zip that installs on Onion, so that
// MiniMart can install it. The list's own fields are kept as they are; an
// entry that can't be pinned keeps them only, with the reason, and the
// store shows it as a guide.
//
// A pinned port zip holds Onion's ports layout: Games/, Imgs/, Shortcuts/
// and Manuals/ under Roms/PORTS/ (or at the zip's top). Each shortcut's
// GameDir and GameDataFile are read the way Onion's Emu/PORTS/import.sh
// reads them, so the store can tell when a game's files are in place.
// The tool never adds game data: a port of a commercial game stays an
// engine that needs the user's own copy.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Port is a ports list entry, in the store's format (cmd/minimart Port).
type Port struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Status     string   `json:"status"`
	Assets     string   `json:"assets"`
	Upstream   string   `json:"upstream"`
	Image      string   `json:"image"`
	Notes      string   `json:"notes"`
	Porter     []string `json:"porter"`

	Repo     string     `json:"repo,omitempty"`
	License  string     `json:"license,omitempty"`
	Version  string     `json:"version,omitempty"`
	Released string     `json:"released,omitempty"`
	Zip      string     `json:"zip,omitempty"`
	SHA256   string     `json:"sha256,omitempty"`
	Size     int64      `json:"size,omitempty"`
	Unpacked int64      `json:"unpacked,omitempty"`
	Root     string     `json:"root,omitempty"`
	Loose    bool       `json:"loose,omitempty"`
	Games    []PortGame `json:"games,omitempty"`
	App      string     `json:"app,omitempty"`
	Why      string     `json:"why,omitempty"`
}

// PortGame is one game a port's shortcut launches.
type PortGame struct {
	Shortcut string `json:"shortcut"`
	Dir      string `json:"dir"`
	Data     string `json:"data"`
	// Shipped: the file Onion checks for comes with the port itself, so
	// the check can't tell whether the user's game files are there.
	Shipped bool `json:"shipped,omitempty"`
}

// PortsDoc is the ports list written.
type PortsDoc struct {
	Name      string `json:"name"`
	Generated string `json:"generated"`
	From      string `json:"from"`
	Ports     []Port `json:"ports"`
}

// upstreamPortsList is the MiyooMini-Ports list.
const upstreamPortsList = "https://raw.githubusercontent.com/Producdevity/MiyooMini-Ports/master/ports.json"

var githubRepoRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?(?:/.*)?$`)

// portAreas are the folders of Roms/PORTS a port installs into.
var portAreas = []string{"Games", "Imgs", "Shortcuts", "Manuals"}

var portSegRe = regexp.MustCompile(`^[^\x00-\x1f"*:<>?\\|/]{1,120}$`)

// portPath is the store's check of an installed path (relative to
// Roms/PORTS): it returns the path, or "" if the store would refuse it.
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

// buildPorts makes the ports list from the list at listURL.
func (g *gen) buildPorts(listURL string, exclude map[string]string) (PortsDoc, error) {
	b, err := g.c.raw(listURL)
	if err != nil {
		return PortsDoc{}, fmt.Errorf("the ports list: %w", err)
	}
	var doc struct {
		Ports []Port `json:"ports"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return PortsDoc{}, fmt.Errorf("the ports list: %w", err)
	}
	out := PortsDoc{Name: "MiyooMini Ports", Generated: g.today, From: listURL}
	for _, p := range doc.Ports {
		p.Repo, p.License, p.Version, p.Released, p.Zip, p.SHA256 = "", "", "", "", "", ""
		p.Size, p.Unpacked, p.Root, p.Loose, p.Games, p.App, p.Why = 0, 0, "", false, nil, "", ""
		if why, ok := exclude[p.Name]; ok {
			p.Why = why
		} else if err := g.pinPort(&p); err != nil {
			p.Why = err.Error()
		}
		if p.Why != "" {
			log.Printf("port %s: %s", p.Name, p.Why)
		}
		out.Ports = append(out.Ports, p)
	}
	return out, nil
}

// pinPort finds the port's release and fills in what the store installs.
func (g *gen) pinPort(p *Port) error {
	m := githubRepoRe.FindStringSubmatch(p.Upstream)
	if m == nil {
		return errors.New("its release page isn't on GitHub")
	}
	name := m[1]
	info, err := g.c.repo(name)
	if err != nil {
		if isNotFound(err) {
			return errors.New("its repository is gone or private")
		}
		return err
	}
	name = info.FullName
	rels, err := g.c.releases(name)
	if err != nil && !isNotFound(err) {
		return err
	}
	sort.SliceStable(rels, func(i, j int) bool { return rels[i].Published > rels[j].Published })
	var app *Port
	for _, pre := range []bool{false, true} {
		tried := 0
		for _, r := range rels {
			if r.Draft || r.Prerelease != pre || tried == 3 {
				continue
			}
			zips := portZips(r.Assets)
			if len(zips) == 0 {
				continue
			}
			tried++
			for _, a := range zips {
				q := *p
				q.Repo, q.Version, q.Released = name, versionOf(r.Tag, r.Name), day(r.Published)
				q.License, _ = g.licence(name, info)
				err := g.inspectPort(&q, a)
				if err != nil {
					log.Printf("port %s: %s: %v", p.Name, a.Name, err)
					continue
				}
				if q.App != "" {
					if app == nil {
						app = &q
					}
					continue
				}
				*p = q
				return nil
			}
		}
	}
	if app != nil {
		*p = *app
		return nil
	}
	if len(rels) == 0 {
		return errors.New("no releases: build it from its repository")
	}
	return errors.New("no release zip laid out for Onion's Ports list")
}

// portZips are a release's zips that may be for Onion, best first: one
// named for Onion, then for the Miyoo, then the rest; smaller first.
func portZips(as []asset) []asset {
	var out []asset
	for _, a := range as {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".zip") && !otherFirmwareRe.MatchString(a.Name) && a.Size <= maxDownload {
			out = append(out, a)
		}
	}
	rank := func(a asset) int {
		n := strings.ToLower(a.Name)
		switch {
		case strings.Contains(n, "onion"):
			return 0
		case strings.Contains(n, "miyoo"):
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i].Size < out[j].Size
	})
	return out
}

// inspectPort downloads the zip (checking GitHub's digest) and reads its
// layout: Onion's ports layout, or an Onion app.
func (g *gen) inspectPort(p *Port, a asset) error {
	dst := filepath.Join(g.zips, hashName(a.URL)+".zip")
	size, sum, err := g.c.download(a.URL, dst)
	if err != nil {
		return err
	}
	if !g.keepZips {
		defer os.Remove(dst)
	}
	if d := strings.TrimPrefix(a.Digest, "sha256:"); d != "" && !strings.EqualFold(d, sum) {
		os.Remove(dst)
		return fmt.Errorf("SHA-256 %s doesn't match GitHub's digest %s", sum, d)
	}
	zr, err := zip.OpenReader(dst)
	if err != nil {
		return errors.New("not a zip")
	}
	defer zr.Close()
	p.Zip, p.SHA256, p.Size = a.URL, sum, size
	if err := portLayout(&zr.Reader, p); err == nil {
		return nil
	} else if info := inspectZip(&zr.Reader, ""); len(info.Apps) > 0 {
		p.App = info.Apps[0].Folder
		p.Zip, p.SHA256, p.Size = "", "", 0
		return nil
	} else {
		return err
	}
}

// portLayout reads a zip in Onion's ports layout into p: its root, the
// size of what installs, and each shortcut's game.
func portLayout(zr *zip.Reader, p *Port) error {
	names := map[string]*zip.File{}
	for _, f := range zr.File {
		names[strings.ReplaceAll(f.Name, `\`, "/")] = f
	}
	root, loose := "", false
	for _, r := range []string{"Roms/PORTS/", "PORTS/", ""} {
		for n := range names {
			if strings.HasPrefix(n, r+"Shortcuts/") {
				root = r
				goto found
			}
		}
	}
	// No Shortcuts/ folder: shortcuts at the top beside Games/ (made for
	// another firmware) go in Shortcuts/.
	for _, r := range []string{"Roms/PORTS/", "PORTS/", ""} {
		games, top := false, false
		for n := range names {
			rel, ok := strings.CutPrefix(n, r)
			games = games || ok && strings.HasPrefix(rel, "Games/")
			top = top || ok && !strings.Contains(rel, "/") && strings.EqualFold(path.Ext(rel), ".port")
		}
		if games && top {
			root, loose = r, true
			goto found
		}
	}
	return errors.New("no Shortcuts/ folder for Onion's Ports list")
found:
	var unpacked int64
	var games []PortGame
	seen := map[string]bool{}
	for n, f := range names {
		if junkPath(n) || !strings.HasPrefix(n, root) || f.FileInfo().IsDir() || strings.HasSuffix(n, "/") {
			continue
		}
		rel := strings.TrimPrefix(n, root)
		if loose && !strings.Contains(rel, "/") && strings.EqualFold(path.Ext(rel), ".port") {
			rel = "Shortcuts/" + rel
		}
		top, _, _ := strings.Cut(rel, "/")
		if !slices.Contains(portAreas, top) {
			continue
		}
		if portPath(rel) == "" {
			return fmt.Errorf("unsafe path %q", n)
		}
		if seen[strings.ToLower(rel)] {
			return fmt.Errorf("%q twice (FAT ignores case)", rel)
		}
		seen[strings.ToLower(rel)] = true
		if !f.Mode().IsRegular() {
			continue
		}
		unpacked += int64(f.UncompressedSize64)
		if top != "Shortcuts" {
			continue
		}
		ext := strings.ToLower(path.Ext(rel))
		if ext != ".port" && ext != ".notfound" {
			continue
		}
		b := readZip(f, 64<<10)
		gm, err := shortcutGame(b)
		if err != nil {
			return fmt.Errorf("%s: %v", path.Base(rel), err)
		}
		gm.Shortcut = strings.TrimSuffix(strings.TrimPrefix(rel, "Shortcuts/"), path.Ext(rel))
		games = append(games, gm)
	}
	if len(games) == 0 {
		return errors.New("no .port shortcut in Shortcuts/")
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Shortcut < games[j].Shortcut })
	games = slices.CompactFunc(games, func(a, b PortGame) bool { return a.Shortcut == b.Shortcut })
	for i, gm := range games {
		games[i].Shipped = gm.Data == "" || shipsData(seen, gm)
	}
	p.Root, p.Loose, p.Unpacked, p.Games = root, loose, unpacked, games
	return nil
}

// shipsData reports whether the zip has the game's data file where
// Onion's check finds it (Games/<Dir>/ or one folder below). files holds
// the installed paths, lower case.
func shipsData(files map[string]bool, gm PortGame) bool {
	dir := strings.ToLower("Games/" + gm.Dir + "/")
	data := strings.ToLower(gm.Data)
	for f := range files {
		rest, ok := strings.CutPrefix(f, dir)
		if !ok {
			continue
		}
		if parts := strings.Split(rest, "/"); len(parts) <= 2 && parts[len(parts)-1] == data {
			return true
		}
	}
	return false
}

func junkPath(n string) bool {
	if strings.HasPrefix(n, "__MACOSX/") || strings.Contains(n, "/__MACOSX/") {
		return true
	}
	b := path.Base(n)
	return b == ".DS_Store" || strings.HasPrefix(b, "._")
}

// shortcutVar reads a NAME="value" line the way import.sh does (grep
// "NAME=" | cut -d= -f2 | grep -o '".*"'): the first such line.
func shortcutVar(b []byte, name string) string {
	re := regexp.MustCompile(`(?m)^[ \t]*` + name + `="([^"\n]*)"`)
	if m := re.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// shortcutGame reads the game folder and data file a shortcut names, with
// Onion's fallbacks (RomDir for GameDir; GameExecutable, then RomFile, for
// GameDataFile). Shortcuts for a RetroArch core, or with values the store
// can't check (shell variables, paths), are refused.
func shortcutGame(b []byte) (PortGame, error) {
	dir := shortcutVar(b, "GameDir")
	if dir == "" {
		dir = shortcutVar(b, "RomDir")
	}
	data := shortcutVar(b, "GameDataFile")
	if data == "" {
		data = shortcutVar(b, "GameExecutable")
	}
	if data == "" {
		data = shortcutVar(b, "RomFile")
	}
	if dir == "" && data == "" && shortcutVar(b, "Core") == "" {
		// A plain launcher script: Onion's launch doesn't check anything.
		return PortGame{}, nil
	}
	switch {
	case dir == "":
		return PortGame{}, errors.New("no GameDir")
	case data == "":
		return PortGame{}, errors.New("no GameDataFile")
	case strings.ContainsAny(dir, "$`/") || portPath("Games/"+dir) == "":
		return PortGame{}, fmt.Errorf("GameDir %q can't be checked", dir)
	case strings.ContainsAny(data, "$`/[]*?") || strings.HasPrefix(data, ".") || !portSegRe.MatchString(data):
		return PortGame{}, fmt.Errorf("GameDataFile %q can't be checked", data)
	}
	return PortGame{Dir: dir, Data: data}, nil
}
