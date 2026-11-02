package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zakijss/minideck/cache"
)

// portZip builds a zip from name -> contents, in order.
func portZip(t testing.TB, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// portServer serves zips by path over TLS and returns a fetcher for it.
func portServer(t testing.TB, zips map[string][]byte) (*httptest.Server, *fetcher) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := zips[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &fetcher{http: srv.Client(), cache: cache.New(t.TempDir())}
}

func sumOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// portCard points the store at an empty card and app folder.
func portCard(t testing.TB) string {
	t.Helper()
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	t.Setenv("MINIDECK_APPDIR", t.TempDir())
	os.MkdirAll(filepath.Join(sd, "Roms", "PORTS", "Shortcuts"), 0o755)
	return sd
}

func installPortT(t testing.TB, f *fetcher, p Port) ([]PortGame, error) {
	t.Helper()
	pp, err := f.DownloadPort(p, func(int64, int64) {})
	if err != nil {
		return nil, err
	}
	return pp.Install(func(int64, int64) {})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPortInstallFreeGame(t *testing.T) {
	sd := portCard(t)
	ports := filepath.Join(sd, "Roms", "PORTS")
	cacheDB := filepath.Join(ports, "Shortcuts", "Shortcuts_cache6.db")
	os.WriteFile(cacheDB, []byte("x"), 0o644)
	z := portZip(t,
		[2]string{"Roms/", ""},
		[2]string{"Roms/PORTS/Games/Ski/ski", "binary"},
		[2]string{"Roms/PORTS/Games/Ski/lib/libSDL2.so", "lib"},
		[2]string{"Roms/PORTS/Imgs/Ski.png", "png"},
		[2]string{"Roms/PORTS/Shortcuts/Ski.notfound", "#!/bin/sh\nGameDir=\"Ski\"\nGameDataFile=\"ski\"\n"},
		[2]string{"README.md", "readme"},
		[2]string{"Tools/convert.exe", "exe"},
		[2]string{"__MACOSX/Roms/PORTS/Games/Ski/._ski", "junk"},
		[2]string{"Roms/PORTS/Games/Ski/.DS_Store", "junk"},
	)
	srv, f := portServer(t, map[string][]byte{"/ski.zip": z})
	p := Port{Name: "SkiFree", Assets: "free", Repo: "a/ski", Version: "1.0", Zip: srv.URL + "/ski.zip", SHA256: sumOf(z), Size: int64(len(z)), Unpacked: 100,
		Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "Ski", Dir: "Ski", Data: "ski", Shipped: true}}}
	if !p.Installable() {
		t.Fatal(p.installErr())
	}
	missing, err := installPortT(t, f, p)
	if err != nil || len(missing) != 0 {
		t.Fatalf("install: %v, missing %v", err, missing)
	}
	for _, want := range []string{"Games/Ski/ski", "Games/Ski/lib/libSDL2.so", "Imgs/Ski.png", "Shortcuts/Ski.port"} {
		if !exists(filepath.Join(ports, want)) {
			t.Errorf("%s not installed", want)
		}
	}
	for _, not := range []string{"Shortcuts/Ski.notfound", "Games/Ski/.DS_Store", "Games/Ski/._ski"} {
		if exists(filepath.Join(ports, not)) {
			t.Errorf("%s should not be there", not)
		}
	}
	if exists(filepath.Join(sd, "README.md")) || exists(filepath.Join(sd, "Tools")) || exists(filepath.Join(ports, "README.md")) {
		t.Error("files outside Roms/PORTS areas were unpacked")
	}
	if exists(cacheDB) {
		t.Error("Onion's list cache wasn't cleared")
	}
	if ents, _ := os.ReadDir(stagingDir()); len(ents) != 0 {
		t.Errorf("staging folder left with %d entries", len(ents))
	}
	st := p.State(loadPortRecords())
	if !st.Installed || !st.Ready() || st.Version != "1.0" {
		t.Fatalf("state %+v", st)
	}
}

