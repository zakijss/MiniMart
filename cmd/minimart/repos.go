package main

import (
	"image"

	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
)

var typeNames = map[string]string{"themes": "Themes and icon packs", "apps": "App catalogue", "ports": "Ports list"}

// repos lists the repositories: A turns one on or off, X removes one you
// added, Y adds one. The last row is the user's game files location (see
// gamefile.go); A edits it.
func (s *store) repos() {
	l := ui.List{}
	for {
		items := make([]ui.Item, len(s.src), len(s.src)+1)
		for i, r := range s.src {
			state := "Off"
			if r.Enabled {
				state = "On"
			}
			sub := typeNames[r.Type]
			if r.Builtin {
				sub += " · built in"
			}
			items[i] = ui.Item{Label: r.Name, Sub: sub, Detail: state}
		}
		l.Items = append(items, gameFilesRow())
		sc := s.scr
		top := sc.StaticFrame("Repositories", "Where the store looks for themes and apps",
			ui.Hint{Button: "A", Label: "On / off"}, ui.Hint{Button: "Y", Label: "Add"}, ui.Hint{Button: "X", Label: "Remove"}, ui.Hint{Button: "B", Label: "Back"})
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
			if l.Sel >= len(s.src) {
				s.editGameFiles()
				break
			}
			s.src[l.Sel].Enabled = !s.src[l.Sel].Enabled
			s.saveSources()
		case ui.X:
			if l.Sel >= len(s.src) {
				break
			}
			r := s.src[l.Sel]
			if r.Builtin {
				s.scr.Notice(s.input, r.Name, "Built-in repositories can be turned off (A), not removed.")
			} else if s.scr.Confirm(s.input, "Remove "+r.Name+"?", "Its themes and apps stop showing in the store. Anything installed stays.", "Remove") {
				s.src = append(s.src[:l.Sel], s.src[l.Sel+1:]...)
				s.saveSources()
				if l.Sel >= len(s.src) {
					l.Sel = len(s.src) - 1
				}
			}
		case ui.Y:
			s.addRepo()
		default:
			l.Handle(ev)
		}
	}
}

func (s *store) saveSources() {
	if err := SaveSources(s.src); err != nil {
		s.scr.Notice(s.input, "Repositories", "Couldn't save: "+err.Error())
	}
}

func (s *store) addRepo() {
	i, ok := s.scr.Choose(s.input, "Add a repository", []ui.Item{
		{Label: "Theme repository", Sub: "Laid out like OnionUI/Themes, e.g. owner/repo", Icon: "star"},
		{Label: "App catalogue", Sub: "A JSON file of apps with checksums", Icon: "list"},
	})
	if !ok {
		return
	}
	typ := []string{"themes", "apps"}[i]
	hint := "owner/repo, or the raw URL of a theme repository"
	if typ == "apps" {
		hint = "https:// address of the catalogue JSON"
	}
	in, ok := s.scr.Prompt(s.input, "Repository address", "", hint, false)
	if !ok {
		return
	}
	u, err := NormalizeURL(typ, in)
	if err != nil {
		s.scr.Notice(s.input, "Can't add it", err.Error())
		return
	}
	for _, r := range s.src {
		if r.URL == u {
			s.scr.Notice(s.input, "Already there", r.Name+" is already in your repositories.")
			return
		}
	}
	src := Source{Type: typ, URL: u, Enabled: true}
	var name string
	s.load("Checking", func() ([]Item, error) {
		name, err = s.f.Validate(src)
		return nil, nil
	})
	if err != nil {
		s.scr.Notice(s.input, "Can't add it", tnet.Explain(err).Error())
		return
	}
	src.Name = name
	warn := "Its themes and apps appear in the store."
	if typ == "apps" {
		warn = "Apps from it are installed only if their checksum matches. Add only catalogues you trust: apps run with full access to the device."
	}
	if !s.scr.Confirm(s.input, "Add "+name+"?", warn, "Add") {
		return
	}
	s.src = append(s.src, src)
	s.saveSources()
}
