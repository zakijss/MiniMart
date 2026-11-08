package main

import (
	"archive/zip"
	"bytes"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// zipOf makes a zip of the files (name -> contents).
func zipOf(t *testing.T, files map[string]string) *zip.Reader {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for n, c := range files {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(c))
	}
	w.Close()
	zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

const cfg = `{"label": "My App", "launch": "launch.sh", "description": "Does a thing"}`

func TestLayouts(t *testing.T) {
	cases := []struct {
		files map[string]string
		top   string
		want  []layout
	}{
		{map[string]string{"App/MyApp/config.json": cfg, "App/MyApp/launch.sh": "", "README.md": ""}, "",
			[]layout{{Dir: "App/MyApp/", Strip: "App/", Folder: "MyApp"}}},
		{map[string]string{"MyApp/config.json": cfg}, "",
			[]layout{{Dir: "MyApp/", Strip: "", Folder: "MyApp"}}},
		{map[string]string{"config.json": cfg, "launch.sh": ""}, "",
			[]layout{{Dir: "", Strip: "", Wrap: true}}},
		{map[string]string{"r-abc/App/config.json": cfg}, "r-abc/",
			[]layout{{Dir: "r-abc/App/", Strip: "r-abc/App/", Wrap: true}}},
		{map[string]string{"r-abc/config.json": cfg}, "r-abc/",
			[]layout{{Dir: "r-abc/", Strip: "r-abc/", Wrap: true}}},
		{map[string]string{"r-abc/src/App/X/config.json": cfg, "r-abc/src/App/X/sub/config.json": cfg}, "r-abc/",
			[]layout{{Dir: "r-abc/src/App/X/", Strip: "r-abc/src/App/", Folder: "X"}}},
		// not apps: an emulator, a theme, a config.json without launch, MinUI and macOS clutter
		{map[string]string{"Emu/GB/config.json": cfg, "Themes/T/config.json": cfg, "App/NoLaunch/config.json": `{"label": "x"}`,
			"MinUI/Tools/X.pak/config.json": cfg, "__MACOSX/App/A/config.json": cfg, `App\Win\config.json`: cfg}, "", nil},
		// two apps in one zip
		{map[string]string{"App/A/config.json": cfg, "App/B/config.json": cfg}, "",
			[]layout{{Dir: "App/A/", Strip: "App/", Folder: "A"}, {Dir: "App/B/", Strip: "App/", Folder: "B"}}},
	}
	for i, c := range cases {
		info := inspectZip(zipOf(t, c.files), c.top)
		var got []layout
		for _, a := range info.Apps {
			got = append(got, a.layout)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: %+v, want %+v", i, got, c.want)
		}
	}
}

func TestInspectZipContents(t *testing.T) {
	info := inspectZip(zipOf(t, map[string]string{
		"App/A/config.json":           cfg,
		"App/A/launch.sh":             "#!/bin/sh\n./a\n",
		"App/A/data/settings.cfg":     "token=\n",
		".tmp_update/startup/a.sh":    "#!/bin/sh\n",
		"Roms/PORTS/A.sh":             "",
		"App/A/bin/a":                 "\x7fELF not really a program but https://api.example.com/v1",
		"App/A/._launch.sh":           "junk",
		"App/A/__MACOSX/whatever.txt": "junk",
	}), "")
	if len(info.Apps) != 1 {
		t.Fatalf("%+v", info.Apps)
	}
	a := info.Apps[0]
	if a.Config.Label != "My App" || a.Files["launch.sh"] == nil || a.Files["data/settings.cfg"] == nil || a.Files["._launch.sh"] != nil {
		t.Errorf("files: %+v", a.Files)
	}
	if len(a.Bins["bin/a"]) != 1 {
		t.Errorf("program strings: %+v", a.Bins)
	}
	if info.Boot["a.sh"] == nil || !reflect.DeepEqual(info.Top, []string{".tmp_update", "App", "Roms"}) {
		t.Errorf("boot %v, top %v", info.Boot, info.Top)
	}
}

func TestWrapFolder(t *testing.T) {
	files := map[string][]byte{"launch.sh": []byte("cd /mnt/SDCARD/App/Clocky/\n./clock\n"), "config.json": []byte(`{"icon": "/mnt/SDCARD/App/Clocky/icon.png"}`)}
	if f := wrapFolder(files, "Clock", "o/r"); f != "Clocky" {
		t.Fatal(f)
	}
	// a reserved folder (Onion's own Clock) is never used
	files = map[string][]byte{"launch.sh": []byte("cd /mnt/SDCARD/App/Clock\n")}
	if f := wrapFolder(files, "Clock", "muxiasama/-MiyooClock"); f != "MiyooClock" {
		t.Fatal(f)
	}
	if f := wrapFolder(nil, "Petals Around the Rose", "o/petals"); f != "PetalsAroundTheRose" {
		t.Fatal(f)
	}
}

func lines(script string) []srcLine {
	return scriptLines(map[string][]byte{"launch.sh": []byte(script)})
}

func TestOutsideWrites(t *testing.T) {
	script := `#!/bin/sh
cd $(dirname "$0")
ROMS=/mnt/SDCARD/Roms
cp game.zip "$ROMS/PORTS/"           # into Roms
cp /mnt/SDCARD/Roms/x.zip ./        # reading Roms is not writing
echo performance > /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor
mkdir -p /mnt/SDCARD/App/MyApp/data  # its own folder
sed -i 's/a/b/' /appconfigs/system.json
rclone copy /mnt/SDCARD/Saves cloud:saves/
rclone copy cloud:saves/ /mnt/SDCARD/Saves/CurrentProfile/
cd /mnt/SDCARD
mkdir -p Backups/1
cd "$SOMEWHERE"
rm -rf ../../thing
date -s "2026-01-01"
`
	paths, from := outside(lines(script), "MyApp")
	want := []string{"Roms/PORTS", "/appconfigs/system.json", "Saves/CurrentProfile", "Backups/1"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("%v, want %v", paths, want)
	}
	if from["Roms/PORTS"][0] != "launch.sh" {
		t.Error(from)
	}
	if f := cpuWrites(lines(script)); len(f) != 1 {
		t.Error("cpu", f)
	}
	if f := clockSets(lines(script)); len(f) != 1 {
		t.Error("clock", f)
	}
	py := scriptLines(map[string][]byte{"app.py": []byte("open('/mnt/SDCARD/RetroArch/cpuclock.txt', 'w')\nopen('/mnt/SDCARD/Roms/x', 'r')\n")})
	if paths, _ := outside(py, "MyApp"); !reflect.DeepEqual(paths, []string{"RetroArch/cpuclock.txt"}) {
		t.Errorf("python: %v", paths)
	}
}

func TestSelfUpdatesAndHosts(t *testing.T) {
	ls := scriptLines(map[string][]byte{
		"update.sh": []byte("curl -L https://api.github.com/repos/Owner/Repo/releases/latest -o /tmp/x\n"),
		"pull.sh":   []byte("git pull\n"),
		"radio.sh":  []byte("wget -O - http://radio.example.net/stream\n"),
	})
	bins := map[string][]string{"bin/app": {"https://github.com/owner/repo/releases/latest/download/app.zip", "Checking for updates..."}, "lib/libSDL2.so": {"https://www.libsdl.org/x"}}
	got := strings.Join(selfUpdates(ls, bins, "owner/repo"), " ")
	for _, want := range []string{"own GitHub repository (update.sh)", "run git to fetch new code (pull.sh)", "Its program app holds the address", `"Checking for updates" in its program app`} {
		if !strings.Contains(got, want) {
			t.Errorf("updates %q lacks %q", got, want)
		}
	}
	if u := selfUpdates(scriptLines(map[string][]byte{"a.sh": []byte("curl https://example.org/data.json\n")}), nil, "o/r"); len(u) != 0 {
		t.Errorf("plain download taken for an update: %v", u)
	}
	hosts, from := hostsNamed(ls, map[string][]string{"bin/app": {"see https://api.weather.com/v3?x and https://github.com/owner/repo for help, https://old.reddit.comhttps://x"}}, "owner/repo")
	if !reflect.DeepEqual(hosts, []string{"radio.example.net", "api.github.com", "api.weather.com"}) {
		t.Errorf("hosts %v", hosts)
	}
	if from["api.weather.com"][0] != "app" {
		t.Error(from)
	}
}

func TestRuntimes(t *testing.T) {
	ls := lines("python3 main.py\n")
	needs, facts := runtimeNeeds(ls, []string{"main.py"})
	if len(needs) != 1 || !strings.HasPrefix(needs[0], "Python") || len(facts) != 0 {
		t.Errorf("%v %v", needs, facts)
	}
	needs, facts = runtimeNeeds(ls, []string{"main.py", "bin/python3"})
	if len(needs) != 0 || len(facts) != 1 {
		t.Errorf("bundled: %v %v", needs, facts)
	}
	needs, facts = runtimeNeeds(lines("/mnt/SDCARD/.tmp_update/bin/parasyte/python2 app.py\n"), nil)
	if len(needs) != 0 || len(facts) != 1 || !strings.Contains(facts[0], "comes with Onion") {
		t.Errorf("Onion's: %v %v", needs, facts)
	}
}

func TestAccountEvidence(t *testing.T) {
	if m := accountMentions("Put your free TMDB API key in data/config.json. Enter your password once."); !reflect.DeepEqual(m, []string{"an API key", "a password"}) {
		t.Error(m)
	}
	if m := accountMentions("This app has no passwords, and an API key list is in the docs."); len(m) != 0 {
		t.Error("passing mention counted:", m)
	}
	got := secretSettings(map[string][]byte{"data/config.json": []byte(`{"api_key": "", "server": ""}`), "notes.txt": []byte("token: x"), "config.json": []byte(`{"token": 1}`)})
	if !reflect.DeepEqual(got, []string{"data/config.json (api_key)"}) {
		t.Error(got)
	}
}

const romReadme = "Put your ROM in the game's folder as `App/<Game>/rom.z64`. Any .z64, .v64 or .n64 dump works.\n\n" +
	"| Game | ROM folder | Version | SHA-1 |\n|---|---|---|---|\n" +
	"| Super Mario 64 | `App/SuperMario64/` | Super Mario 64 (USA) | `9bef1128717f958171a4afac3ed78ee2bb4e86ce` |\n" +
	"| Star Fox 64 | `App/StarFox64/` | Star Fox 64 (USA), version 1.1 | `09f0d105f476b00efa5303a3ebc42e60a7753b7a` |\n"

func TestNeedsFileFromReadme(t *testing.T) {
	nf := needsFileFrom(romReadme, "StarFox64")
	want := &NeedsFile{What: "Star Fox 64 (USA), version 1.1", Path: "App/StarFox64/rom.z64", Ext: []string{".z64", ".v64", ".n64"},
		SHA1: []string{"09f0d105f476b00efa5303a3ebc42e60a7753b7a"}, ByteOrder: "n64"}
	if !reflect.DeepEqual(nf, want) {
		t.Fatalf("%+v", nf)
	}
	if needsFileFrom(romReadme, "Other") != nil || needsFileFrom("# An app\n", "StarFox64") != nil {
		t.Fatal("found a needed file where there is none")
	}
}

func TestCategory(t *testing.T) {
	cases := []struct{ name, desc, want string }{
		{"Pixel Reader", "An ebook reader for EPUB and text files", "Reading"},
		{"MiniAmp", "Classic-skin music player", "Media"},
		{"Carrier", "qBittorrent remote", "Network"},
		{"CPU Overclock", "Choose CPU clock speed", "System"},
		{"Petals Around the Rose", "The classic dice logic game", "Games"},
		{"Super Mario 64", "Native port. Needs your own ROM.", "Emulators and ports"},
		{"Htop", "an interactive process viewer", "Tools"},
		{"Zzz", "", "Tools"},
	}
	for _, c := range cases {
		if got := category(c.name, c.desc, "", ""); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestVersionOf(t *testing.T) {
	cases := [][3]string{
		{"v1.2.3", "", "1.2.3"}, {"1.0", "", "1.0"}, {"SearchFilter-1.2.3", "", "1.2.3"},
		{"v1.0.0-rc.7", "", "1.0.0-rc.7"}, {"miyoomini", "Clock 1.0.0", "1.0.0"}, {"latest", "", "latest"},
	}
	for _, c := range cases {
		if got := versionOf(c[0], c[1]); got != c[2] {
			t.Errorf("%v: %s", c, got)
		}
	}
}

func TestDescription(t *testing.T) {
	if d := description("", "Probably the best app", "# Title\n\nA plain first paragraph.\n", "X"); d != "A plain first paragraph." {
		t.Errorf("opinion not skipped: %q", d)
	}
	if d := description("", "", "", "X"); d != "X, an app for Onion." {
		t.Error(d)
	}
	long := strings.Repeat("word ", 60)
	if d := description(long, "", "", "X"); len(d) > 190 || !strings.HasSuffix(d, "…") {
		t.Errorf("%d %q", len(d), d)
	}
}

func TestPicksGoIsValidGo(t *testing.T) {
	src, err := picksGo(Catalog{Generated: "2026-11-02", Apps: []App{{Name: `Q "uoted"`, Folder: "Q", Repo: "o/r", Category: "Tools",
		Facts: []string{"a", "b"}, NeedsFile: &NeedsFile{What: "w", Path: "App/Q/x.z64", Ext: []string{".z64"}, SHA1: []string{"x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(src), "// Code generated by tools/catalog; DO NOT EDIT.") {
		t.Error("no generated header")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "picks.go", src, 0); err != nil {
		t.Fatal(err)
	}
}

func TestSeedRepos(t *testing.T) {
	repos, wrapped, err := seedRepos("testdata/seed.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repos, []string{"a/one", "b/two"}) || !reflect.DeepEqual(wrapped, map[string]string{"b/two": "Two"}) {
		t.Fatalf("%v %v", repos, wrapped)
	}
}

// An app built to replace an Onion app, its zip using that app's folder, is
// left out unless hints.json gives it a folder of its own; then it unpacks
// there, beside the original.
func TestFolderHint(t *testing.T) {
	info := inspectZip(zipOf(t, map[string]string{
		"BatteryMonitorUI/config.json": cfg,
		"BatteryMonitorUI/launch.sh":   "#!/bin/sh\ncd $(dirname \"$0\")\n./bm\n",
	}), "")
	if len(info.Apps) != 1 {
		t.Fatalf("%+v", info.Apps)
	}
	g := &gen{hints: map[string]hint{}, wrapped: map[string]string{}}
	p := pin{url: "https://example.com/bm.zip", info: info}
	if _, err := g.entry("o/bm", &repoInfo{}, p, info.Apps[0], ""); err == nil {
		t.Fatal("an app over Onion's Battery Monitor was let in")
	}
	g.hints["o/bm#BatteryMonitorUI"] = hint{Folder: "BatteryMonitorV2", Facts: []string{"It can write the power chip's settings."}}
	a, err := g.entry("o/bm", &repoInfo{}, p, info.Apps[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Folder != "BatteryMonitorV2" || a.Strip != "BatteryMonitorUI/" || !a.Wrap {
		t.Fatalf("folder %q strip %q wrap %v", a.Folder, a.Strip, a.Wrap)
	}
	if !strings.Contains(strings.Join(a.Facts, " "), "power chip") {
		t.Errorf("hinted fact missing: %v", a.Facts)
	}
}