func TestPortOwnedGameGoesLiveWithUserFiles(t *testing.T) {
	sd := portCard(t)
	ports := filepath.Join(sd, "Roms", "PORTS")
	z := portZip(t,
		[2]string{"Roms/PORTS/Games/Undertale/butterscotch", "engine"},
		[2]string{"Roms/PORTS/Shortcuts/RPG/Undertale.port", "GameDir=\"Undertale\"\nGameDataFile=\"data.win\"\n"},
	)
	srv, f := portServer(t, map[string][]byte{"/u.zip": z})
	g := PortGame{Shortcut: "RPG/Undertale", Dir: "Undertale", Data: "data.win"}
	p := Port{Name: "Undertale", Assets: "owned", Repo: "a/u", Version: "2", Zip: srv.URL + "/u.zip", SHA256: sumOf(z), Size: int64(len(z)), Root: "Roms/PORTS/", Games: []PortGame{g}}
	missing, err := installPortT(t, f, p)
	if err != nil || len(missing) != 1 {
		t.Fatalf("install: %v, missing %v", err, missing)
	}
	// Onion hides a .notfound shortcut: the store named it so.
	if exists(filepath.Join(ports, "Shortcuts/RPG/Undertale.port")) || !exists(filepath.Join(ports, "Shortcuts/RPG/Undertale.notfound")) {
		t.Fatal("shortcut should be .notfound while data.win is missing")
	}
	if st := p.State(loadPortRecords()); st.Ready() || len(st.Missing) != 1 {
		t.Fatalf("state %+v", st)
	}
	// Three folders down is past Onion's -maxdepth 2: still missing.
	deep := filepath.Join(ports, "Games/Undertale/a/b/data.win")
	os.MkdirAll(filepath.Dir(deep), 0o755)
	os.WriteFile(deep, []byte("x"), 0o644)
	if m := activate(p.Games); len(m) != 1 {
		t.Fatal("found data.win three folders down")
	}
	// One folder down, other case: Onion's find -iname finds it, and the
	// next look at the port (list or details) brings the shortcut in line.
	user := filepath.Join(ports, "Games/Undertale/game/DATA.WIN")
	os.MkdirAll(filepath.Dir(user), 0o755)
	os.WriteFile(user, []byte("mine"), 0o644)
	syncShortcuts(p)
	if !exists(filepath.Join(ports, "Shortcuts/RPG/Undertale.port")) || exists(filepath.Join(ports, "Shortcuts/RPG/Undertale.notfound")) {
		t.Fatal("shortcut should be .port once the data is there")
	}
	if st := p.State(loadPortRecords()); !st.Ready() {
		t.Fatalf("state %+v", st)
	}
	// The files deleted again: the game is hidden again.
	os.Rename(user, user+".away")
	syncShortcuts(p)
	if !exists(filepath.Join(ports, "Shortcuts/RPG/Undertale.notfound")) {
		t.Fatal("shortcut still .port after the data went")
	}
	os.Rename(user+".away", user)
	syncShortcuts(p)
	// Removing the port keeps the user's game files.
	if err := RemovePort(p); err != nil {
		t.Fatal(err)
	}
	if !exists(user) || exists(filepath.Join(ports, "Games/Undertale/butterscotch")) {
		t.Fatal("remove deleted the user's files or kept the engine")
	}
	if userFilesLeft(p) != 2 {
		t.Fatalf("userFilesLeft = %d", userFilesLeft(p))
	}
	if exists(filepath.Join(ports, "Shortcuts/RPG")) {
		t.Error("empty shortcut folder left")
	}
	for _, a := range portAreas[:3] {
		if a != "Imgs" && !exists(filepath.Join(ports, a)) {
			t.Errorf("area folder %s removed", a)
		}
	}
}

func TestPortShippedDataIsntReadyForOwnedGame(t *testing.T) {
	sd := portCard(t)
	z := portZip(t,
		[2]string{"Roms/PORTS/Games/Hexen2/hexen2", "engine"},
		[2]string{"Roms/PORTS/Shortcuts/Hexen II.port", "GameDir=\"Hexen2\"\nGameDataFile=\"hexen2\"\n"},
	)
	srv, f := portServer(t, map[string][]byte{"/h.zip": z})
	p := Port{Name: "Hexen II", Assets: "owned", Repo: "a/h", Version: "1", Zip: srv.URL + "/h.zip", SHA256: sumOf(z), Root: "Roms/PORTS/",
		Games: []PortGame{{Shortcut: "Hexen II", Dir: "Hexen2", Data: "hexen2", Shipped: true}}}
	if _, err := installPortT(t, f, p); err != nil {
		t.Fatal(err)
	}
	st := p.State(loadPortRecords())
	if st.Ready() || len(st.Unchecked) != 1 {
		t.Fatalf("an owned port whose checked file ships with it shows ready: %+v", st)
	}
	if !exists(filepath.Join(sd, "Roms/PORTS/Shortcuts/Hexen II.port")) {
		t.Fatal("shortcut should stay .port (Onion's own check passes)")
	}
}

