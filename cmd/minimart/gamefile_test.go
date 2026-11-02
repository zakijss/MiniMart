package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zakijss/minideck/cache"
)

// rom makes a fake N64 ROM in .z64 byte order, n bytes long.
func rom(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x80, 0x37, 0x12, 0x40})
	for i := 4; i < n; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

// swap2 and swap4 turn a .z64 ROM into .v64 and .n64 byte order.
func swap2(b []byte) []byte {
	out := append([]byte{}, b...)
	for i := 0; i+1 < len(out); i += 2 {
		out[i], out[i+1] = out[i+1], out[i]
	}
	return out
}

func swap4(b []byte) []byte {
	out := append([]byte{}, b...)
	for i := 0; i+3 < len(out); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = out[i+3], out[i+2], out[i+1], out[i]
	}
	return out
}

func sha1Hex(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

func TestRomSHA1ByteOrders(t *testing.T) {
	dir := t.TempDir()
	z := rom(300000) // not a multiple of the buffer, to cross chunk ends
	want := sha1Hex(z)
	for name, b := range map[string][]byte{"a.z64": z, "a.v64": swap2(z), "a.n64": swap4(z)} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		got, err := romSHA1(p, "n64")
		if err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", name, got, err, want)
		}
		// without the byte order, a file is hashed as it is
		if plain, _ := romSHA1(p, ""); plain != sha1Hex(b) {
			t.Errorf("%s: plain hash %s", name, plain)
		}
	}
}

func port(sd string, sums ...string) App {
	return App{Name: "Mario", Folder: "Mario64", NeedsFile: &NeedsFile{What: "Super Mario 64 (USA)", Path: "App/Mario64/rom.z64",
		Ext: []string{".z64", ".v64", ".n64"}, SHA1: sums, ByteOrder: "n64"}}
}

