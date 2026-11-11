package main

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
)

// provideGameFile puts the user's own copy of the app's game file in
// place, if it isn't already: a matching copy on the card is taken without
// asking; then the game files location, if the user set one, after a
// reminder that it must hold their own copies; then a file they pick.
func (s *store) provideGameFile(a App) {
	nf := a.NeedsFile
	if nf == nil || nf.present() {
		return
	}
	var found string
	s.load("Looking for your copy", func() ([]Item, error) {
		found = scanCard(nf, defaultScan)
		return nil, nil
	})
	msg := ""
	if found != "" {
		err := s.progress("Copying", nf.What, func(p func(int64, int64)) error { return copyGameFile(found, nf, p) })
		if err == nil {
			s.scr.Notice(s.input, "Game file added", fmt.Sprintf("Your copy %s matches a known-good dump of %s. It was copied to %s.", cardPath(found), nf.What, nf.Path))
			return
		}
		msg = "Copying " + cardPath(found) + " failed: " + err.Error()
	}
	if loc := loadSettings().GameFiles; loc != "" && len(nf.SHA1) > 0 {
		if s.scr.Confirm(s.input, "Look in your game files?", "Ask "+loc+" for "+nf.What+"? Keep only your own copies of games you own there. The file is used only if it matches a known-good dump.", "Look there") {
			var name string
			err := s.progress("Downloading", nf.What, func(p func(int64, int64)) error {
				var err error
				name, err = s.f.FetchGameFile(loc, nf, p)
				return err
			})
			if err == nil {
				s.scr.Notice(s.input, "Game file added", fmt.Sprintf("%s from your game files matches a known-good dump of %s. It was saved as %s.", name, nf.What, nf.Path))
				return
			}
			msg = "Your game files: " + tnet.Explain(err).Error()
		}
	}
	s.askForGameFile(a, msg)
}