func TestPortUpdateRemovesStaleFilesKeepsUsers(t *testing.T) {
	sd := portCard(t)
	ports := filepath.Join(sd, "Roms", "PORTS")
	v1 := portZip(t,
		[2]string{"Roms/PORTS/Games/G/g", "v1"},
		[2]string{"Roms/PORTS/Games/G/old/stale.so", "old"},
		[2]string{"Roms/PORTS/Shortcuts/G.port", "GameDir=\"G\"\nGameDataFile=\"g\"\n"},
	)
	v2 := portZip(t,
		[2]string{"Roms/PORTS/Games/G/g", "v2"},
		[2]string{"Roms/PORTS/Shortcuts/G.port", "GameDir=\"G\"\nGameDataFile=\"g\"\n"},
	)
	srv, f := portServer(t, map[string][]byte{"/1.zip": v1, "/2.zip": v2})
	p := Port{Name: "G", Assets: "free", Repo: "a/g", Version: "1", Zip: srv.URL + "/1.zip", SHA256: sumOf(v1), Root: "Roms/PORTS/",
		Games: []PortGame{{Shortcut: "G", Dir: "G", Data: "g", Shipped: true}}}
	if _, err := installPortT(t, f, p); err != nil {
		t.Fatal(err)
	}
	save := filepath.Join(ports, "Games/G/save.dat")
	os.WriteFile(save, []byte("progress"), 0o644)
	p.Version, p.Zip, p.SHA256 = "2", srv.URL+"/2.zip", sumOf(v2)
	pp, err := f.DownloadPort(p, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(pp.Replaces) != 0 {
		t.Fatalf("an update of the store's own files asks to replace %v", pp.Replaces)
	}
	if _, err := pp.Install(func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ports, "Games/G/g")); string(b) != "v2" {
		t.Fatal("not updated")
	}
	if exists(filepath.Join(ports, "Games/G/old")) {
		t.Fatal("file from the old version (or its folder) left")
	}
	if !exists(save) {
		t.Fatal("update deleted the user's save")
	}
	if st := p.State(loadPortRecords()); st.Version != "2" {
		t.Fatalf("recorded version %q", st.Version)
	}
}

func TestPortReplacesCopyInstalledByHand(t *testing.T) {
	sd := portCard(t)
	hand := filepath.Join(sd, "Roms/PORTS/Games/G/g")
	os.MkdirAll(filepath.Dir(hand), 0o755)
	os.WriteFile(hand, []byte("by hand"), 0o644)
	os.WriteFile(filepath.Join(sd, "Roms/PORTS/Shortcuts/G.port"), []byte("x"), 0o644)
	z := portZip(t, [2]string{"Roms/PORTS/Games/G/g", "new"}, [2]string{"Roms/PORTS/Shortcuts/G.port", "GameDir=\"G\"\nGameDataFile=\"g\"\n"})
	srv, f := portServer(t, map[string][]byte{"/g.zip": z})
	p := Port{Name: "G", Assets: "free", Repo: "a/g", Zip: srv.URL + "/g.zip", SHA256: sumOf(z), Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "G", Dir: "G", Data: "g"}}}
	if st := p.State(loadPortRecords()); !st.ByHand || st.Installed {
		t.Fatalf("state %+v", st)
	}
	pp, err := f.DownloadPort(p, func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Discard()
	if len(pp.Replaces) != 2 {
		t.Fatalf("Replaces = %v", pp.Replaces)
	}
}