func TestValidNeedsFile(t *testing.T) {
	ok := port("", strings.Repeat("a", 40))
	if err := validNeedsFile(ok); err != nil {
		t.Fatal(err)
	}
	bad := []func(nf *NeedsFile){
		func(nf *NeedsFile) { nf.Path = "App/Other/rom.z64" },
		func(nf *NeedsFile) { nf.Path = "App/Mario64/../../x.z64" },
		func(nf *NeedsFile) { nf.Path = "App/Mario64/sub/rom.z64" },
		func(nf *NeedsFile) { nf.Path = "/mnt/SDCARD/App/Mario64/rom.z64" },
		func(nf *NeedsFile) { nf.Path = "App/Mario64/.hidden" },
		func(nf *NeedsFile) { nf.Ext = []string{"z64"} },
		func(nf *NeedsFile) { nf.SHA1 = []string{"not hex"} },
		func(nf *NeedsFile) { nf.What = "" },
		func(nf *NeedsFile) { nf.What = "a\nb" },
		func(nf *NeedsFile) { nf.ByteOrder = "pdp" },
		func(nf *NeedsFile) { nf.Names = []string{"../x.z64"} },
		func(nf *NeedsFile) { nf.Names = []string{"a/b.z64"} },
	}
	for i, f := range bad {
		a := port("", strings.Repeat("a", 40))
		f(a.NeedsFile)
		if validNeedsFile(a) == nil {
			t.Errorf("case %d accepted: %+v", i, a.NeedsFile)
		}
	}
	// the catalogue parser drops an app whose needed file is unsafe
	b, _ := json.Marshal(Catalog{Apps: []App{
		{Name: "Bad", Folder: "Bad", Zip: "https://x/a.zip", SHA256: strings.Repeat("a", 64), NeedsFile: &NeedsFile{What: "x", Path: "App/Other/x.z64"}},
		{Name: "Good", Folder: "Mario64", Zip: "https://x/b.zip", SHA256: strings.Repeat("b", 64), NeedsFile: ok.NeedsFile},
	}})
	c, err := ParseCatalog(b, "t")
	if err != nil || len(c.Apps) != 1 || c.Apps[0].Name != "Good" || c.Apps[0].NeedsFile == nil {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestScanCardFindsMatchingCopy(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	good := rom(8192)
	a := port(sd, sha1Hex(good))
	write := func(rel string, b []byte) string {
		p := filepath.Join(sd, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
		return p
	}
	write("Roms/N64/other.z64", rom(4096))               // wrong dump
	write("Roms/N64/notes.txt", good)                    // wrong extension
	write("Roms/.hidden/copy.z64", good)                 // hidden folder
	write("Roms/a/b/c/d/e/deep.z64", good)               // deeper than the limit
	want := write("Roms/N64/USA/Mario.v64", swap2(good)) // the user's copy, byte-swapped
	if got := scanCard(a.NeedsFile, defaultScan); got != want {
		t.Fatalf("found %q, want %q", got, want)
	}
	if err := copyGameFile(want, a.NeedsFile, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if !a.NeedsFile.present() {
		t.Fatal("not in place")
	}
	if b, _ := os.ReadFile(a.NeedsFile.target()); string(b) != string(swap2(good)) {
		t.Fatal("copy differs")
	}
	// without listed dumps nothing is taken on its own
	if got := scanCard(port(sd).NeedsFile, defaultScan); got != "" {
		t.Fatalf("unchecked file taken: %s", got)
	}
}

func TestScanCardBounds(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	good := rom(8192)
	a := port(sd, sha1Hex(good))
	dir := filepath.Join(sd, "Roms", "N64")
	os.MkdirAll(dir, 0o755)
	for i := 0; i < 20; i++ { // 20 candidates that don't match, sorted before the good one
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("a%03d.z64", i)), rom(4096+i), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "z.z64"), good, 0o644)
	lim := defaultScan
	lim.Hashes = 10
	if got := scanCard(a.NeedsFile, lim); got != "" {
		t.Fatalf("hashed past the limit: %s", got)
	}
	lim = defaultScan
	lim.Entries = 5
	if got := scanCard(a.NeedsFile, lim); got != "" {
		t.Fatalf("read past the entry limit: %s", got)
	}
	lim = defaultScan
	lim.MaxSize = 1000
	if got := scanCard(a.NeedsFile, lim); got != "" {
		t.Fatalf("hashed a file over the size limit: %s", got)
	}
	if got := scanCard(a.NeedsFile, defaultScan); got != filepath.Join(dir, "z.z64") {
		t.Fatalf("within the limits: %q", got)
	}
	// Roms deeper than its limit, Media within its own
	deep := filepath.Join(sd, "Media", "a", "b", "c", "d")
	os.MkdirAll(deep, 0o755)
	os.Remove(filepath.Join(dir, "z.z64"))
	os.WriteFile(filepath.Join(deep, "m.z64"), good, 0o644)
	if got := scanCard(a.NeedsFile, defaultScan); got != "" {
		t.Fatalf("Media searched too deep: %s", got)
	}
	os.Rename(filepath.Join(deep, "m.z64"), filepath.Join(sd, "m.z64"))
	if got := scanCard(a.NeedsFile, defaultScan); got != filepath.Join(sd, "m.z64") {
		t.Fatalf("card root: %q", got)
	}
}

func TestCheckGameFileRefusesMismatch(t *testing.T) {
	sd := t.TempDir()
	good, bad := rom(4096), rom(5000)
	p := filepath.Join(sd, "x.z64")
	os.WriteFile(p, bad, 0o644)
	a := port(sd, sha1Hex(good))
	if err := checkGameFile(p, a.NeedsFile); !errors.Is(err, errNotKnown) {
		t.Fatalf("mismatch: %v", err)
	}
	os.WriteFile(p, swap4(good), 0o644)
	if err := checkGameFile(p, a.NeedsFile); err != nil {
		t.Fatalf("an .n64 copy of a listed dump: %v", err)
	}
	// with no dumps listed, the user's pick is taken as it is
	if err := checkGameFile(p, port(sd).NeedsFile); err != nil {
		t.Fatal(err)
	}
}

func TestFetchGameFile(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	good, bad := rom(4096), rom(5000)
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	f := &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
	a := port(sd, sha1Hex(good))
	os.MkdirAll(filepath.Join(sd, "App", "Mario64"), 0o755)

	// nothing there
	if _, err := f.FetchGameFile(srv.URL+"/games/", a.NeedsFile, func(int64, int64) {}); err == nil || a.NeedsFile.present() {
		t.Fatalf("nothing there: %v", err)
	}
	// a file under the expected name that isn't a listed dump is refused
	files["/games/Super Mario 64 (USA).z64"] = bad
	if _, err := f.FetchGameFile(srv.URL+"/games/", a.NeedsFile, func(int64, int64) {}); !errors.Is(err, errNotKnown) || a.NeedsFile.present() {
		t.Fatalf("mismatch: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(sd, "App", "Mario64")); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
	// a matching .v64 copy under another of the expected names is taken
	files["/games/Super Mario 64 (USA).v64"] = swap2(good)
	name, err := f.FetchGameFile(srv.URL+"/games/", a.NeedsFile, func(int64, int64) {})
	if err != nil || name != "Super Mario 64 (USA).v64" || !a.NeedsFile.present() {
		t.Fatalf("%s %v", name, err)
	}
	// without listed dumps the location isn't asked at all
	if _, err := f.FetchGameFile(srv.URL+"/games/", port(sd).NeedsFile, func(int64, int64) {}); err == nil {
		t.Fatal("fetched an unchecked file")
	}
}

func TestNormalizeGameFiles(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"nas.local/games":               "https://nas.local/games/",
		"http://192.168.0.5:8080/roms/": "http://192.168.0.5:8080/roms/",
		"http://nas.tail1.ts.net/g":     "http://nas.tail1.ts.net/g/",
	}
	for in, want := range cases {
		if got, err := normalizeGameFiles(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"ftp://nas/games", "http://user:pw@nas/games", "https://"} {
		if _, err := normalizeGameFiles(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestCandidateNames(t *testing.T) {
	nf := port("", "").NeedsFile
	got := strings.Join(nf.candidateNames(), "|")
	if got != "Super Mario 64 (USA).z64|Super Mario 64 (USA).v64|Super Mario 64 (USA).n64|rom.z64" {
		t.Fatal(got)
	}
}
