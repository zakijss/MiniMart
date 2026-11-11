package main

import (
	"fmt"
	"image"
	"strings"

	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
)

// ports lists the ports: those with a pinned release install from here,
// app releases open in Apps, the rest are a guide to where to get them.
func (s *store) ports() {
	// fresh: download the list now instead of showing the copy on the
	// card first
	var stale error // why the last START couldn't download the list
	get := func(fresh bool) (list []Port, err error) {
		s.load("Ports", func() ([]Item, error) {
			if fresh {
				stale = s.f.refresh(s.src)
			}
			list, err = s.f.Ports(s.src)
			return nil, nil
		})
		return list, err
	}
	list, err := get(false)
	for len(list) == 0 {
		if err == nil {
			s.scr.Notice(s.input, "Ports", "Turn on the ports list in Repositories.")
			return
		}
		if !s.retry("Ports", "Couldn't load the ports list: "+tnet.Explain(err).Error()) {
			return
		}
		list, err = get(true)
	}
	l := ui.List{}
	for {
		recs := loadPortRecords()
		items := make([]ui.Item, len(list))
		n := 0
		for i, p := range list {
			syncShortcuts(p)
			sub, detail := portLine(p, p.State(recs))
			if p.Installable() {
				n++
			}
			items[i] = ui.Item{Label: p.Name, Sub: sub, Detail: detail, Chevron: true}
		}
		l.Items = items
		sc := s.scr
		free, _ := spaceText(0)
		top := sc.StaticFrame("Ports", fmt.Sprintf("%d install from here · %s", n, free),
			ui.Hint{Button: "A", Label: "Details"}, ui.Hint{Button: "START", Label: "Refresh"}, ui.Hint{Button: "L1/R1", Label: "Page"}, ui.Hint{Button: "B", Label: "Back"})
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
			s.portDetail(list[l.Sel])
		case ui.Start:
			nl, err := get(true)
			if len(nl) == 0 {
				msg := "Turn on the ports list in Repositories."
				if err != nil {
					msg = "Couldn't load the ports list: " + tnet.Explain(err).Error() + ". The list stays as it was."
				}
				s.scr.Notice(s.input, "Ports", msg)
				break
			}
			sel := list[l.Sel].key()
			list, l.Sel = nl, 0
			for i, p := range list {
				if p.key() == sel {
					l.Sel = i
				}
			}
			if stale != nil {
				s.scr.Notice(s.input, "Ports", "Couldn't download the list, so the copy on the card is shown: "+tnet.Explain(stale).Error())
			}
		default:
			handle(&l, ev)
		}
	}
}

// portLine is a port's second line and right-hand state in the list.
func portLine(p Port, st PortState) (sub, detail string) {
	var parts []string
	switch {
	case p.App != "":
		parts = append(parts, "App: installs from Apps")
	case !p.Installable():
		parts = append(parts, "Guide only")
	case p.needsOwnFiles():
		parts = append(parts, "Needs your own game files")
	default:
		parts = append(parts, "Free game")
	}
	if p.Status != "" && p.Status != "playable" {
		parts = append(parts, p.Status)
	}
	var cats []string
	for _, c := range p.Categories {
		if c != "app" {
			cats = append(cats, c)
		}
	}
	if len(cats) > 0 {
		parts = append(parts, strings.Join(cats, ", "))
	}
	switch {
	case st.Ready():
		detail = "Ready"
	case len(st.Missing) > 0:
		detail = "Add files"
	case st.Installed || st.ByHand:
		detail = "Installed"
	}
	return strings.Join(parts, " · "), detail
}

// stateText says what is on the card for the port.
func stateText(p Port, st PortState) (string, bool) {
	switch {
	case !st.Installed && !st.ByHand:
		return "Not installed", false
	case len(st.Missing) > 0 && !p.needsOwnFiles():
		return "Installed, but it still needs " + missingText(st.Missing) + ": get it from its release page (" + p.Upstream + ").", true
	case len(st.Missing) > 0:
		return "Installed, waiting for your game files: " + missingText(st.Missing), true
	case len(st.Unchecked) > 0:
		return "Installed. Add your own game files as its notes say (in Roms/PORTS/Games/" + st.Unchecked[0].Dir + "/): MiniMart can't check for them.", false
	case st.ByHand:
		return "On the card (installed by hand): ready to play from Onion's Ports list", false
	}
	return "Installed " + versionLabel(st.Version) + ": ready to play from Onion's Ports list", false
}

// missingText names the files the games still need and where they go.
func missingText(gs []PortGame) string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Data+" in Roms/PORTS/Games/"+g.Dir+"/")
	}
	return strings.Join(out, "; ")
}