func TestPortRefusesBadZips(t *testing.T) {
	cases := map[string][]byte{
		"slip":        portZip(t, [2]string{"Roms/PORTS/Games/../../../evil", "x"}),
		"winslip":     portZip(t, [2]string{`Roms\PORTS\Games\X\..\..\..\evil`, "x"}),
		"nested slip": portZip(t, [2]string{"Roms/PORTS/Games/G/../../Shortcuts/../../../evil.sh", "x"}),
		"case clash":  portZip(t, [2]string{"Roms/PORTS/Games/G/data", "a"}, [2]string{"Roms/PORTS/Games/g/DATA", "b"}),
		"colon":       portZip(t, [2]string{"Roms/PORTS/Games/G/a:b", "x"}),
		"empty":       portZip(t, [2]string{"README.md", "x"}, [2]string{"Roms/PORTS/notes.txt", "x"}),
	}
	for name, z := range cases {
		t.Run(name, func(t *testing.T) {
			sd := portCard(t)
			srv, f := portServer(t, map[string][]byte{"/z.zip": z})
			p := Port{Name: "Z", Repo: "a/z", Zip: srv.URL + "/z.zip", SHA256: sumOf(z), Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "Z", Dir: "G", Data: "data"}}}
			if _, err := installPortT(t, f, p); err == nil {
				t.Fatal("installed")
			}
			if exists(filepath.Join(sd, "evil")) || exists(filepath.Join(filepath.Dir(sd), "evil")) || exists(filepath.Join(sd, "Roms/PORTS/Games/G")) {
				t.Fatal("wrote something")
			}
			if len(loadPortRecords()) != 0 {
				t.Fatal("recorded an install")
			}
		})
	}
}

func TestPortRefusesMismatchAndNoRoom(t *testing.T) {
	portCard(t)
	z := portZip(t, [2]string{"Roms/PORTS/Games/G/g", "x"}, [2]string{"Roms/PORTS/Shortcuts/G.port", "x"})
	srv, f := portServer(t, map[string][]byte{"/g.zip": z})
	p := Port{Name: "G", Repo: "a/g", Zip: srv.URL + "/g.zip", SHA256: strings.Repeat("0", 64), Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "G", Dir: "G", Data: "g"}}}
	if _, err := installPortT(t, f, p); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("mismatch: %v", err)
	}
	if ents, _ := os.ReadDir(stagingDir()); len(ents) != 0 {
		t.Fatal("download left behind")
	}
	p.SHA256, p.Unpacked = sumOf(z), 1<<50
	if _, err := installPortT(t, f, p); err == nil || !strings.Contains(err.Error(), "space") {
		t.Fatalf("no room: %v", err)
	}
	if t, short := spaceText(1 << 50); !short || !strings.Contains(t, "only") {
		panic(t)
	}
}

func TestPortLooseAndLauncherShortcuts(t *testing.T) {
	sd := portCard(t)
	ports := filepath.Join(sd, "Roms", "PORTS")
	// Made on Windows, for another firmware: Games\ and a .port at the top.
	z := portZip(t,
		[2]string{`Insaniquarium.port`, "#!/bin/sh\ncd Games/Insaniquarium && ./Insaniquarium\n"},
		[2]string{`Games\Insaniquarium\Insaniquarium`, "bin"},
		[2]string{`Games\Insaniquarium\script.sh`, "sh"},
	)
	srv, f := portServer(t, map[string][]byte{"/i.zip": z})
	p := Port{Name: "Insaniquarium", Assets: "owned", Repo: "a/i", Zip: srv.URL + "/i.zip", SHA256: sumOf(z), Loose: true,
		Games: []PortGame{{Shortcut: "Insaniquarium"}}}
	if !p.Installable() {
		t.Fatal(p.installErr())
	}
	if _, err := installPortT(t, f, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Shortcuts/Insaniquarium.port", "Games/Insaniquarium/Insaniquarium", "Games/Insaniquarium/script.sh"} {
		if !exists(filepath.Join(ports, want)) {
			t.Errorf("%s missing", want)
		}
	}
	if exists(filepath.Join(ports, "Insaniquarium.port")) {
		t.Error("loose shortcut left at the top")
	}
	if st := p.State(loadPortRecords()); !st.Installed || len(st.Missing) != 0 {
		t.Fatalf("a launcher shortcut is never missing data: %+v", st)
	}
}

