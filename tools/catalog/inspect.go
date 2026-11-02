package main

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// An Onion app is a folder App/<Folder>/ on the card with a config.json
// that has a "launch" entry. A zip can hold one in several layouts, which
// the store unpacks as follows (see App.Strip and App.Wrap in the store):
//
//	App/<Folder>/config.json        strip "App/"
//	<Folder>/config.json            strip ""
//	x/y/App/<Folder>/config.json    strip "x/y/App/"
//	config.json                     wrap: the files go into App/<Folder>/
//	x/App/config.json               wrap, strip "x/App/"
//	<repo>-<sha>/config.json        wrap, strip "<repo>-<sha>/" (a commit)

// layout is where an app is in a zip and how the store unpacks it.
type layout struct {
	Dir    string // the zip folder holding config.json, "" for the root
	Strip  string
	Wrap   bool
	Folder string
}

// launchRe is the store's test for config.json's launch entry.
var launchRe = regexp.MustCompile(`"launch"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// folderRe is the store's rule for an app folder name.
var folderRe = regexp.MustCompile(`^[A-Za-z0-9_()+-]([A-Za-z0-9 ._()+-]{0,62}[A-Za-z0-9_()+-])?$`)

// notAppDirs are zip folders whose config.json files belong to something
// other than an app: emulators, RetroArch apps, Onion's own files, themes.
var notAppDirs = map[string]bool{
	"Emu": true, "RApp": true, ".tmp_update": true, "Roms": true, "Themes": true, "Icons": true,
	"BIOS": true, "Saves": true, "Emus": true, "Tools": true, ".github": true, "__MACOSX": true,
	"miyoo": true, "RetroArch": true, "Apps": true, "MinUI": true, ".system": true,
}

// reservedFolders are App folders of Onion's own apps and of MiniDeck's
// apps: a community app installed into one would overwrite them.
var reservedFolders = []string{
	// Onion (OnionUI/Onion static/build and static/packages)
	"240pSuite", "AdvanceMENU", "BatteryMonitorUI", "Clock", "Commander_Italic", "Expert_Mode", "FFplay", "Gmu",
	"Green", "Guest_Mode", "Onion_Manual", "OtaUpdate", "PackageManager", "PlayActivity", "RandomGamePicker",
	"RetroArch", "Screenshots_Viewer", "StartGameSwitcher", "Terminal", "ThemeSwitcher", "Tweaks",
	// MiniDeck
	"MiniScale", "MiniRack", "MiniTape", "MiniKey", "MiniTop", "MiniMart", "MiniBook", "MiniRadar", "MiniPulse",
}

func reserved(folder string) bool {
	for _, r := range reservedFolders {
		if strings.EqualFold(r, folder) {
			return true
		}
	}
	return false
}

// junk is macOS clutter.
func junk(name string) bool {
	if name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
		return true
	}
	base := path.Base(strings.TrimSuffix(name, "/"))
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

// appDirs returns the zip folders that hold an app's config.json (one with
// a launch entry, as hasLaunch reports), leaving out folders inside another
// app's and folders of things that aren't apps.
func appDirs(names []string, hasLaunch func(string) bool) []string {
	var dirs []string
	for _, n := range names {
		if path.Base(n) != "config.json" || junk(n) || strings.Contains(n, `\`) {
			continue
		}
		dir := strings.TrimSuffix(n, "config.json")
		skip := false
		for _, seg := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
			if notAppDirs[seg] || strings.HasSuffix(seg, ".pak") {
				skip = true
			}
		}
		if !skip && hasLaunch(n) {
			dirs = append(dirs, dir)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) < len(dirs[j]) || len(dirs[i]) == len(dirs[j]) && dirs[i] < dirs[j]
	})
	var out []string
	for _, d := range dirs {
		inside := false
		for _, o := range out {
			if o == "" || strings.HasPrefix(d, o) {
				inside = true
			}
		}
		if !inside {
			out = append(out, d)
		}
	}
	return out
}

// layoutOf is how the store unpacks the app whose config.json is in dir.
// archiveTop is a commit archive's top folder ("repo-sha/"), "" for a
// release zip. A folder of its own gives Strip and Folder; the zip's root,
// an "App" folder or the archive's top folder (no folder of its own) gives
// Wrap, with Folder chosen later (see wrapFolder).
func layoutOf(dir, archiveTop string) layout {
	segs := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	last := segs[len(segs)-1]
	if dir == "" || last == "App" || dir == archiveTop {
		return layout{Dir: dir, Strip: dir, Wrap: true}
	}
	parent := strings.TrimSuffix(dir, last+"/")
	return layout{Dir: dir, Strip: parent, Folder: last}
}

// folderFromName makes a folder name from a label or repository name:
// letters, digits, '-' and '_' only, words run together.
func folderFromName(s string) string {
	var b strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// appPathRe finds the App folder a script or config.json names.
var appPathRe = regexp.MustCompile(`(?:/mnt/SDCARD/|\.\./\.\./|^|[\s"'=])App/([A-Za-z0-9 ._()+-]+?)/`)

// expectedFolder is the App folder the app's own files name most often
// ("/mnt/SDCARD/App/X/..." in its scripts, "App/X/icon.png" in its
// config.json), or "" if they name none.
func expectedFolder(files map[string][]byte) string {
	count := map[string]int{}
	for name, b := range files {
		if !textual(name) {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			for _, m := range appPathRe.FindAllStringSubmatch(line, -1) {
				if folderRe.MatchString(m[1]) && !strings.Contains(m[1], "$") {
					count[m[1]]++
				}
			}
		}
	}
	best, n := "", 0
	for f, c := range count {
		if c > n || c == n && f < best {
			best, n = f, c
		}
	}
	return best
}

// wrapFolder chooses the folder for an app that has none of its own in the
// zip: the one its files name, else one made from its label, else from the
// repository's name; never a reserved one.
func wrapFolder(files map[string][]byte, label, repo string) string {
	for _, f := range []string{expectedFolder(files), folderFromName(label), folderFromName(path.Base(repo))} {
		if f != "" && folderRe.MatchString(f) && !reserved(f) {
			return f
		}
	}
	return ""
}

// config is the part of an Onion config.json the tool reads.
type config struct {
	Label  string `json:"label"`
	Desc   string `json:"description"`
	Launch string `json:"launch"`
	Icon   string `json:"icon"`
}

// readConfig reads config.json leniently: Onion reads it with its own
// parser, so a file a strict parser refuses still counts if the store's
// launch pattern finds a launch entry.
func readConfig(b []byte) (config, bool) {
	var c config
	if json.Unmarshal(b, &c) == nil && c.Launch != "" {
		return c, true
	}
	m := launchRe.FindSubmatch(b)
	if m == nil {
		return config{}, false
	}
	c.Launch = string(m[1])
	if lm := regexp.MustCompile(`"label"\s*:\s*"((?:[^"\\]|\\.)*)"`).FindSubmatch(b); lm != nil {
		c.Label = string(lm[1])
	}
	return c, true
}

// zipApp is one app found in a zip: its layout, config, and its files'
// contents, as the evidence for its facts. Text files are kept whole (up
// to a limit); programs only as their printable strings and the libraries
// they load.
type zipApp struct {
	layout
	Config config
	Files  map[string][]byte   // text files, by path inside the app folder
	Bins   map[string][]string // programs: their strings
	Libs   map[string][]string // programs: the shared libraries they load
	Names  []string            // every file in the app folder
}

// zipInfo is what a zip holds.
type zipInfo struct {
	Root string // a commit archive's top folder, "" for a release zip
	Apps []zipApp
	Boot map[string][]byte // scripts in .tmp_update/startup/, by name
	Top  []string          // its top-level folders (inside Root)
}

// textual reports whether a file is read as text: scripts, settings and
// documents.
func textual(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".sh", ".py", ".lua", ".json", ".cfg", ".conf", ".ini", ".txt", ".md", ".js", ".toml", ".yaml", ".yml", ".xml", "":
		return true
	}
	return false
}

const (
	maxText = 1 << 20  // largest text file read
	maxBin  = 48 << 20 // largest program read for its strings
)

// inspectZip finds the apps in a zip.
func inspectZip(zr *zip.Reader, archiveTop string) zipInfo {
	info := zipInfo{Root: archiveTop}
	byName := map[string]*zip.File{}
	var names []string
	top := map[string]bool{}
	for _, f := range zr.File {
		byName[f.Name] = f
		names = append(names, f.Name)
		if junk(f.Name) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, archiveTop)
		if t, rest, dir := strings.Cut(rel, "/"); t != "" && dir && (rest != "" || f.FileInfo().IsDir()) {
			top[t] = true // a folder: files inside it, or a folder entry
		}
		if dir, name := path.Split(rel); (dir == ".tmp_update/startup/" || strings.HasSuffix(dir, "/.tmp_update/startup/")) && strings.HasSuffix(name, ".sh") {
			if info.Boot == nil {
				info.Boot = map[string][]byte{}
			}
			info.Boot[name] = readZip(f, maxText)
		}
	}
	for t := range top {
		info.Top = append(info.Top, t)
	}
	sort.Strings(info.Top)
	hasLaunch := func(n string) bool {
		_, ok := readConfig(readZip(byName[n], 64<<10))
		return ok
	}
	for _, dir := range appDirs(names, hasLaunch) {
		a := zipApp{layout: layoutOf(dir, archiveTop), Files: map[string][]byte{}, Bins: map[string][]string{}, Libs: map[string][]string{}}
		a.Config, _ = readConfig(readZip(byName[dir+"config.json"], 64<<10))
		for _, f := range zr.File {
			if !strings.HasPrefix(f.Name, dir) || junk(f.Name) || f.FileInfo().IsDir() || !f.Mode().IsRegular() {
				continue
			}
			rel := strings.TrimPrefix(f.Name, dir)
			a.Names = append(a.Names, rel)
			if f.UncompressedSize64 > maxBin {
				continue
			}
			head := readZip(f, 4)
			switch {
			case bytes.HasPrefix(head, []byte("\x7fELF")):
				b := readZip(f, maxBin)
				a.Bins[rel] = telling(printable(b, 6))
				if ef, err := elf.NewFile(bytes.NewReader(b)); err == nil {
					a.Libs[rel], _ = ef.ImportedLibraries()
				}
			case textual(rel) && f.UncompressedSize64 <= maxText:
				a.Files[rel] = readZip(f, maxText)
			}
		}
		info.Apps = append(info.Apps, a)
	}
	return info
}

// readZip reads up to n bytes of a zip entry.
func readZip(f *zip.File, n int64) []byte {
	if f == nil {
		return nil
	}
	r, err := f.Open()
	if err != nil {
		return nil
	}
	defer r.Close()
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return b
}

// telling keeps the strings of a program that can be evidence: addresses
// and paths.
func telling(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.Contains(s, "://") || strings.Contains(s, "/mnt/") || strings.Contains(s, "App/") || strings.Contains(s, ".tmp_update") || strings.Contains(s, "/appconfigs") || updatePhraseRe.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// printable returns the runs of at least min printable ASCII characters in
// b, as the strings tool does.
func printable(b []byte, min int) []string {
	var out []string
	start := -1
	for i, c := range b {
		if c >= 0x20 && c < 0x7f {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= min {
			out = append(out, string(b[start:i]))
		}
		start = -1
	}
	if start >= 0 && len(b)-start >= min {
		out = append(out, string(b[start:]))
	}
	return out
}
