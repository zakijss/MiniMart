package main

// Updates: everything installed from the store that has a newer version
// in the repositories, updated one at a time or all together. An update
// is the same install as from the item's own page, so each download is
// checked against its pinned SHA-256. Themes, icon packs and homebrew
// games have no versions in their repositories, so they aren't listed.

import (
	"fmt"
	"image"
	"strings"

	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
)

// update is one installed item with a newer version.
type update struct {
	Kind      string // "MiniDeck app", "Community app", "Port"
	Name      string
	Installed string
	Available string
	app       *App
	port      *Port
	size      int64
}

// self reports whether the update is MiniMart itself.
func (u update) self() bool { return u.app != nil && u.app.Folder == "MiniMart" }

// sameVersion compares versions as people write them: "v1.0" and "1.0"
// are the same.
func sameVersion(a, b string) bool {
	norm := func(s string) string { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "v") }
	return norm(a) == norm(b)
}

// findUpdates lists the installed items with another version available.
// Apps whose installed version isn't known (unzipped by hand, no
// version.txt) are left out: there is nothing to compare. errs are the
// repositories that couldn't be read.
func (f *fetcher) findUpdates(ss []Source) ([]update, []error) {
	f.refresh(ss)
	var out []update
	apps, errs := f.Apps(ss)
	seen := map[string]bool{}
	for i := range apps {
		a := apps[i]
		if seen[a.Folder] || !a.Installed() {
			continue
		}
		have := a.InstalledVersion()
		if have == "" {
			continue
		}
		seen[a.Folder] = true // the first catalogue listing a folder wins, as in Apps
		avail, size := a.Version, a.Size
		if a.Manifest != "" {
			m, err := f.Manifest(a)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
				continue
			}
			avail, size = m.Version, m.Assets.Zip.Size
		}
		if avail == "" || sameVersion(have, avail) {
			continue
		}
		kind := "Community app"
		if a.Own {
			kind = "MiniDeck app"
		}
		out = append(out, update{Kind: kind, Name: a.Name, Installed: have, Available: avail, app: &a, size: size})
	}
	ports, err := f.Ports(ss)
	if err != nil {
		errs = append(errs, err)
	}
	recs := loadPortRecords()
	for i := range ports {
		p := ports[i]
		r, ok := recs[p.key()]
		if !ok || !p.Installable() || sameVersion(r.Version, p.Version) {
			continue
		}
		out = append(out, update{Kind: "Port", Name: p.Name, Installed: r.Version, Available: p.Version, port: &p, size: p.Size})
	}
	// MiniDeck's apps first, then community apps, then ports; MiniMart
	// itself last, so Update all finishes the others with the running copy.
	rank := func(u update) int {
		switch {
		case u.self():
			return 9
		case u.Kind == "MiniDeck app":
			return 0
		case u.Kind == "Community app":
			return 1
		}
		return 2
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank(out[j]) < rank(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, errs
}

// refresh reads the enabled app catalogues and ports lists again now,
// rather than the copies cached on the card (which the store otherwise
// shows first and refreshes behind the scenes). One that can't be read
// keeps its cached copy.
func (f *fetcher) refresh(ss []Source) {
	for _, s := range ss {
		if !s.Enabled || s.Type != "apps" && s.Type != "ports" {
			continue
		}
		u := s.URL
		if u == builtinCatalog {
			u = communityCatalog
		}
		if b, err := f.get(u); err == nil {
			f.cache.Put(u, b)
		}
	}
}

// apply installs one update. It returns the version installed.
func (s *store) apply(u update, p func(int64, int64)) (string, error) {
	if u.app != nil {
		return s.f.InstallApp(*u.app, p)
	}
	pp, err := s.f.DownloadPort(*u.port, p)
	if err != nil {
		return "", err
	}
	if _, err := pp.Install(p); err != nil {
		return "", err
	}
	return u.port.Version, nil
}

// updates checks for updates and lists them: A updates the one selected,
// Y updates all of them after asking once.
func (s *store) updates() {
	var list []update
	var errs []error
	check := func() {
		s.load("Checking for updates", func() ([]Item, error) {
			list, errs = s.f.findUpdates(s.src)
			return nil, nil
		})
	}
	check()
	l := ui.List{}
	for {
		sc := s.scr
		note := ""
		if len(errs) > 0 {
			note = " · couldn't check " + fmt.Sprint(len(errs))
		}
		if len(list) == 0 {
			msg := "Everything installed from MiniMart is up to date."
			if len(errs) > 0 {
				msg += " Some repositories couldn't be checked: " + tnet.Explain(errs[0]).Error()
			}
			msg += " Themes, icon packs and homebrew games have no versions, so they aren't checked."
			s.scr.Notice(s.input, "Updates", msg)
			return
		}
		items := make([]ui.Item, len(list))
		for i, u := range list {
			items[i] = ui.Item{Label: u.Name, Sub: u.Kind + " · " + versionLabel(u.Installed) + " → " + versionLabel(u.Available), Detail: humanSize(u.size)}
		}
		l.Items = items
		l.Sel = min(l.Sel, len(items)-1)
		free, _ := spaceText(0)
		top := sc.StaticFrame("Updates", fmt.Sprintf("%d available · %s%s", len(list), free, note),
			ui.Hint{Button: "A", Label: "Update"}, ui.Hint{Button: "Y", Label: "Update all"}, ui.Hint{Button: "X", Label: "Check again"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(sc, image.Rect(20, top, ui.W-26, ui.H-58))
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.X:
			check()
		case ui.A:
			s.runUpdates(list[l.Sel : l.Sel+1])
			check()
		case ui.Y:
			if s.confirmUpdates(list) {
				s.runUpdates(list)
				check()
			}
		default:
			l.Handle(ev)
		}
	}
}

// confirmUpdates asks once before Update all, saying what it will do.
func (s *store) confirmUpdates(list []update) bool {
	w := ui.W - 56
	var total int64
	var names, system []string
	community := 0
	for _, u := range list {
		total += u.size
		names = append(names, u.Name+" "+versionLabel(u.Available))
		if u.app != nil && u.app.System != "" {
			system = append(system, u.Name)
		}
		if u.Kind != "MiniDeck app" {
			community++
		}
	}
	rs := para(fmt.Sprintf("MiniMart installs %d updates, one after another. Each download is checked against its pinned SHA-256 and installed only if it matches. Settings and sign-ins stay.", len(list)), ui.Body, ui.Text, 0, w)
	rs = append(rs, spaceRows(downloadNeeds(total), 10, w)...)
	if community > 0 {
		rs = append(rs, para(fmt.Sprintf("%d of them aren't from MiniDeck and weren't reviewed by it.", community), ui.Small, ui.Warn, 8, w)...)
	}
	if len(system) > 0 {
		rs = append(rs, para("These change files outside their own folder: "+strings.Join(system, ", ")+".", ui.Small, ui.Warn, 6, w)...)
	}
	rs = append(rs, bullets(names, ui.Small, ui.Text, 8, w)...)
	return s.textScreen("Update all?", fmt.Sprintf("%d updates", len(list)), rs, "Update all")
}

// runUpdates installs the updates in order, with a progress bar each, and
// then says what happened to each.
func (s *store) runUpdates(list []update) {
	var done, failed []string
	selfUpdated := false
	for i, u := range list {
		title := "Updating"
		if len(list) > 1 {
			title = fmt.Sprintf("Updating %d of %d", i+1, len(list))
		}
		var ver string
		err := s.progress(title, u.Name, func(p func(int64, int64)) error {
			v, err := s.apply(u, p)
			ver = v
			return err
		})
		if err != nil {
			failed = append(failed, u.Name+": "+tnet.Explain(err).Error())
			continue
		}
		done = append(done, u.Name+" "+versionLabel(ver))
		if u.self() {
			selfUpdated = true
		}
	}
	w := ui.W - 56
	var rs []row
	if len(done) > 0 {
		rs = append(rs, para(fmt.Sprintf("Updated (%d)", len(done)), ui.Caps, ui.Accent, 0, w)...)
		rs = append(rs, bullets(done, ui.Small, ui.Text, 4, w)...)
	}
	if len(failed) > 0 {
		rs = append(rs, para(fmt.Sprintf("Not updated (%d)", len(failed)), ui.Caps, ui.Warn, 12, w)...)
		rs = append(rs, bullets(failed, ui.Small, ui.Warn, 4, w)...)
	}
	if selfUpdated {
		rs = append(rs, para("MiniMart itself was updated: the new version starts the next time you open it.", ui.Small, ui.Muted, 12, w)...)
	}
	if len(done) > 0 {
		rs = append(rs, para("Updated apps show their new version when you next open them.", ui.Small, ui.Muted, 8, w)...)
	}
	title := "Updates installed"
	switch {
	case len(done) == 0:
		title = "Nothing was updated"
	case len(failed) > 0:
		title = "Some updates failed"
	}
	s.textScreen(title, "", rs, "")
}