func TestParsePortsChecksEntries(t *testing.T) {
	sha := strings.Repeat("a", 64)
	good := Port{Name: "Good", Repo: "a/b", Zip: "https://x/y.zip", SHA256: sha, Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "Cat/Good", Dir: "Good", Data: "good.dat"}}}
	bad := func(f func(*Port)) Port { p := good; p.Games = append([]PortGame(nil), good.Games...); f(&p); return p }
	doc := map[string]any{"ports": []Port{
		good,
		{Name: "Guide", Upstream: "https://github.com/a/c/releases"},
		bad(func(p *Port) { p.Name = "Http"; p.Zip = "http://x/y.zip" }),
		bad(func(p *Port) { p.Name = "Sha"; p.SHA256 = "abc" }),
		bad(func(p *Port) { p.Name = "Shortcut"; p.Games[0].Shortcut = "../../evil" }),
		bad(func(p *Port) { p.Name = "Dir"; p.Games[0].Dir = "a/../../b" }),
		bad(func(p *Port) { p.Name = "Data"; p.Games[0].Data = "../x" }),
		bad(func(p *Port) { p.Name = "Root"; p.Root = "../" }),
		bad(func(p *Port) { p.Name = "Ctl\x07" }),
		{Name: "App", App: "Bad/Folder"},
	}}
	b, _ := json.Marshal(doc)
	ps, err := ParsePorts(b, "test")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Port{}
	for _, p := range ps {
		got[p.Name] = p
	}
	if _, ok := got["Ctl\x07"]; ok || len(ps) != 9 {
		t.Fatalf("%d entries kept", len(ps))
	}
	if !got["Good"].Installable() {
		t.Fatal(got["Good"].installErr())
	}
	for _, n := range []string{"Guide", "Http", "Sha", "Shortcut", "Dir", "Data", "Root", "App"} {
		if got[n].Installable() || got[n].App != "" {
			t.Errorf("%s is installable", n)
		}
	}
	if _, err := ParsePorts([]byte("<html>"), "x"); err == nil {
		t.Fatal("parsed HTML")
	}
}

// TestBuiltinPorts checks the list compiled into the store, and the
// published one with STORE_PORTS=FILE: every entry with a download must
// pass the store's checks, so the generator and the store agree.
func TestBuiltinPorts(t *testing.T) {
	check := func(name string, b []byte, min int) {
		var raw struct {
			Ports []Port `json:"ports"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		ps, err := ParsePorts(b, name)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for i, p := range ps {
			if raw.Ports[i].Zip != "" && !p.Installable() {
				t.Errorf("%s: %s: %v", name, p.Name, p.installErr())
			}
			if p.Installable() {
				n++
			}
		}
		if n < min {
			t.Errorf("%s: only %d installable ports", name, n)
		}
	}
	check("built in", builtinPorts, 20)
	if p := os.Getenv("STORE_PORTS"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		check(p, b, 20)
	}
}

// TestPortStress installs and removes a port of maxPortFiles files, and
// refuses one more.
func TestPortStress(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	sd := portCard(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(n string) {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		w.Write([]byte("x"))
	}
	add("Roms/PORTS/Shortcuts/Big.port")
	for i := 1; i < maxPortFiles; i++ {
		add(fmt.Sprintf("Roms/PORTS/Games/Big/d%03d/f%05d", i%400, i))
	}
	zw.Close()
	z := buf.Bytes()
	srv, f := portServer(t, map[string][]byte{"/big.zip": z})
	p := Port{Name: "Big", Repo: "a/big", Zip: srv.URL + "/big.zip", SHA256: sumOf(z), Root: "Roms/PORTS/", Games: []PortGame{{Shortcut: "Big", Dir: "Big", Data: "f00001"}}}
	start := time.Now()
	if _, err := installPortT(t, f, p); err != nil {
		t.Fatal(err)
	}
	inst := time.Since(start)
	if st := p.State(loadPortRecords()); !st.Ready() {
		t.Fatalf("state %+v", st)
	}
	start = time.Now()
	if err := RemovePort(p); err != nil {
		t.Fatal(err)
	}
	rm := time.Since(start)
	if exists(filepath.Join(sd, "Roms/PORTS/Games/Big")) {
		t.Fatal("folders left after remove")
	}
	t.Logf("%d files: install %v, remove %v", maxPortFiles, inst, rm)

	var buf2 bytes.Buffer
	zw = zip.NewWriter(&buf2)
	for i := 0; i <= maxPortFiles; i++ {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("Roms/PORTS/Games/Big/f%05d", i), Method: zip.Store})
		w.Write([]byte("x"))
	}
	zw.Close()
	z2 := buf2.Bytes()
	srv2, f2 := portServer(t, map[string][]byte{"/big.zip": z2})
	p.Zip, p.SHA256 = srv2.URL+"/big.zip", sumOf(z2)
	if _, err := installPortT(t, f2, p); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("over the limit: %v", err)
	}
}