// portDetail shows a port; A installs or updates it, X removes it, Y checks
// again for the user's game files.
func (s *store) portDetail(p Port) {
	msg := ""
	off := 0
	w := ui.W - 56
	for {
		syncShortcuts(p)
		st := p.State(loadPortRecords())
		var rs []row
		if p.Notes != "" {
			rs = append(rs, para(p.Notes, ui.Body, ui.Text, 0, w)...)
		}
		switch {
		case p.App != "":
			rs = append(rs, para("Its release is an Onion app: A opens it in Apps, where it installs like any other.", ui.Small, ui.Accent, 8, w)...)
		case !p.Installable():
			rs = append(rs, para("MiniMart can't install this one: "+p.installErr().Error()+". Get it from the address below.", ui.Small, ui.Warn, 8, w)...)
		case p.needsOwnFiles():
			rs = append(rs, para("Commercial game: MiniMart installs the port only. Its game files come from your own copy, never from MiniMart.", ui.Small, ui.Warn, 8, w)...)
		default:
			rs = append(rs, para("Free game: everything it needs comes with it.", ui.Small, ui.Accent, 8, w)...)
		}
		gap := 12
		add := func(label, value string, warn bool) {
			c := ui.Text
			if warn {
				c = ui.Warn
			}
			rs = append(rs, kv(label, value, c, gap, w)...)
			gap = 6
		}
		if len(p.Games) > 0 || st.Installed {
			t, warn := stateText(p, st)
			add("On this Miyoo", t, warn)
		}
		if p.Installable() {
			t, short := spaceText(portNeeds(p))
			add("Space", t, short)
			add("Download", humanSize(p.Size)+", "+humanSize(p.Unpacked)+" unpacked", false)
			rel := versionLabel(p.Version)
			if d := releaseDate(p.Released); d != "" {
				rel += ", released " + d
			}
			add("Release", rel, false)
			if l := strings.TrimSpace(p.License); l == "" || strings.EqualFold(l, "none") {
				add("Licence", noLicenceText, true)
			} else {
				add("Licence", p.License, false)
			}
		}
		if p.Repo != "" {
			add("Repository", "github.com/"+p.Repo, false)
		} else {
			add("Get it from", p.Upstream, false)
		}
		if len(p.Porter) > 0 {
			add("Ported by", strings.Join(p.Porter, ", "), false)
		}
		if p.Status != "" {
			add("Status", p.Status, false)
		}
		if p.Installable() {
			rs = append(rs, para("Not reviewed by MiniDeck: read its repository before installing.", ui.Small, ui.Muted, 10, w)...)
		}
		if msg != "" {
			rs = append(rs, para(msg, ui.Small, ui.Warn, 10, w)...)
		}

		sc := s.scr
		sc.Background()
		top := sc.Header(p.Name, p.Source)
		maxOff := drawRows(sc, rs, off, 28, top+10, ui.H-62)
		off = min(off, maxOff)
		var hints []ui.Hint
		switch {
		case p.App != "":
			hints = append(hints, ui.Hint{Button: "A", Label: "Open in Apps"})
		case p.Installable():
			act := "Install"
			if st.Installed {
				act = "Update"
				if st.Version == p.Version {
					act = "Reinstall"
				}
			}
			hints = append(hints, ui.Hint{Button: "A", Label: act})
		}
		if st.Installed {
			hints = append(hints, ui.Hint{Button: "X", Label: "Remove"})
		}
		if (st.Installed || st.ByHand) && len(st.Missing) > 0 {
			hints = append(hints, ui.Hint{Button: "Y", Label: "Check"})
		}
		if maxOff > 0 && len(hints) < 3 {
			hints = append(hints, ui.Hint{Button: "UP/DOWN", Label: "Scroll"})
		}
		sc.Footer(append(hints, ui.Hint{Button: "B", Label: "Back"})...)
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		if !ev.Pressed {
			continue
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.Up:
			off = max(0, off-1)
		case ui.Down:
			off = min(maxOff, off+1)
		case ui.L1, ui.Left:
			off = max(0, off-pageRows)
		case ui.R1, ui.Right:
			off = min(maxOff, off+pageRows)
		case ui.A:
			msg = ""
			switch {
			case p.App != "":
				msg = s.openApp(p)
			case p.Installable():
				msg = s.installPort(p, st)
			}
		case ui.X:
			if !st.Installed {
				break
			}
			if s.scr.Confirm(s.input, "Remove "+p.Name+"?", "MiniMart deletes the files it installed. Files you added (the game's own files, saves, settings) stay.", "Remove") {
				if err := RemovePort(p); err != nil {
					msg = err.Error()
				} else if n := userFilesLeft(p); n > 0 {
					what := fmt.Sprintf("%d files you added are", n)
					if n == 1 {
						what = "1 file you added is"
					}
					s.scr.Notice(s.input, "Removed", fmt.Sprintf("%s is removed. %s still in Roms/PORTS/Games: delete them in MiniTop › Storage if you don't want them.", p.Name, what))
				}
			}
		case ui.Y:
			if len(st.Missing) == 0 {
				break
			}
			if missing := activate(p.Games); len(missing) > 0 {
				msg = "Still missing: " + missingText(missing) + "."
			} else {
				s.scr.Notice(s.input, "Ready", p.Name+" found its game files: play it from Onion's Ports list.")
			}
		}
	}
}