// cardPath is p relative to the card, for messages.
func cardPath(p string) string {
	if rel, err := filepath.Rel(sdcard(), p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// gameFileText explains what the app needs and what the store does about
// it, as rows of width w.
func gameFileText(a App, w int) []row {
	nf := a.NeedsFile
	rs := para(a.Name+" needs your own copy of "+nf.What+".", ui.Body, ui.Text, 0, w)
	rs = append(rs, para("MiniMart never downloads games or game data. Copy the dump you made of your own copy of the game onto the card (into Roms/, for example), then pick it here. It is copied to "+nf.Path+".", ui.Small, ui.Text, 10, w)...)
	if len(nf.SHA1) > 0 {
		rs = append(rs, para("It must match one of these known-good dumps (SHA-1"+byteOrderNote(nf)+"):", ui.Small, ui.Muted, 10, w)...)
		rs = append(rs, bullets(nf.SHA1, ui.Small, ui.Muted, 4, w)...)
	}
	rs = append(rs, para("Accepted: "+strings.Join(nf.exts(), ", ")+". You can also do this later from the app's page (Y).", ui.Small, ui.Muted, 10, w)...)
	return rs
}

func byteOrderNote(nf *NeedsFile) string {
	if nf.ByteOrder == "n64" {
		return " of the .z64 byte order; .v64 and .n64 dumps are converted to check them"
	}
	return ""
}

// askForGameFile explains what the app needs and lets the user pick their
// copy from the card. msg says why the automatic search found nothing.
func (s *store) askForGameFile(a App, msg string) {
	nf := a.NeedsFile
	w := ui.W - 56
	for {
		rs := gameFileText(a, w)
		if msg != "" {
			rs = append(rs, para(msg, ui.Small, ui.Warn, 12, w)...)
		}
		if !s.textScreen("Your copy of the game", a.Name, rs, "Pick a file") {
			return
		}
		p, ok := s.pickFile("Pick your copy", nf)
		if !ok {
			continue
		}
		var err error
		s.load("Checking "+filepath.Base(p), func() ([]Item, error) {
			err = checkGameFile(p, nf)
			return nil, nil
		})
		if err == nil {
			err = s.progress("Copying", nf.What, func(pr func(int64, int64)) error { return copyGameFile(p, nf, pr) })
		}
		if err != nil {
			msg = cardPath(p) + ": " + err.Error()
			continue
		}
		s.scr.Notice(s.input, "Game file added", cardPath(p)+" was copied to "+nf.Path+".")
		return
	}
}

// pickRoots are where the picker looks: Roms/ and Media/ with their
// folders, and the card's top level (its files only).
var pickRoots = []struct{ dir, label, sub string }{
	{"Roms", "Roms", "Your games' folders"},
	{"Media", "Media", "Music, videos and other files"},
	{"", "SD card", "Files at the top of the card"},
}

// pickEntry is a row of the file picker.
type pickEntry struct {
	path string
	dir  bool
	size int64
}

// pickEntries lists dir for the picker: folders (when sub is set) then the
// files with one of the file's extensions, each by name.
func pickEntries(dir string, sub bool, nf *NeedsFile) []pickEntry {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var dirs, files []pickEntry
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		switch {
		case e.IsDir():
			if sub {
				dirs = append(dirs, pickEntry{path: p, dir: true})
			}
		case e.Type().IsRegular() && nf.accepts(name):
			var n int64
			if fi, err := e.Info(); err == nil {
				n = fi.Size()
			}
			files = append(files, pickEntry{path: p, size: n})
		}
	}
	byName := func(es []pickEntry) {
		sort.Slice(es, func(i, j int) bool {
			return strings.ToLower(filepath.Base(es[i].path)) < strings.ToLower(filepath.Base(es[j].path))
		})
	}
	byName(dirs)
	byName(files)
	return append(dirs, files...)
}

// pickFile lets the user choose a file with one of the extensions from
// Roms/, Media/ or the card's top level. B goes up a folder, and from the
// top back out.
func (s *store) pickFile(title string, nf *NeedsFile) (string, bool) {
	for {
		items := make([]ui.Item, len(pickRoots))
		for i, r := range pickRoots {
			items[i] = ui.Item{Label: r.label, Sub: r.sub, Icon: "list", Chevron: true}
		}
		i, ok := s.scr.Choose(s.input, title, items)
		if !ok {
			return "", false
		}
		r := pickRoots[i]
		if p, ok := s.pickIn(title, filepath.Join(sdcard(), r.dir), r.dir != "", nf); ok {
			return p, true
		}
	}
}

// pickIn browses from root (into folders when sub is set) and returns the
// file chosen, or false when B leaves root.
func (s *store) pickIn(title, root string, sub bool, nf *NeedsFile) (string, bool) {
	dir := root
	var sels []int
	l := ui.List{}
	for {
		es := pickEntries(dir, sub, nf)
		items := make([]ui.Item, len(es))
		for i, e := range es {
			if e.dir {
				items[i] = ui.Item{Label: filepath.Base(e.path), Icon: "list", Chevron: true}
			} else {
				items[i] = ui.Item{Label: filepath.Base(e.path), Detail: sizeText(e.size)}
			}
		}
		l.Items = items
		sc := s.scr
		top := sc.StaticFrame(title, cardPath(dir)+" · "+strings.Join(nf.exts(), ", "),
			ui.Hint{Button: "A", Label: "Open"}, ui.Hint{Button: "B", Label: "Up"})
		if len(items) == 0 {
			sc.TextCenter(ui.W/2, top+60, "No folders or "+strings.Join(nf.exts(), ", ")+" files here", ui.Small, ui.Muted)
		} else {
			l.Draw(sc, image.Rect(20, top, ui.W-26, ui.H-58))
		}
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return "", false
		}
		if !ev.Pressed {
			continue
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			if dir == root {
				return "", false
			}
			dir = filepath.Dir(dir)
			l = ui.List{}
			if n := len(sels); n > 0 {
				l.Sel, sels = sels[n-1], sels[:n-1]
			}
		case ui.A:
			if l.Sel >= len(es) {
				break
			}
			e := es[l.Sel]
			if !e.dir {
				return e.path, true
			}
			sels = append(sels, l.Sel)
			dir = e.path
			l = ui.List{}
		default:
			handle(&l, ev)
		}
	}
}

// gameFilesRow is the Repositories row for the game files location.
func gameFilesRow() ui.Item {
	loc := loadSettings().GameFiles
	it := ui.Item{Label: "Your game files", Sub: "A folder on your own server, for games you own", Detail: "Not set", Chevron: true}
	if loc != "" {
		it.Sub = loc
		it.Detail = "Set"
	}
	return it
}

// editGameFiles asks for the game files location: a folder address on the
// user's own server (or tailnet) holding copies of games they own. An empty
// address turns it off.
func (s *store) editGameFiles() {
	cur := loadSettings().GameFiles
	in, ok := s.scr.Prompt(s.input, "Your game files", cur, "http(s):// folder on your own server; empty to turn off", false)
	if !ok {
		return
	}
	u, err := normalizeGameFiles(in)
	if err != nil {
		s.scr.Notice(s.input, "Can't use it", err.Error())
		return
	}
	if u != "" && !s.scr.Confirm(s.input, "Use "+u+"?", "When an app needs a file from a game, MiniMart asks this folder for it. Keep only your own copies of games you own there. A file is used only if it matches a known-good dump.", "Use it") {
		return
	}
	if err := updateSettings(func(st *settings) { st.GameFiles = u }); err != nil {
		s.scr.Notice(s.input, "Your game files", "Couldn't save: "+err.Error())
	}
}
