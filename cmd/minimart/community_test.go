package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zakijss/minideck/cache"
)

// The "Community apps" source reads the published catalogue, keeps it on
// the card, and shows the compiled-in list when it can't be read.
func TestCommunityCatalogFallback(t *testing.T) {
	src := Source{Name: "Community apps", Type: "apps", URL: communityCatalog, Enabled: true, Builtin: true}

	// offline with nothing kept: the compiled-in list, without an error
	off := &fetcher{http: &http.Client{Transport: offlineTransport{}}, cache: cache.New(t.TempDir())}
	list, errs := off.Apps([]Source{src})
	if len(errs) != 0 || len(list) != len(apps) || list[0].Source != "Community apps" {
		t.Fatalf("offline: %d apps, %v", len(list), errs)
	}

	// a published catalogue kept on the card is used, offline too
	one := App{Name: "Remote", Folder: "Remote", Desc: "d", Repo: "o/r", License: "MIT", Category: "Tools",
		Zip: "https://github.com/o/r/releases/download/v1/r.zip", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Version: "1"}
	b, _ := json.Marshal(Catalog{Name: "Community apps", Apps: []App{one}})
	off.cache.Put(communityCatalog, b)
	list, errs = off.Apps([]Source{src})
	if len(errs) != 0 || len(list) != 1 || list[0].Name != "Remote" || list[0].Own || list[0].Source != "Community apps" {
		t.Fatalf("kept catalogue: %+v %v", list, errs)
	}

	// one that doesn't parse, or has no apps, falls back
	off.cache.Put(communityCatalog, []byte(`{"apps": []}`))
	if list, _ = off.Apps([]Source{src}); len(list) != len(apps) {
		t.Fatalf("empty catalogue: %d apps", len(list))
	}
	off.cache.Put(communityCatalog, []byte(`not json`))
	if list, _ = off.Apps([]Source{src}); len(list) != len(apps) {
		t.Fatalf("damaged catalogue: %d apps", len(list))
	}
}

// A sources.json saved when the community list lived only in the app
// still turns the source off.
func TestCommunitySourceMigrates(t *testing.T) {
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	old := []Source{{Name: "Community apps", Type: "apps", URL: builtinCatalog, Enabled: false, Builtin: true}}
	if err := SaveSources(old); err != nil {
		t.Fatal(err)
	}
	for _, s := range LoadSources() {
		if s.Name == "Community apps" {
			if s.URL != communityCatalog || s.Enabled {
				t.Fatalf("%+v", s)
			}
			return
		}
	}
	t.Fatal("no Community apps source")
}

// START on Apps and Ports downloads the lists again; when that fails, the
// copy on the card stays and the error says which list.
func TestRefreshKeepsCopy(t *testing.T) {
	src := Source{Name: "Community apps", Type: "apps", URL: communityCatalog, Enabled: true, Builtin: true}
	off := &fetcher{http: &http.Client{Transport: offlineTransport{}}, cache: cache.New(t.TempDir())}
	off.cache.Put(communityCatalog, []byte("kept"))
	err := off.refresh([]Source{src, {Name: "Off", Type: "apps", URL: "https://example.com/off.json"}})
	if err == nil || !strings.HasPrefix(err.Error(), "Community apps: ") {
		t.Fatalf("refresh offline: %v", err)
	}
	if b, ok := off.cache.Get(communityCatalog); !ok || string(b) != "kept" {
		t.Fatalf("kept copy is now %q", b)
	}
}
