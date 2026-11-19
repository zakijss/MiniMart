package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnzipAppStrip(t *testing.T) {
	d := t.TempDir()
	zp := filepath.Join(d, "a.zip")
	f, _ := os.Create(zp)
	w := zip.NewWriter(f)
	for _, n := range []string{"App/Foo/config.json", "App/Foo/bin/x", "__MACOSX/App/._Foo", "App/Other/y", "README.md"} {
		e, _ := w.Create(n)
		e.Write([]byte(n))
	}
	w.Close()
	f.Close()
	out := filepath.Join(d, "App")
	if err := unzipApp(zp, out, "App/", "Foo"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Foo/config.json", "Foo/bin/x"} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 {
		t.Errorf("only Foo should be unpacked, got %d entries", len(ents))
	}
	for _, bad := range []string{"", "/App/", "../", "App"} {
		if err := unzipApp(zp, out, bad, "Foo"); err == nil {
			t.Errorf("strip %q must be refused", bad)
		}
	}
	if err := unzipApp(zp, out, "App/", "Missing"); err == nil {
		t.Error("an empty result must be an error")
	}
}

// An update that fails part way, or whose zip has no config.json, leaves
// the installed app exactly as it was: nothing is renamed into place until
// every file is written.
func TestFailedUpdateKeepsApp(t *testing.T) {
	d := t.TempDir()
	out := filepath.Join(d, "App")
	app := filepath.Join(out, "Foo")
	os.MkdirAll(app, 0755)
	old := map[string]string{"config.json": "old", "a.txt": "old", "settings.json": "mine"}
	for n, c := range old {
		os.WriteFile(filepath.Join(app, n), []byte(c), 0644)
	}
	zipOf := func(names ...string) string {
		zp := filepath.Join(d, "u.zip")
		f, _ := os.Create(zp)
		w := zip.NewWriter(f)
		for _, n := range names {
			e, _ := w.Create(n)
			e.Write([]byte("new"))
		}
		w.Close()
		f.Close()
		return zp
	}
	check := func(when string) {
		t.Helper()
		for n, c := range old {
			if b, _ := os.ReadFile(filepath.Join(app, n)); string(b) != c {
				t.Errorf("%s: %s is %q, want %q", when, n, b, c)
			}
		}
		ents, _ := os.ReadDir(app)
		for _, e := range ents {
			if _, ok := old[e.Name()]; !ok && e.Name() != "b.txt.store-new" {
				t.Errorf("%s: %s left behind", when, e.Name())
			}
		}
	}

	// b.txt can't be written (its temporary name is taken by a folder)
	os.MkdirAll(filepath.Join(app, "b.txt.store-new", "x"), 0755)
	if err := extractApp(zipOf("Foo/config.json", "Foo/a.txt", "Foo/b.txt"), out, "", "Foo", false); err == nil {
		t.Fatal("a failed write must be an error")
	}
	check("failed write")
	os.RemoveAll(filepath.Join(app, "b.txt.store-new"))

	if err := extractApp(zipOf("Foo/a.txt", "Foo/b.txt"), out, "", "Foo", false); err == nil || !strings.Contains(err.Error(), "didn't contain") {
		t.Fatalf("no config.json: %v", err)
	}
	check("no config.json")

	if err := extractApp(zipOf("Foo/config.json", "Foo/a.txt"), out, "", "Foo", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "a.txt")); string(b) != "new" {
		t.Errorf("a.txt is %q after the update", b)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "settings.json")); string(b) != "mine" {
		t.Error("the update lost settings.json")
	}
}
