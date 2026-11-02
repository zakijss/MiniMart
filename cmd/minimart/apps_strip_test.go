package main

import (
	"archive/zip"
	"os"
	"path/filepath"
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
