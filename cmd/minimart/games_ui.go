package main

import (
	"fmt"
	"image"
	"strings"

	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
)

func init() { typeNames["games"] = "Homebrew games" }

// homebrew lists the systems that have games, then the games of the one
// chosen. Y adds a game repository.
func (s *store) homebrew() {
	var list []Game
	var errs []error
	reload := func() {
		s.load("Homebrew games", func() ([]Item, error) {
			list, errs = s.f.Games(s.src)
			return nil, nil
		})
	}
	reload()
	if len(list) == 0 {
		msg := "No games: turn on Homebrew Hub in Repositories."
		if len(errs) > 0 {
			msg = "Couldn't load the games: " + tnet.Explain(errs[0]).Error()
		}
		s.scr.Notice(s.input, "Homebrew games", msg)
		return
	}
	l := ui.List{}
	for {
		var shown []system
		var items []ui.Item
		for _, sys := range systems {
			n := 0
			for _, g := range list {
				if g.System == sys.Folder {
					n++
				}
			}
			if n == 0 {
				continue
			}
			sub := fmt.Sprintf("%d games · Roms/%s", n, sys.Folder)
			if !sys.EmulatorInstalled() {
				sub = fmt.Sprintf("%d games · add its emulator in Package Manager", n)
			}
			shown = append(shown, sys)
			items = append(items, ui.Item{Label: sys.Name, Sub: sub, Icon: "play", Chevron: true})
		}
		l.Items = items
		if l.Sel >= len(items) {
			l.Sel = 0
		}
		sc := s.scr
		top := sc.StaticFrame("Homebrew games", "Free games, shared by their authors",
			ui.Hint{Button: "A", Label: "Open"}, ui.Hint{Button: "Y", Label: "Add repository"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(sc, image.Rect(20, top, ui.W-26, ui.H-58))
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A:
			if l.Sel < len(shown) {
				var gs []Game
				for _, g := range list {
					if g.System == shown[l.Sel].Folder {
						gs = append(gs, g)
					}
				}
				s.gameList(shown[l.Sel], gs)
			}
		case ui.Y:
			if s.addGameRepo() {
				reload()
			}
		default:
			l.Handle(ev)
		}
	}
}

// gameList lists one system's games; X changes the order.
func (s *store) gameList(sys system, list []Game) {
	l := ui.List{}
	recs := loadRecords()
	o := listOrder("games", gameOrders)
	var sizes map[string]int64
	arrange := func() {
		if o == bySize && sizes == nil {
			s.load(sys.Name, func() ([]Item, error) { sizes = s.f.GameSizes(list); return nil, nil })
		}
		sortGames(list, o, sizes, func(g Game) bool { return installedFile(recs, g) != "" })
	}
	arrange()
	for {
		items := make([]ui.Item, len(list))
		for i, g := range list {
			label := g.Title
			if g.WIP {
				label += " (in progress)"
			}
			sub := g.Licence()
			if g.Author != "" {
				sub = "by " + g.Author + " · " + sub
			}
			d := ""
			if installedFile(recs, g) != "" {
				d = "Installed"
			}
			items[i] = ui.Item{Label: label, Sub: sub, Detail: d, Chevron: true}
		}
		l.Items = items
		sc := s.scr
		top := sc.StaticFrame(sys.Name, fmt.Sprintf("%d games with a licence that allows downloading · %s", len(list), orderLabel(o, false)),
			ui.Hint{Button: "A", Label: "Details"}, ui.Hint{Button: "X", Label: "Sort"}, ui.Hint{Button: "L1/R1", Label: "Page"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(sc, image.Rect(20, top, ui.W-26, ui.H-58))
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A:
			if l.Sel < len(list) {
				s.gameDetail(sys, list[l.Sel])
				recs = loadRecords()
			}
		case ui.X:
			if n := s.chooseOrder("Sort games", gameOrders, o, false); n != o {
				o = n
				saveOrder("games", o)
				arrange()
				l.Sel = 0
			}
		default:
			l.Handle(ev)
		}
	}
}

func (s *store) gameDetail(sys system, g Game) {
	var size int64 = -1
	go func() {
		n := s.f.GameSize(g)
		s.mu.Lock()
		size = n
		s.mu.Unlock()
		s.redraw()
	}()
	msg := ""
	recs := loadRecords()
	for {
		where := installedFile(recs, g)
		sc := s.scr
		sc.Background()
		sub := g.Source
		if g.Author != "" {
			sub = "by " + g.Author + " · " + g.Source
		}
		top := sc.Header(g.Title, sub)
		x, y, w := 28, top+12, ui.W-56
		desc := g.Desc
		if desc == "" {
			desc = "No description in the repository."
		}
		lines := ui.Wrap(ui.Body, desc, w)
		if len(lines) > 4 {
			lines = append(lines[:3], ui.Fit(ui.Body, lines[3]+" …", w))
		}
		for _, line := range lines {
			sc.Text(x, y, line, ui.Body, ui.Text)
			y += ui.LineHeight(ui.Body)
		}
		y += 12
		sc.Text(x, y, ui.Fit(ui.Small, "Licence: "+g.Licence(), w), ui.Small, ui.Text)
		y += ui.LineHeight(ui.Small) + 4
		s.mu.Lock()
		sz := size
		s.mu.Unlock()
		info := []string{}
		switch {
		case sz > 0:
			info = append(info, fmt.Sprintf("%.2f MB download.", float64(sz)/(1<<20)))
		case sz < 0:
			info = append(info, "Checking size…")
		}
		if g.SHA256 != "" {
			info = append(info, "Checked against the SHA-256 the repository gives.")
		} else {
			info = append(info, "The repository gives no checksum: the store records the file's SHA-256 when it installs, and tells you if a later download differs.")
		}
		if where != "" {
			info = append(info, "Installed as "+where+".")
		} else {
			info = append(info, "Not installed. It goes in Roms/"+sys.Folder+".")
		}
		if !sys.EmulatorInstalled() {
			info = append(info, "To play it, add the "+sys.Name+" emulator in Package Manager.")
		}
		for _, line := range ui.Wrap(ui.Small, strings.Join(info, " "), w) {
			sc.Text(x, y, line, ui.Small, ui.Muted)
			y += ui.LineHeight(ui.Small)
		}
		y = drawSpace(sc, x, y+6, w, downloadNeeds(sz))
		if msg != "" {
			y += 8
			for _, line := range ui.Wrap(ui.Small, msg, w) {
				sc.Text(x, y, line, ui.Small, ui.Warn)
				y += ui.LineHeight(ui.Small)
			}
		}
		hints := []ui.Hint{{Button: "A", Label: "Install"}}
		if where != "" {
			hints = []ui.Hint{{Button: "A", Label: "Reinstall"}, {Button: "X", Label: "Remove"}}
		}
		sc.Footer(append(hints, ui.Hint{Button: "B", Label: "Back"})...)
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A:
			msg = ""
			var p *pendingGame
			err := s.progress("Downloading", g.Title, func(pr func(int64, int64)) error {
				var err error
				p, err = s.f.DownloadGame(g, pr)
				return err
			})
			if err != nil {
				msg = tnet.Explain(err).Error()
				break
			}
			if p.Changed && !s.scr.Confirm(s.input, "This download is different",
				"Its SHA-256 doesn't match the copy installed on "+p.Before+". The author may have released a new version. Install it?", "Install") {
				p.Discard()
				break
			}
			names, err := p.Install()
			recs = loadRecords()
			if err != nil {
				msg = err.Error()
				break
			}
			s.scr.Notice(s.input, "Installed", fmt.Sprintf("%s is in Games › %s (Roms/%s/%s). Licence: %s.",
				g.Title, sys.Name, sys.Folder, names[0], g.Licence()))
		case ui.X:
			if where != "" && s.scr.Confirm(s.input, "Remove "+g.Title+"?", "Its file is deleted from Roms/"+sys.Folder+". Your saves stay.", "Remove") {
				if err := RemoveGame(g); err != nil {
					msg = err.Error()
				}
				recs = loadRecords()
			}
		}
	}
}

// addGameRepo adds a game repository, as Repositories › Add does for themes
// and apps: it's checked, then added once you agree to the warning.
func (s *store) addGameRepo() bool {
	in, ok := s.scr.Prompt(s.input, "Game repository", "", "https:// address of a game catalogue JSON, or a Homebrew Hub …/api/", false)
	if !ok {
		return false
	}
	u, err := NormalizeURL("games", in)
	if err != nil {
		s.scr.Notice(s.input, "Can't add it", err.Error())
		return false
	}
	for _, r := range s.src {
		if r.URL == u {
			s.scr.Notice(s.input, "Already there", r.Name+" is already in your repositories.")
			return false
		}
	}
	src := Source{Type: "games", URL: u, Enabled: true}
	var name string
	s.load("Checking", func() ([]Item, error) {
		name, err = s.f.Validate(src)
		return nil, nil
	})
	if err != nil {
		s.scr.Notice(s.input, "Can't add it", tnet.Explain(err).Error())
		return false
	}
	src.Name = name
	if !s.scr.Confirm(s.input, "Add "+name+"?", "Only games whose recorded licence allows downloading are listed, but the store can't check who made a game. "+
		"Add only repositories you trust to list homebrew shared by its authors, never commercial games.", "Add") {
		return false
	}
	s.src = append(s.src, src)
	s.saveSources()
	return true
}
