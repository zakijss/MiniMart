package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zakijss/minideck/cache"
)

func TestMarkdownText(t *testing.T) {
	md := "<!-- hidden -->\n" +
		"<h1 align=\"center\">Title</h1>\n" +
		"<p align=\"center\"><img src=\"x.png\"></p>\n\n" +
		"[![CI](https://img.shields.io/x.svg)](https://github.com/x/y/actions)\n\n" +
		"A **bold** line with a [link](https://example.org) and `code`\n" +
		"that wraps &amp; continues.\n\n" +
		"> [!WARNING]\n> Back up first.\n\n" +
		"Setext heading\n==============\n\n" +
		"## Install\n\n" +
		"- one\n* two\n1. first step\n\n" +
		"| Game | SHA-1 |\n|---|---|\n| Mario | `abc` |\n\n" +
		"```sh\nmake   build\n\n```\n" +
		"---\n" +
		"[ref]: https://example.org/ref\n" +
		"Last.\n"
	got := markdownText(md, 10000)
	want := []mdBlock{
		{mdHeading, "Title"},
		{mdPara, "A bold line with a link and code that wraps & continues."},
		{mdPara, "Warning: Back up first."},
		{mdHeading, "Setext heading"},
		{mdHeading, "Install"},
		{mdItem, "one"},
		{mdItem, "two"},
		{mdPara, "1. first step"},
		{mdPara, "Game · SHA-1"},
		{mdPara, "Mario · abc"},
		{mdCode, "make   build"},
		{mdPara, "Last."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	// no URLs survive
	for _, b := range got {
		if strings.Contains(b.Text, "http") {
			t.Errorf("a link address survived: %q", b.Text)
		}
	}
}

func TestMarkdownTextLimit(t *testing.T) {
	md := strings.Repeat("A paragraph of some length.\n\n", 100)
	got := markdownText(md, 100)
	if len(got) != 4 || !strings.Contains(got[3].Text, "rest is in the repository") {
		t.Fatalf("%+v", got)
	}
}

func TestReadmeRef(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r/releases/download/v1.2/x.zip":                         "v1.2",
		"https://github.com/o/r/archive/0123456789012345678901234567890123456789.zip": "0123456789012345678901234567890123456789",
		"": "HEAD",
	}
	for zip, want := range cases {
		if got := readmeRef(App{Repo: "o/r", Zip: zip}); got != want {
			t.Errorf("%s: %s, want %s", zip, got, want)
		}
	}
}

// rewrite sends every request to the test server, keeping its path.
type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestReadmeFetch(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/o/lower/v1/readme.md":
			w.Write([]byte("# Lower\n\nText."))
		case "/o/broken/v1/README.md":
			http.Error(w, "down", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	f := &fetcher{http: &http.Client{Transport: rewrite{u}}, cache: cache.New(t.TempDir())}

	// falls back to readme.md, and keeps it
	a := App{Repo: "o/lower", Zip: "https://github.com/o/lower/releases/download/v1/x.zip"}
	bs, err := f.Readme(a)
	if err != nil || len(bs) != 2 || bs[0].Text != "Lower" {
		t.Fatalf("%+v %v", bs, err)
	}
	if _, err := f.Readme(a); err != nil || hits["/o/lower/v1/readme.md"] != 1 {
		t.Fatalf("not cached: %v %v", err, hits)
	}
	// none: answered from the cache the second time
	n := App{Repo: "o/none", Zip: "https://github.com/o/none/releases/download/v1/x.zip"}
	if _, err := f.Readme(n); !errors.Is(err, errNoReadme) {
		t.Fatal(err)
	}
	before := hits["/o/none/v1/README.md"]
	if _, err := f.Readme(n); !errors.Is(err, errNoReadme) || hits["/o/none/v1/README.md"] != before {
		t.Fatal("no README not remembered")
	}
	// a server error isn't taken for "no README"
	if _, err := f.Readme(App{Repo: "o/broken", Zip: "https://github.com/o/broken/releases/download/v1/x.zip"}); err == nil || errors.Is(err, errNoReadme) {
		t.Fatalf("server error: %v", err)
	}
	// offline: an error, and no README remembered as missing
	off := &fetcher{http: &http.Client{Transport: offlineTransport{}}, cache: cache.New(t.TempDir())}
	if _, err := off.Readme(a); err == nil || errors.Is(err, errNoReadme) {
		t.Fatalf("offline: %v", err)
	}
	if _, err := f.Readme(App{Repo: "../x", Zip: ""}); !errors.Is(err, errNoReadme) {
		t.Fatal("bad repo name fetched")
	}
}

const sampleChangelog = `# Changelog

## [Unreleased]

- not out yet

## [1.3.0] - 2026-11-20

### Added

- Update all.

## [1.2.1] - 2026-11-12

### Fixed

- A fix.

## [1.2.0] - 2026-11-01

- Old news.
`

func TestChangelog(t *testing.T) {
	got := changesSince(sampleChangelog, "1.2.0")
	if !strings.Contains(got, "## 1.3.0 - 2026-11-20") || !strings.Contains(got, "A fix.") ||
		strings.Contains(got, "Old news") || strings.Contains(got, "not out yet") || strings.Contains(got, "# Changelog") {
		t.Errorf("since 1.2.0:\n%s", got)
	}
	if got := changesSince(sampleChangelog, "v1.2.1"); strings.Contains(got, "A fix.") || !strings.Contains(got, "Update all.") {
		t.Errorf("since v1.2.1:\n%s", got)
	}
	if got := changesSince(sampleChangelog, "0.9"); !strings.Contains(got, "Old news") {
		t.Errorf("unknown installed version keeps everything:\n%s", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o/app/v1.3.0/CHANGELOG.md" {
			w.Write([]byte(sampleChangelog))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	f := &fetcher{http: &http.Client{Transport: rewrite{u}}, cache: cache.New(t.TempDir())}
	up := update{Installed: "1.2.1", app: &App{Repo: "o/app"}, ref: "v1.3.0"}
	bs, err := f.Changelog(up)
	if err != nil || len(bs) == 0 || bs[0].Kind != mdHeading || bs[0].Text != "1.3.0 - 2026-11-20" {
		t.Fatalf("%+v %v", bs, err)
	}
	up.ref = "v9"
	if _, err := f.Changelog(up); !errors.Is(err, errNoChangelog) {
		t.Errorf("missing changelog: %v", err)
	}
	if _, err := f.Changelog(update{port: &Port{}}); !errors.Is(err, errNoChangelog) {
		t.Errorf("port: %v", err)
	}
}

func TestProgressLabel(t *testing.T) {
	for _, tc := range []struct {
		done, total int64
		rate        float64
		want        string
	}{
		{1 << 20, 4 << 20, 0, "1.0 of 4.0 MB"},
		{1 << 20, 0, 512 << 10, "1.0 MB · 512 KB/s"},
		{1 << 20, 4 << 20, 512 << 10, "1.0 of 4.0 MB · 512 KB/s · 7 s left"},
		{0, 300 << 20, 2 << 20, "0.0 of 300.0 MB · 2.0 MB/s · 3 min left"},
		{4 << 20, 4 << 20, 1 << 20, "4.0 of 4.0 MB · 1.0 MB/s"},
	} {
		if got := progressLabel(tc.done, tc.total, tc.rate); got != tc.want {
			t.Errorf("progressLabel(%d, %d, %.0f) = %q, want %q", tc.done, tc.total, tc.rate, got, tc.want)
		}
	}
}
