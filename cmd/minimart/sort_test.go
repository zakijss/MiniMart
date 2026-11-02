package main

import (
	"strings"
	"testing"
)

func appNames(list []App) string {
	var out []string
	for _, a := range list {
		out = append(out, a.Name)
	}
	return strings.Join(out, ",")
}

func TestSortAppsBy(t *testing.T) {
	base := []App{
		{Name: "beta", Released: "2026-01-01", Size: 300, Category: "Media"},
		{Name: "Alpha", Released: "2025-05-01", Size: 0, Category: "Games"},
		{Name: "own", Own: true, Released: "2024-01-01", Size: 100},
		{Name: "gamma", Released: "", Size: 200, Category: "nonsense"},
	}
	cases := []struct {
		o    order
		want string
	}{
		{byName, "own,Alpha,beta,gamma"},
		{byNewest, "beta,Alpha,own,gamma"},       // unknown dates last
		{bySize, "own,gamma,beta,Alpha"},         // unknown sizes last
		{byCategory, "Alpha,beta,gamma,own"},     // Games, Media, then Other by name
		{byInstalled, "beta,gamma,Alpha,own"},    // installed first, then by name
		{order("bogus"), "own,Alpha,beta,gamma"}, // stays by name
	}
	installed := func(a App) bool { return a.Name == "beta" || a.Name == "gamma" }
	for _, c := range cases {
		list := append([]App{}, base...)
		sortAppsBy(list, c.o, installed)
		if got := appNames(list); got != c.want {
			t.Errorf("%s: %s, want %s", c.o, got, c.want)
		}
	}
	if got := appNames(filterApps(base, "Media")); got != "beta" {
		t.Errorf("filter Media: %s", got)
	}
	if got := appNames(filterApps(base, otherCategory)); got != "own,gamma" {
		t.Errorf("filter Other: %s", got)
	}
	if got := len(filterApps(base, "")); got != 4 {
		t.Errorf("no filter: %d", got)
	}
	if categoryOf(App{Category: "media"}) != "Media" || categoryOf(App{}) != otherCategory {
		t.Error("categoryOf")
	}
}

func TestSortItemsAndGames(t *testing.T) {
	base := "https://raw.githubusercontent.com/OnionUI/Themes/main/"
	items := []Item{{Base: base, Name: "B", Pos: 0}, {Base: base, Name: "a", Pos: 2}, {Base: base, Name: "C", Pos: 1}}
	sortItems(items, byNewest, nil, nil)
	if items[0].Name != "a" || items[1].Name != "C" || items[2].Name != "B" {
		t.Errorf("newest: %+v", items)
	}
	size := map[string]int64{items[2].ZipURL(): 5, items[1].ZipURL(): 9}
	sortItems(items, bySize, size, nil)
	if items[0].Name != "B" || items[1].Name != "C" || items[2].Name != "a" {
		t.Errorf("size: %+v", items)
	}
	sortItems(items, byName, nil, nil)
	if items[0].Name != "a" || items[2].Name != "C" {
		t.Errorf("name: %+v", items)
	}
	sortItems(items, byInstalled, nil, func(it Item) bool { return it.Name == "C" })
	if items[0].Name != "C" {
		t.Errorf("installed: %+v", items)
	}

	games := []Game{{Title: "z", Date: "2020-01-01", URL: "u1"}, {Title: "y", URL: "u2", Size: 4}, {Title: "x", Date: "2024-03-03", URL: "u3"}}
	sortGames(games, byNewest, nil, nil)
	if games[0].Title != "x" || games[2].Title != "y" {
		t.Errorf("games newest: %+v", games)
	}
	sortGames(games, bySize, map[string]int64{"u2": 4, "u1": 7}, nil)
	if games[0].Title != "y" || games[1].Title != "z" || games[2].Title != "x" {
		t.Errorf("games size: %+v", games)
	}
}

func TestOrderSaved(t *testing.T) {
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	if listOrder("apps", appOrders) != byName {
		t.Fatal("default order")
	}
	saveOrder("apps", byCategory)
	saveOrder("themes", bySize)
	if listOrder("apps", appOrders) != byCategory || listOrder("themes", themeOrders) != bySize {
		t.Fatal("orders not remembered")
	}
	// an order a list doesn't offer falls back to its default
	if listOrder("apps", themeOrders) != byName {
		t.Fatal("unoffered order kept")
	}
	if err := updateSettings(func(st *settings) { st.GameFiles = "https://nas.example/games/" }); err != nil {
		t.Fatal(err)
	}
	st := loadSettings()
	if st.GameFiles != "https://nas.example/games/" || st.Sort["apps"] != byCategory {
		t.Fatalf("settings: %+v", st)
	}
}

func TestListingSizes(t *testing.T) {
	if u := listingURL("https://raw.githubusercontent.com/OnionUI/Themes/main/", true); u != "https://api.github.com/repos/OnionUI/Themes/contents/release/icons?ref=main" {
		t.Fatal(u)
	}
	if listingURL("https://example.org/themes/", false) != "" {
		t.Fatal("not GitHub")
	}
	got := parseListing([]byte(`[{"name":"Hippo Stock.zip","type":"file","size":1234},{"name":"icons","type":"dir","size":0},{"name":"x.txt","type":"file","size":5}]`))
	if len(got) != 1 || got["Hippo Stock"] != 1234 {
		t.Fatalf("%v", got)
	}
}

func TestAppListSub(t *testing.T) {
	if s := appListSub(12, byNewest, "Media"); s != "Media · 12 apps · newest first" {
		t.Fatal(s)
	}
	if s := appListSub(3, byName, ""); s != "3 apps · by name" {
		t.Fatal(s)
	}
}
