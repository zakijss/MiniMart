// MiniMart installs themes and icon packs from the OnionUI/Themes repository
// (and, in its Apps section, community apps; in Homebrew games, freely
// licensed homebrew ROMs) straight onto the card.
package main

import (
	"errors"
	"fmt"
	"image"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/cache"
	"github.com/zakijss/minideck/tnet"
	"github.com/zakijss/minideck/ui"
	"github.com/zakijss/minideck/version"
)

// store is the app: its screen, downloads and the thumbnails in memory.
type store struct {
	scr   *ui.Screen
	input <-chan ui.Event
	f     *fetcher
	poke  chan struct{}

	mu      sync.Mutex
	thumbs  map[string]image.Image // list thumbnails, by name
	loading map[string]bool
	order   []string // thumbnails in memory, oldest first
	sem     chan struct{}
	src     []Source // repositories
}

func main() {
	version.Flag("minimart")
	version.Note("minimart")
	scr, err := ui.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer scr.Close()
	c := cache.New(filepath.Join(appdir.Dir(), "cache"))
	go c.Prune(60 << 20)
	s := &store{scr: scr, input: ui.Input(), f: &fetcher{http: tnet.Client(20 * time.Second), cache: c},
		poke: make(chan struct{}, 1), thumbs: map[string]image.Image{}, loading: map[string]bool{}, sem: make(chan struct{}, 2),
		src: LoadSources()}
	s.checkInBackground(time.Now())
	s.home()
}

func (s *store) redraw() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// next waits for a button press, a finished background load, or a second.
// A load or the second gives an empty event, so the caller redraws.
func (s *store) next() (ui.Event, bool) {
	for {
		select {
		case ev, ok := <-s.input:
			if ok && !ev.Pressed {
				continue
			}
			return ev, ok
		case <-s.poke:
		case <-time.After(time.Second):
		}
		return ui.Event{}, true
	}
}

// homeItems is the home list. Appearance is last; its row is built again
// for every frame, so it shows the look in use.
func homeItems() []ui.Item {
	return []ui.Item{
		updatesItem(loadNote(), time.Now()),
		{Label: "Featured themes", Sub: "Picked by the Onion team", Icon: "star", Chevron: true},
		{Label: "All themes", Sub: "Over 200 from the community", Icon: "list", Chevron: true},
		{Label: "Icon packs", Sub: "New icons for the menus", Icon: "gear", Chevron: true},
		{Label: "Apps", Sub: "MiniDeck's apps and community apps", Icon: "plus", Chevron: true},
		{Label: "Homebrew games", Sub: "Free games their authors let you download", Icon: "play", Chevron: true},
		{Label: "Ports", Sub: "PC games ported to the Miyoo", Icon: "dice", Chevron: true},
		{Label: "Repositories", Sub: "Where the store looks; add your own", Icon: "server", Chevron: true},
		ui.AppearanceItem(),
	}
}

func (s *store) home() {
	l := ui.List{}
	for {
		l.Items = homeItems()
		sc := s.scr
		free, _ := spaceText(0)
		top := sc.StaticFrame("MiniMart", free,
			ui.Hint{Button: "A", Label: "Open"}, ui.AppearanceHint, ui.Hint{Button: "B", Label: "Exit"})
		l.Draw(sc, image.Rect(20, top, ui.W-26, ui.H-58))
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.Select:
			s.scr.Appearance(s.input)
		case ui.A:
			switch l.Sel {
			case 0:
				s.updates()
			case 1, 2:
				s.themes(l.Sel == 1)
			case 3:
				s.iconPacks()
			case 4:
				s.appList()
			case 5:
				s.homebrew()
			case 6:
				s.ports()
			case 7:
				s.repos()
			case 8:
				s.scr.Appearance(s.input)
			}
		default:
			l.Handle(ev)
		}
	}
}

