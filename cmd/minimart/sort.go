package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zakijss/minideck/ui"
)

// categories are the kinds of community app, in the order the filter
// lists them. The catalogue generator (tools/catalog) gives each app one;
// anything else reads as "Other".
var categories = []string{"Games", "Emulators and ports", "Media", "Network", "Reading", "Tools", "System"}

// otherCategory is the category of an app without a known one.
const otherCategory = "Other"

// categoryOf is the app's category, or "Other".
func categoryOf(a App) string {
	for _, c := range categories {
		if strings.EqualFold(a.Category, c) {
			return c
		}
	}
	return otherCategory
}

// An order is how a list is sorted. Not every list offers every order:
// themes have no release date, only the order they were added in.
type order string

const (
	byName      order = "name"
	byNewest    order = "newest"
	bySize      order = "size"
	byCategory  order = "category"
	byInstalled order = "installed"
)

// orderLabel is the order as the list's subtitle and the sort menu show it.
func orderLabel(o order, themes bool) string {
	switch o {
	case byNewest:
		if themes {
			return "newest added first"
		}
		return "newest first"
	case bySize:
		return "smallest first"
	case byCategory:
		return "by category"
	case byInstalled:
		return "installed first"
	}
	return "by name"
}

// The orders each list offers, the first being its default.
var (
	appOrders   = []order{byName, byNewest, bySize, byCategory, byInstalled}
	themeOrders = []order{byName, byNewest, bySize, byInstalled}
	gameOrders  = []order{byName, byNewest, bySize, byInstalled}
)

// validOrder returns o if the list offers it, else the list's default.
func validOrder(o order, offered []order) order {
	for _, x := range offered {
		if x == o {
			return o
		}
	}
	return offered[0]
}

// releasedTime is the app's release date, or the zero time if unknown.
func releasedTime(d string) time.Time {
	t, _ := time.Parse("2006-01-02", strings.TrimSpace(d))
	return t
}

// sortAppsBy sorts the Apps list. By name, MiniDeck's own apps come first
// (sortApps); the other orders sort every app alike, by name where they
// tie. installed reports whether an app is on the card. Unknown dates and
// sizes go last.
func sortAppsBy(list []App, o order, installed func(App) bool) {
	sortApps(list)
	if o = validOrder(o, appOrders); o == byName {
		return
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch o {
		case byNewest:
			ta, tb := releasedTime(a.Released), releasedTime(b.Released)
			if !ta.Equal(tb) {
				return ta.After(tb)
			}
		case bySize:
			if a.Size != b.Size {
				return less64(a.Size, b.Size)
			}
		case byCategory:
			ca, cb := categoryRank(categoryOf(a)), categoryRank(categoryOf(b))
			if ca != cb {
				return ca < cb
			}
		case byInstalled:
			ia, ib := installed(a), installed(b)
			if ia != ib {
				return ia
			}
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

// less64 orders sizes smallest first, with unknown sizes (0 or less) last.
func less64(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return a > 0
	}
	return a < b
}

// categoryRank is the category's place in the filter's list; "Other" last.
func categoryRank(c string) int {
	for i, x := range categories {
		if x == c {
			return i
		}
	}
	return len(categories)
}

// filterApps keeps the apps of one category; "" keeps them all.
func filterApps(list []App, cat string) []App {
	if cat == "" {
		return list
	}
	var out []App
	for _, a := range list {
		if categoryOf(a) == cat {
			out = append(out, a)
		}
	}
	return out
}

// sortItems sorts themes or icon packs. Newest goes by each item's place in
// its repository's list (Pos: newer additions are further down), size by
// its download size keyed by ZipURL (0 when unknown).
func sortItems(list []Item, o order, size map[string]int64, installed func(Item) bool) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch o {
		case byNewest:
			if a.Pos != b.Pos {
				return a.Pos > b.Pos
			}
		case bySize:
			if sa, sb := size[a.ZipURL()], size[b.ZipURL()]; sa != sb {
				return less64(sa, sb)
			}
		case byInstalled:
			if ia, ib := installed(a), installed(b); ia != ib {
				return ia
			}
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

// sortGames sorts one system's games. size gives each game's download size
// by URL (0 when unknown).
func sortGames(list []Game, o order, size map[string]int64, installed func(Game) bool) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch o {
		case byNewest:
			ta, tb := releasedTime(a.Date), releasedTime(b.Date)
			if !ta.Equal(tb) {
				return ta.After(tb)
			}
		case bySize:
			if sa, sb := size[a.URL], size[b.URL]; sa != sb {
				return less64(sa, sb)
			}
		case byInstalled:
			if ia, ib := installed(a), installed(b); ia != ib {
				return ia
			}
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})
}

// chooseOrder asks for a list's order; the current one is marked. It
// returns the order chosen, or cur on B.
func (s *store) chooseOrder(title string, offered []order, cur order, themes bool) order {
	items := make([]ui.Item, len(offered))
	for i, o := range offered {
		items[i] = ui.Item{Label: upperFirst(orderLabel(o, themes))}
		if o == cur {
			items[i].Detail = "Current"
		}
	}
	i, ok := s.scr.Choose(s.input, title, items)
	if !ok {
		return cur
	}
	return offered[i]
}

// chooseAppView asks for the Apps list's order and category in one list:
// the orders first, then "All categories" and each category with apps.
// counts gives the number of apps in each category.
func (s *store) chooseAppView(cur order, cat string, counts map[string]int) (order, string) {
	var items []ui.Item
	for _, o := range appOrders {
		it := ui.Item{Label: upperFirst(orderLabel(o, false)), Sub: "Sort"}
		if o == cur {
			it.Detail = "Current"
		}
		items = append(items, it)
	}
	cats := []string{""}
	for _, c := range append(append([]string{}, categories...), otherCategory) {
		if counts[c] > 0 {
			cats = append(cats, c)
		}
	}
	for _, c := range cats {
		it := ui.Item{Label: "All categories", Sub: "Show"}
		n := 0
		for _, k := range counts {
			n += k
		}
		if c != "" {
			it.Label = c
			n = counts[c]
		}
		it.Detail = fmt.Sprint(n)
		if c == cat {
			it.Detail += " · shown"
		}
		items = append(items, it)
	}
	i, ok := s.scr.Choose(s.input, "Sort and show", items)
	if !ok {
		return cur, cat
	}
	if i < len(appOrders) {
		return appOrders[i], cat
	}
	return cur, cats[i-len(appOrders)]
}

// upperFirst capitalises the first letter of an ASCII label.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