// openApp opens the app a port's release is in, from the app catalogues.
func (s *store) openApp(p Port) string {
	var list []App
	s.load("Apps", func() ([]Item, error) {
		list, _ = s.f.Apps(s.src)
		return nil, nil
	})
	for _, a := range list {
		if a.Folder == p.App && (p.Repo == "" || strings.EqualFold(a.Repo, p.Repo)) {
			s.appDetail(a)
			return ""
		}
	}
	for _, a := range list {
		if a.Folder == p.App {
			s.appDetail(a)
			return ""
		}
	}
	return "It isn't in the app catalogues that are turned on. Get it from: " + p.Upstream
}

// installPort asks, downloads, checks and installs the port, and says what
// happened. It returns a message for the details page on failure.
func (s *store) installPort(p Port, st PortState) string {
	w := ui.W - 56
	verb := "Install"
	if st.Installed {
		verb = "Update"
	}
	var rs []row
	rs = append(rs, kv("Repository", "github.com/"+p.Repo, ui.Text, 0, w)...)
	if l := strings.TrimSpace(p.License); l == "" || strings.EqualFold(l, "none") {
		rs = append(rs, kv("Licence", noLicenceText, ui.Warn, 6, w)...)
	} else {
		rs = append(rs, kv("Licence", p.License, ui.Text, 6, w)...)
	}
	rs = append(rs, kv("Goes in", "Roms/PORTS (Games, Imgs, Shortcuts, Manuals)", ui.Text, 6, w)...)
	if t, short := spaceText(portNeeds(p)); short {
		return t
	}
	rs = append(rs, spaceRows(portNeeds(p), 6, w)...)
	rs = append(rs, para("Not reviewed by MiniDeck. It runs with full access to the device, like any port. The download is installed only if it matches the checksum in the ports list.", ui.Small, ui.Muted, 12, w)...)
	if p.needsOwnFiles() {
		rs = append(rs, para("You need your own copy of the game: MiniMart installs only the port.", ui.Small, ui.Warn, 8, w)...)
	}
	if !s.textScreen(verb+" "+p.Name+"?", "From "+p.Source, rs, verb) {
		return ""
	}
	var pp *pendingPort
	err := s.progress("Downloading", p.Name, func(pr func(int64, int64)) error {
		var err error
		pp, err = s.f.DownloadPort(p, pr)
		return err
	})
	if err != nil {
		return tnet.Explain(err).Error()
	}
	if len(pp.Replaces) > 0 {
		first := pp.Replaces[0]
		more := ""
		if len(pp.Replaces) > 1 {
			more = fmt.Sprintf(" and %d more", len(pp.Replaces)-1)
		}
		if !s.scr.Confirm(s.input, "Replace files?", fmt.Sprintf("%d files of a copy already on the card (Roms/PORTS/%s%s) would be replaced. Your saves and game files with other names stay.", len(pp.Replaces), first, more), "Replace") {
			pp.Discard()
			return ""
		}
	}
	var missing []PortGame
	err = s.progress("Installing", p.Name, func(pr func(int64, int64)) error {
		var err error
		missing, err = pp.Install(pr)
		return err
	})
	if err != nil {
		return err.Error()
	}
	if len(missing) == 0 {
		if st := p.State(loadPortRecords()); len(st.Unchecked) > 0 {
			s.scr.Notice(s.input, "Port installed", fmt.Sprintf("%s %s is installed and matched its checksum. Copy your own game files into Roms/PORTS/Games/%s/ as its notes say, then play it from Onion's Ports list.", p.Name, versionLabel(p.Version), st.Unchecked[0].Dir))
			return ""
		}
		s.scr.Notice(s.input, "Installed", p.Name+" "+versionLabel(p.Version)+" is installed and matched its checksum. Play it from Onion's Ports list.")
		return ""
	}
	if !p.needsOwnFiles() {
		s.scr.Notice(s.input, "Port installed", fmt.Sprintf("%s %s is installed, but it still needs %s, from its release page (%s). Then press Y here.", p.Name, versionLabel(p.Version), missingText(missing), p.Upstream))
		return ""
	}
	s.scr.Notice(s.input, "Port installed", fmt.Sprintf("%s %s is installed. To play, copy your own game files in: %s. Then press Y here, or run ~Import ports in Onion's Ports list.", p.Name, versionLabel(p.Version), missingText(missing)))
	return ""
}