// load shows a spinner while fn runs.
func (s *store) load(title string, fn func() ([]Item, error)) ([]Item, error) {
	var out []Item
	var err error
	done := make(chan struct{})
	go func() { out, err = fn(); close(done) }()
	for {
		sc := s.scr
		sc.Background()
		sc.Header(title, "")
		sc.Loading(220, "Loading")
		sc.Present()
		select {
		case <-done:
			return out, err
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (s *store) themes(featured bool) {
	title := "All themes"
	if featured {
		title = "Featured"
	}
	all, err := s.load(title, func() ([]Item, error) { return s.f.Themes(s.src) })
	if err != nil {
		s.scr.Notice(s.input, "MiniMart", "Couldn't load the themes: "+tnet.Explain(err).Error())
		return
	}
	var list []Item
	for _, it := range all {
		if !featured || it.Featured {
			list = append(list, it)
		}
	}
	s.browse(title, "themes", list)
}

func (s *store) iconPacks() {
	list, err := s.load("Icon packs", func() ([]Item, error) { return s.f.IconPacks(s.src) })
	if err != nil {
		s.scr.Notice(s.input, "MiniMart", "Couldn't load the icon packs: "+tnet.Explain(err).Error())
		return
	}
	s.browse("Icon packs", "icons", list)
}

// thumb returns the item's list thumbnail, loading it in the background.
func (s *store) thumb(it Item) image.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	if img, ok := s.thumbs[it.Base+it.Name]; ok {
		return img
	}
	if !s.loading[it.Base+it.Name] {
		s.loading[it.Base+it.Name] = true
		go func() {
			s.sem <- struct{}{}
			img, err := s.f.Preview(it)
			var th image.Image
			if err == nil {
				th = Thumb(img, 96, 96)
			}
			<-s.sem
			s.mu.Lock()
			delete(s.loading, it.Base+it.Name)
			if th != nil {
				s.thumbs[it.Base+it.Name] = th
				s.order = append(s.order, it.Base+it.Name)
				for len(s.order) > 60 { // ~37 KB each
					delete(s.thumbs, s.order[0])
					s.order = s.order[1:]
				}
			}
			s.mu.Unlock()
			s.redraw()
		}()
	}
	return nil
}

// browse lists themes or icon packs (key "themes" or "icons", for the
// saved order). X changes the order.
func (s *store) browse(title, key string, list []Item) {
	// Whether each item is installed or in use comes from the card (a stat
	// or two and a file read each, for 200 and more themes), so the rows are
	// built once here (and after sorting), and the one whose details were
	// open again after, not on every redraw.
	active := map[bool]string{false: activeName(false), true: activeName(true)}
	o := listOrder(key, themeOrders)
	var sizes map[string]int64
	var items []ui.Item
	arrange := func() {
		if o == bySize && sizes == nil {
			s.load(title, func() ([]Item, error) { sizes = s.f.ItemSizes(list); return nil, nil })
		}
		sortItems(list, o, sizes, Item.Installed)
		items = make([]ui.Item, len(list))
		for i, it := range list {
			items[i] = s.browseRow(it, active[it.Icons])
		}
	}
	arrange()
	l := ui.List{Items: items}
	for {
		sc := s.scr
		top := sc.StaticFrame(title, fmt.Sprintf("%d to choose from · %s", len(list), orderLabel(o, true)),
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
				it := list[l.Sel]
				s.detail(it) // may install or remove it
				items[l.Sel] = s.browseRow(it, activeName(it.Icons))
			}
		case ui.X:
			if n := s.chooseOrder("Sort "+strings.ToLower(title), themeOrders, o, true); n != o {
				o = n
				saveOrder(key, o)
				arrange()
				l = ui.List{Items: items}
			}
		default:
			l.Handle(ev)
		}
	}
}

// browseRow is the list row for a theme or icon pack; active is the name of
// the one in use.
func (s *store) browseRow(it Item, active string) ui.Item {
	sub := it.Author()
	if sub != "" {
		sub = "by " + sub
	}
	detail := ""
	if it.Name == active {
		detail = "In use"
	} else if it.Installed() {
		detail = "Installed"
	}
	return ui.Item{Label: it.Title(), Sub: sub, Detail: detail, Image: func() image.Image { return s.thumb(it) }}
}

func (s *store) detail(it Item) {
	var preview image.Image
	var size int64 = -1
	go func() {
		if img, err := s.f.Preview(it); err == nil {
			p := Thumb(img, 320, 240)
			if it.Icons { // icon pack previews are tall
				p = Thumb(img, 166, 240)
			}
			s.mu.Lock()
			preview = p
			s.mu.Unlock()
			s.redraw()
		}
		n := s.f.Size(it)
		s.mu.Lock()
		size = n
		s.mu.Unlock()
		s.redraw()
	}()
	msg := ""
	for {
		sc := s.scr
		sc.Background()
		top := sc.Header(it.Title(), "by "+it.Author())
		s.mu.Lock()
		p, sz := preview, size
		s.mu.Unlock()
		box := image.Rect(24, top+8, 24+320, top+8+240)
		if p != nil {
			b := p.Bounds()
			sc.RoundPicture(p, image.Rect(box.Min.X, box.Min.Y, box.Min.X+b.Dx(), box.Min.Y+b.Dy()), 12)
		} else {
			sc.Panel(box, 12)
			sc.Loading(box.Min.Y+box.Dy()/2-12, "Preview")
		}
		x, y := 364, top+16
		kind := "Theme"
		if it.Icons {
			kind = "Icon pack"
		}
		if it.Remix {
			kind += " · remix"
		}
		sc.Text(x, y, kind, ui.Caps, ui.Muted)
		y += 28
		state := "Not installed"
		switch {
		case it.Active():
			state = "In use"
		case it.Installed():
			state = "Installed"
		}
		sc.Text(x, y, state, ui.Heading, ui.Text)
		y += 34
		if sz > 0 {
			sc.Text(x, y, fmt.Sprintf("%.1f MB download", float64(sz)/(1<<20)), ui.Body, ui.Muted)
		} else if sz < 0 {
			sc.Text(x, y, "Checking size…", ui.Body, ui.Muted)
		}
		y += 30
		y = drawSpace(sc, x, y, ui.W-x-24, downloadNeeds(sz))
		y += 10
		howTo := "Apply it in Theme Switcher."
		if it.Icons {
			howTo = "Apply it in Tweaks › Appearance › Icon packs."
		}
		for _, line := range ui.Wrap(ui.Small, howTo, ui.W-x-24) {
			sc.Text(x, y, line, ui.Small, ui.Muted)
			y += ui.LineHeight(ui.Small)
		}
		if msg != "" {
			y += 10
			for _, line := range ui.Wrap(ui.Small, msg, ui.W-x-24) {
				sc.Text(x, y, line, ui.Small, ui.Warn)
				y += ui.LineHeight(ui.Small)
			}
		}
		hints := []ui.Hint{{Button: "A", Label: "Install"}}
		if it.Installed() {
			hints = []ui.Hint{{Button: "A", Label: "Reinstall"}}
			if !it.Active() {
				hints = append(hints, ui.Hint{Button: "X", Label: "Remove"})
			}
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
			if err := s.install(it); err != nil {
				msg = tnet.Explain(err).Error()
			} else {
				s.scr.Notice(s.input, "Installed", it.Title()+" is on your card. "+howTo)
			}
		case ui.X:
			if it.Installed() && !it.Active() && s.scr.Confirm(s.input, "Remove "+it.Title()+"?", "It's deleted from the card. You can install it again from here.", "Remove") {
				if err := Remove(it); err != nil {
					msg = err.Error()
				}
			}
		}
	}
}

// install downloads with a progress bar.
func (s *store) install(it Item) error {
	return s.progress("Installing", it.Title(), func(p func(int64, int64)) error { return s.f.Install(it, p) })
}

// progress runs work, showing a progress bar fed by the callback it gets.
func (s *store) progress(title, name string, work func(func(int64, int64)) error) error {
	var mu sync.Mutex
	var done, total int64
	errc := make(chan error, 1)
	go func() {
		errc <- work(func(d, t int64) {
			mu.Lock()
			done, total = d, t
			mu.Unlock()
		})
	}()
	for {
		mu.Lock()
		d, t := done, total
		mu.Unlock()
		sc := s.scr
		sc.Background()
		sc.Header(title, name)
		bar := image.Rect(80, 230, ui.W-80, 240)
		frac := 0.0
		if t > 0 {
			frac = float64(d) / float64(t)
		}
		sc.Bar(bar, frac, ui.Text)
		label := fmt.Sprintf("%.1f MB", float64(d)/(1<<20))
		if t > 0 {
			label = fmt.Sprintf("%.1f of %.1f MB", float64(d)/(1<<20), float64(t)/(1<<20))
		}
		sc.TextCenter(ui.W/2, 256, label, ui.Small, ui.Muted)
		sc.Present()
		select {
		case err := <-errc:
			return err
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// appList lists the apps of every catalogue: MiniDeck's own first, then
// the rest by name. The first time it has community apps to show, it shows
// the due-diligence warning and opens only once the user acknowledges it;
// Y shows the warning again.
func (s *store) appList() {
	var list []App
	var errs []error
	s.load("Apps", func() ([]Item, error) {
		list, errs = s.f.Apps(s.src)
		return nil, nil
	})
	if len(list) == 0 {
		msg := "No apps: turn on an app catalogue in Repositories."
		if len(errs) > 0 {
			msg = "Couldn't load the catalogues: " + tnet.Explain(errs[0]).Error()
		}
		s.scr.Notice(s.input, "Apps", msg)
		return
	}
	community := false
	for _, a := range list {
		community = community || !a.Own
	}
	if community && !warningSeen() && !s.warning(true) {
		return
	}
	note := sourcesNote(errs)
	// What is installed is read from the card once, and again for an app
	// whose details were open, not on every redraw.
	state := map[string]string{}
	counts := map[string]int{}
	for _, a := range list {
		state[a.Folder] = installedState(a)
		counts[categoryOf(a)]++
	}
	o := listOrder("apps", appOrders)
	cat := ""
	var shown []App
	arrange := func() {
		sortAppsBy(list, o, func(a App) bool { return state[a.Folder] != "" })
		shown = filterApps(list, cat)
	}
	arrange()
	l := ui.List{}
	for {
		s.drawAppList(&l, shown, state, appListSub(len(shown), o, cat), note, community, o == byCategory)
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
		case ui.A:
			if l.Sel < len(shown) {
				a := shown[l.Sel]
				s.appDetail(a) // may install or remove it
				state[a.Folder] = installedState(a)
			}
		case ui.X:
			no, nc := s.chooseAppView(o, cat, counts)
			if no != o {
				saveOrder("apps", no)
			}
			if no != o || nc != cat {
				o, cat = no, nc
				arrange()
				l = ui.List{}
			}
		case ui.Y:
			if community {
				s.warning(false)
			}
		default:
			l.Handle(ev)
		}
	}
}

// appListSub is the Apps list's subtitle: how many apps it shows, in what
// order, and the category shown, if only one is.
func appListSub(n int, o order, cat string) string {
	sub := fmt.Sprintf("%d apps · %s", n, orderLabel(o, false))
	if cat != "" {
		sub = cat + " · " + sub
	}
	return sub
}

// drawAppList draws the Apps list, with the reminder about community apps
// and a note about catalogues that couldn't be read under it. With withCat
// set, each row starts with the app's category.
func (s *store) drawAppList(l *ui.List, list []App, state map[string]string, sub, note string, community, withCat bool) {
	items := make([]ui.Item, len(list))
	for i, a := range list {
		line := listLine(a)
		if withCat {
			line = categoryOf(a) + " · " + line
		}
		items[i] = ui.Item{Label: a.Name, Sub: line, Detail: state[a.Folder], Chevron: true}
	}
	l.Items = items
	hints := []ui.Hint{{Button: "A", Label: "Details"}, {Button: "X", Label: "Sort"}}
	if community {
		hints = append(hints, ui.Hint{Button: "Y", Label: "About apps"})
	}
	sc := s.scr
	top := sc.StaticFrame("Apps", sub, append(hints, ui.Hint{Button: "B", Label: "Back"})...)
	bottom := ui.H - 58
	lh := ui.LineHeight(ui.Small) + 4
	if note != "" { // a catalogue that couldn't be read
		bottom -= lh
		sc.Text(28, bottom+2, ui.Fit(ui.Small, note, ui.W-56), ui.Small, ui.Muted)
	}
	if community {
		bottom -= lh
		sc.Text(28, bottom+2+(ui.LineHeight(ui.Small)-ui.LineHeight(ui.Caps))/4*2, ui.Fit(ui.Caps, reminder, ui.W-56), ui.Caps, ui.Warn)
	}
	l.Draw(sc, image.Rect(20, top, ui.W-26, bottom-4))
	sc.Present()
}

// pageRows is how far L1 and R1 scroll a text page, in rows.
const pageRows = 8

// readmeRows are the README part of an app's page: a title, then the
// README as text, or a line saying it is loading or isn't there.
func readmeRows(bs []mdBlock, err error, done bool, w int) []row {
	rs := para("README", ui.Caps, ui.Muted, 18, w)
	switch {
	case !done:
		return append(rs, para("Loading the README…", ui.Small, ui.Muted, 6, w)...)
	case errors.Is(err, errNoReadme):
		return append(rs, para("The repository has no README.", ui.Small, ui.Muted, 6, w)...)
	case err != nil:
		return append(rs, para("Couldn't load the README: "+tnet.Explain(err).Error(), ui.Small, ui.Muted, 6, w)...)
	}
	for i, b := range bs {
		switch b.Kind {
		case mdHeading:
			rs = append(rs, para(b.Text, ui.Body, ui.Accent, 12, w)...)
		case mdItem:
			rs = append(rs, bullets([]string{b.Text}, ui.Small, ui.Text, 4, w)...)
		case mdCode:
			gap := 0
			if i == 0 || bs[i-1].Kind != mdCode {
				gap = 8
			}
			code := para(b.Text, ui.Small, ui.Muted, gap, w-16)
			for i := range code {
				code[i].indent = 16
			}
			rs = append(rs, code...)
		default:
			rs = append(rs, para(b.Text, ui.Small, ui.Text, 8, w)...)
		}
	}
	return rs
}

// installedState is the app's list detail: its installed version,
// "Installed" if that isn't known, or "" if it isn't installed.
func installedState(a App) string {
	if !a.Installed() {
		return ""
	}
	if v := a.InstalledVersion(); v != "" {
		return versionLabel(v)
	}
	return "Installed"
}

// appDetail shows what an app does and the facts about it; A installs or
// updates it (asking first for a community app), X removes it.
func (s *store) appDetail(a App) {
	var m *manifest
	var merr error
	go func() {
		mm, err := s.f.Manifest(a)
		s.mu.Lock()
		m, merr = mm, err
		s.mu.Unlock()
		s.redraw()
	}()
	var readme []mdBlock
	var rerr error
	readmeDone := false
	go func() {
		bs, err := s.f.Readme(a)
		s.mu.Lock()
		readme, rerr, readmeDone = bs, err, true
		s.mu.Unlock()
		s.redraw()
	}()
	msg := ""
	off := 0
	w := ui.W - 56
	for {
		s.mu.Lock()
		mm, err := m, merr
		s.mu.Unlock()

		var rs []row
		desc := ui.Wrap(ui.Body, a.Desc, w)
		if len(desc) > 3 {
			desc = append(desc[:2], ui.Fit(ui.Body, desc[2]+" …", w))
		}
		for _, l := range desc {
			rs = append(rs, row{text: l, face: ui.Body, col: ui.Text})
		}
		if a.Own {
			rs = append(rs, para("MiniDeck's own app, built from this project's source.", ui.Small, ui.Accent, 8, w)...)
		} else {
			rs = append(rs, para("Not reviewed by MiniDeck: read its repository before installing.", ui.Small, ui.Warn, 8, w)...)
		}
		ver, size := a.Version, a.Size
		if mm != nil {
			ver, size = mm.Version, mm.Assets.Zip.Size
		}
		verNote := "checking the latest version…"
		if err != nil {
			verNote = "couldn't check the version: " + tnet.Explain(err).Error()
		}
		rs = append(rs, factRows(a, ver, verNote, size, w)...)
		installed := "Not installed"
		if a.Installed() {
			installed = "Installed"
			if v := a.InstalledVersion(); v != "" {
				installed = "Installed: " + versionLabel(v)
			}
		}
		rs = append(rs, kv("On this Miyoo", installed, ui.Text, 6, w)...)
		if nf := a.NeedsFile; nf != nil && a.Installed() {
			have := "Not added yet: press Y to add yours."
			if nf.present() {
				have = "In place (" + nf.Path + ")"
			}
			rs = append(rs, kv("Your copy", have, ui.Text, 6, w)...)
		}
		if a.Note != "" {
			rs = append(rs, para(a.Note, ui.Small, ui.Muted, 12, w)...)
		}
		if msg != "" {
			rs = append(rs, para(msg, ui.Small, ui.Warn, 10, w)...)
		}
		s.mu.Lock()
		rs = append(rs, readmeRows(readme, rerr, readmeDone, w)...)
		s.mu.Unlock()

		sc := s.scr
		sc.Background()
		top := sc.Header(a.Name, a.Source)
		maxOff := drawRows(sc, rs, off, 28, top+10, ui.H-62)
		off = min(off, maxOff)
		act := "Install"
		if a.Installed() {
			act = "Update"
			if mm != nil && a.InstalledVersion() == mm.Version {
				act = "Reinstall"
			}
		}
		hints := []ui.Hint{{Button: "A", Label: act}}
		if a.Installed() {
			hints = append(hints, ui.Hint{Button: "X", Label: "Remove"})
		}
		needCopy := a.NeedsFile != nil && a.Installed() && !a.NeedsFile.present()
		if needCopy {
			hints = append(hints, ui.Hint{Button: "Y", Label: "Add game file"})
		}
		if maxOff > 0 {
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
		case ui.Y:
			if needCopy {
				s.provideGameFile(a)
			}
		case ui.A:
			msg = ""
			if needsConfirm(a) && !s.confirmInstall(a, a.Installed()) {
				break
			}
			var ver string
			err := s.progress("Installing", a.Name, func(p func(int64, int64)) error {
				v, err := s.f.InstallApp(a, p)
				ver = v
				return err
			})
			if err != nil {
				msg = tnet.Explain(err).Error()
			} else {
				s.scr.Notice(s.input, "Installed", fmt.Sprintf("%s %s is installed: the download matched its pinned checksum. It appears in Apps when you leave MiniMart.", a.Name, versionLabel(ver)))
				s.provideGameFile(a)
			}
		case ui.X:
			if !a.Installed() {
				break
			}
			yes := false
			if a.System == "" {
				yes = s.scr.Confirm(s.input, "Remove "+a.Name+"?", "Its folder is deleted, with its settings and sign-in.", "Remove")
			} else {
				rs := para("Only App/"+a.Folder+" is deleted, with its settings and sign-in. That may not undo what the app changed outside its folder:", ui.Body, ui.Text, 0, w)
				yes = s.textScreen("Remove "+a.Name+"?", "", append(rs, para(a.System, ui.Small, ui.Warn, 10, w)...), "Remove")
			}
			if yes {
				if err := RemoveApp(a); err != nil {
					msg = err.Error()
				}
			}
		}
	}
}
