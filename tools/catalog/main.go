// Command catalog builds MiniMart's "Community apps" catalogue from
// GitHub, with no entry written by hand. It finds candidate repositories
// (GitHub searches by topic and keyword, plus the repositories already in
// the catalogue), pins each one's newest release that has an Onion app zip
// (or, with no such release, its default branch at one commit), downloads
// it, checks that it holds an Onion app folder, and derives the app's facts
// from its files and README (see evidence.go) and its category (see
// category.go). Repositories in exclude.json are left out; hints.json adds
// the store settings a repository can't show (which file holds a sign-in).
//
// It writes the catalogue as JSON in the format the store reads, and can
// write the store's compiled-in copy (cmd/minimart/picks.go) too:
//
//	go run ./tools/catalog -out build/catalog/community.json -picks cmd/minimart/picks.go
//
// GITHUB_TOKEN raises GitHub's API limit; API replies and downloads are
// kept under -cache, so running it again soon asks GitHub nothing new.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// rulesVersion changes when the rules that derive an entry change, so a
// previous catalogue's entries (-prev) are derived afresh.
const rulesVersion = 1

// App is a catalogue entry, in the store's format (cmd/minimart App).
type App struct {
	Name      string     `json:"name"`
	Folder    string     `json:"folder"`
	Desc      string     `json:"desc"`
	Repo      string     `json:"repo"`
	License   string     `json:"license"`
	Zip       string     `json:"zip"`
	SHA256    string     `json:"sha256"`
	Version   string     `json:"version"`
	Released  string     `json:"released"`
	Size      int64      `json:"size"`
	Strip     string     `json:"strip,omitempty"`
	Wrap      bool       `json:"wrap,omitempty"`
	Secure    string     `json:"secure,omitempty"`
	Tailnet   bool       `json:"tailnet,omitempty"`
	Account   string     `json:"account,omitempty"`
	Updates   string     `json:"updates,omitempty"`
	System    string     `json:"system,omitempty"`
	Boot      []string   `json:"boot,omitempty"`
	Needs     []string   `json:"needs,omitempty"`
	Facts     []string   `json:"facts,omitempty"`
	Category  string     `json:"category"`
	NeedsFile *NeedsFile `json:"needsfile,omitempty"`
}

// NeedsFile is the store's NeedsFile: a game file the user supplies.
type NeedsFile struct {
	What      string   `json:"what"`
	Path      string   `json:"path"`
	Ext       []string `json:"ext,omitempty"`
	SHA1      []string `json:"sha1,omitempty"`
	ByteOrder string   `json:"byteorder,omitempty"`
}

// Catalog is the file written: the store reads name and apps.
type Catalog struct {
	Name      string `json:"name"`
	Generated string `json:"generated"`
	Rules     int    `json:"rules"`
	Apps      []App  `json:"apps"`
}

// Scan records what was looked at and left out, so the next run can skip
// downloads already found to hold no app.
type Scan struct {
	Rules   int               `json:"rules"`
	NoApp   map[string]string `json:"noapp"`   // zip URL -> why
	Skipped map[string]string `json:"skipped"` // owner/repo -> why
}

// hint is what hints.json adds to an app.
type hint struct {
	Secure  string `json:"secure"`
	Tailnet bool   `json:"tailnet"`
	// Folder: install the zip's folder under this name instead. For an app
	// built to replace an Onion app, whose zip uses that app's folder: it
	// goes in beside the original rather than over it. Keyed by the zip's
	// folder ("owner/repo#BatteryMonitorUI").
	Folder string `json:"folder"`
	// Facts: statements about the app the generator can't find in its
	// files, added to its page (what its own menus can change, say).
	Facts []string `json:"facts"`
}

type gen struct {
	c        *client
	zips     string // where downloads are kept
	keepZips bool
	exclude  map[string]string
	hints    map[string]hint
	wrapped  map[string]string    // owner/repo -> folder a wrapped app had before
	known    map[string]*repoInfo // repositories the searches returned, by lower-case name
	prev     map[string]App       // previous entries, by zip URL + "#" + folder
	noApp    map[string]string
	scan     Scan
	today    string
}

func main() {
	out := flag.String("out", "build/catalog/community.json", "the catalogue to write")
	scanOut := flag.String("scan", "build/catalog/scan.json", "where to write what was looked at and left out")
	picks := flag.String("picks", "", "also write the store's compiled-in list here (cmd/minimart/picks.go)")
	seedFile := flag.String("seeds", "cmd/minimart/picks.go", "a Go or JSON file whose \"repo\" entries are always looked at")
	cacheDir := flag.String("cache", "build/catalog-cache", "where API replies and downloads are kept")
	ttl := flag.Duration("ttl", 12*time.Hour, "how long a cached API reply is used")
	keep := flag.Bool("keep-zips", true, "keep downloaded zips in the cache")
	prevFile := flag.String("prev", "", "a previous catalogue whose entries are reused when their zip is unchanged")
	prevScan := flag.String("prev-scan", "", "a previous scan.json: zips it found holding no app aren't downloaded again")
	minApps := flag.Int("min", 0, "fail, writing nothing, if fewer apps than this are found")
	only := flag.String("only", "", "look at these repositories only (comma-separated owner/repo)")
	noSearch := flag.Bool("no-search", false, "look only at the seed repositories")
	dir := flag.String("dir", "tools/catalog", "the folder with exclude.json and hints.json")
	portsOut := flag.String("ports-out", "", "also write the ports list here (see ports.go)")
	portsBuiltin := flag.String("ports-builtin", "", "and the store's compiled-in copy of it here (cmd/minimart/ports_builtin.json)")
	portsList := flag.String("ports-list", upstreamPortsList, "the MiyooMini-Ports list to read")
	portsOnly := flag.Bool("ports-only", false, "write only the ports list")
	minPorts := flag.Int("min-ports", 0, "fail, writing no ports list, if fewer installable ports than this are found")
	flag.Parse()
	log.SetFlags(0)

	g := &gen{
		c:        newClient(filepath.Join(*cacheDir, "http"), os.Getenv("GITHUB_TOKEN"), *ttl),
		zips:     filepath.Join(*cacheDir, "zips"),
		keepZips: *keep,
		prev:     map[string]App{},
		known:    map[string]*repoInfo{},
		noApp:    map[string]string{},
		scan:     Scan{Rules: rulesVersion, NoApp: map[string]string{}, Skipped: map[string]string{}},
		today:    time.Now().UTC().Format("2006-01-02"),
	}
	if err := readMap(filepath.Join(*dir, "exclude.json"), &g.exclude); err != nil {
		log.Fatal(err)
	}
	if err := readMap(filepath.Join(*dir, "hints.json"), &g.hints); err != nil {
		log.Fatal(err)
	}
	g.loadPrev(*prevFile, *prevScan)

	if *portsOut != "" {
		var pex map[string]string
		if err := readMap(filepath.Join(*dir, "ports-exclude.json"), &pex); err != nil {
			log.Fatal(err)
		}
		doc, err := g.buildPorts(*portsList, pex)
		if err != nil {
			log.Fatal(err)
		}
		n := 0
		for _, p := range doc.Ports {
			if p.Zip != "" {
				n++
			}
		}
		if n < *minPorts {
			log.Fatalf("only %d installable ports (fewer than -min-ports %d): nothing written", n, *minPorts)
		}
		for _, f := range []string{*portsOut, *portsBuiltin} {
			if f == "" {
				continue
			}
			if err := writeJSON(f, doc); err != nil {
				log.Fatal(err)
			}
		}
		log.Printf("%d ports listed, %d install from MiniMart", len(doc.Ports), n)
		if *portsOnly {
			return
		}
	}

	var repos []string
	if *only != "" {
		repos = strings.Split(*only, ",")
	} else {
		seeds, wrapped, err := seedRepos(*seedFile)
		if err != nil {
			log.Fatal(err)
		}
		g.wrapped = wrapped
		repos = seeds
		if !*noSearch {
			found, err := g.discover()
			if err != nil {
				log.Printf("search: %v", err)
			}
			repos = mergeRepos(seeds, found)
		}
	}
	log.Printf("%d repositories to look at", len(repos))

	var all []App
	for _, r := range repos {
		apps, err := g.repoApps(r)
		if err != nil {
			g.scan.Skipped[r] = err.Error()
			log.Printf("%s: %v", r, err)
			continue
		}
		all = append(all, apps...)
	}
	all = g.dedupe(all)
	sort.SliceStable(all, func(i, j int) bool {
		a, b := strings.ToLower(all[i].Name), strings.ToLower(all[j].Name)
		return a < b || a == b && all[i].Folder < all[j].Folder
	})
	if len(all) < *minApps {
		log.Fatalf("only %d apps found (fewer than -min %d): nothing written", len(all), *minApps)
	}
	cat := Catalog{Name: "Community apps", Generated: g.today, Rules: rulesVersion, Apps: all}
	if err := writeJSON(*out, cat); err != nil {
		log.Fatal(err)
	}
	if err := writeJSON(*scanOut, g.scan); err != nil {
		log.Fatal(err)
	}
	if *picks != "" {
		src, err := picksGo(cat)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*picks, src, 0644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("%d apps from %d repositories; %d left out; %d API requests", len(all), len(repos), len(g.scan.Skipped), g.c.calls)
}

// readMap reads a JSON object, leaving out keys starting with "_" (notes).
func readMap[T any](p string, out *map[string]T) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	*out = map[string]T{}
	for k, v := range raw {
		if strings.HasPrefix(k, "_") {
			continue
		}
		var t T
		if err := json.Unmarshal(v, &t); err != nil {
			return fmt.Errorf("%s: %s: %w", p, k, err)
		}
		(*out)[k] = t
	}
	return nil
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0644)
}

// loadPrev reads a previous catalogue and scan, if they were made by the
// same rules.
func (g *gen) loadPrev(catFile, scanFile string) {
	if b, err := os.ReadFile(catFile); err == nil {
		var c Catalog
		if json.Unmarshal(b, &c) == nil && c.Rules == rulesVersion {
			for _, a := range c.Apps {
				g.prev[a.Zip+"#"+a.Folder] = a
			}
		}
	}
	if b, err := os.ReadFile(scanFile); err == nil {
		var s Scan
		if json.Unmarshal(b, &s) == nil && s.Rules == rulesVersion {
			g.noApp = s.NoApp
		}
	}
}

// repoRe is an owner/repo name.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// seedRepos reads the "repo" entries of a picks.go or a catalogue JSON,
// and the folder of each entry with "wrap" set: an app with no folder of
// its own in its zip keeps the one it was installed into before.
func seedRepos(p string) ([]string, map[string]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	var out []string
	wrapped := map[string]string{}
	repoLine := regexp.MustCompile(`(?:Repo:\s*|"repo":\s*)"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)"`)
	folderLine := regexp.MustCompile(`(?:Folder:\s*|"folder":\s*)"([^"]+)"`)
	wrapRe := regexp.MustCompile(`(?:Wrap:\s*|"wrap":\s*)true`)
	// one entry per App{ ... } block or JSON object
	for _, block := range regexp.MustCompile(`App\{|\{\s*"name"`).Split(string(b), -1) {
		m := repoLine.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		out = appendNew(out, m[1])
		if f := folderLine.FindStringSubmatch(block); f != nil && wrapRe.MatchString(block) {
			wrapped[m[1]] = f[1]
		}
	}
	return out, wrapped, nil
}

// searches are the GitHub searches that find candidates: repositories
// tagged for Onion or the Miyoo Mini, and ones describing an app for them.
var searches = []string{
	"topic:onionos", "topic:onion-os", "topic:onionui", "topic:onion-ui",
	"topic:miyoo-mini", "topic:miyoomini", "topic:miyoo-mini-plus", "topic:miyoominiplus", "topic:miyoo",
	"onionos app", "\"onion os\" app", "onion miyoo app", "miyoo mini app", "miyoo mini plus app", "onionos in:readme miyoo",
}

// discover runs the searches and returns the repositories found that
// aren't forks, archived forks or private.
func (g *gen) discover() ([]string, error) {
	var out []string
	var lastErr error
	for _, q := range searches {
		rs, err := g.c.search(q, 3)
		if err != nil {
			lastErr = err
		}
		for _, r := range rs {
			if r.Fork || r.Private {
				continue
			}
			out = appendNew(out, r.FullName)
			g.known[strings.ToLower(r.FullName)] = &r
		}
	}
	return out, lastErr
}

// mergeRepos puts the seeds first, then the rest found, each once
// (GitHub names are case-insensitive).
func mergeRepos(seeds, found []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range append(append([]string{}, seeds...), found...) {
		if k := strings.ToLower(r); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// otherFirmwareRe matches release assets (and releases) built for other
// firmwares or devices, or that aren't builds: they are not looked into.
var otherFirmwareRe = regexp.MustCompile(`(?i)(muos|\.muxapp|spruce|minui|nextui|allium|knulli|trimui|garlic|rg35|rg40|anbernic|crossmix|koriki|batocera|recalbox|flip|tsp|brick|\.pak|source|-src|_src|debug|symbols|windows|win64|macos|darwin|linux-x86|x86_64|amd64|x64|aarch64|arm64|android|\.apk)`)

// pin is a download chosen for a repository: a release asset or a commit.
type pin struct {
	url, sha, version, released, ref, tag string
	size                                  int64
	prerelease, commit                    bool
	asset                                 string
	info                                  zipInfo
	prebuilt                              []App // entries reused from -prev
}

// prevFor returns the previous catalogue's entries for a zip.
func (g *gen) prevFor(url string) []App {
	var out []App
	for k, a := range g.prev {
		if strings.HasPrefix(k, url+"#") {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}

// repoApps finds the apps of one repository.
func (g *gen) repoApps(name string) ([]App, error) {
	if !repoRe.MatchString(name) {
		return nil, errors.New("not an owner/repo name")
	}
	if why, ok := g.excluded(name); ok {
		return nil, errors.New("excluded: " + why)
	}
	info := g.known[strings.ToLower(name)]
	if info == nil {
		var err error
		if info, err = g.c.repo(name); err != nil {
			return nil, err
		}
	}
	if info.FullName != "" && info.FullName != name {
		if why, ok := g.excluded(info.FullName); ok {
			return nil, errors.New("excluded: " + why)
		}
		name = info.FullName // renamed or moved
	}
	pins, err := g.pinRepo(name, info)
	if err != nil {
		return nil, err
	}
	var out []App
	for _, p := range pins {
		if len(p.prebuilt) > 0 {
			out = append(out, p.prebuilt...)
			continue
		}
		readme := g.readme(name, p.ref)
		for _, za := range p.info.Apps {
			a, err := g.entry(name, info, p, za, readme)
			if err != nil {
				log.Printf("%s: %v", name, err)
				g.scan.Skipped[name+"#"+za.Folder] = err.Error()
				continue
			}
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no installable Onion app")
	}
	return out, nil
}

// excluded reports whether exclude.json leaves a repository or app out.
func (g *gen) excluded(key string) (string, bool) {
	for k, why := range g.exclude {
		if strings.EqualFold(k, key) {
			return why, true
		}
	}
	return "", false
}

// pinRepo chooses what to install from a repository: the newest stable
// release whose zip assets hold an Onion app, else the newest pre-release
// that does, else the default branch's newest commit if its files have an
// app folder. From one release, an app folder found in several assets is
// taken from the one named for Onion, else the smallest.
func (g *gen) pinRepo(name string, info *repoInfo) ([]pin, error) {
	rels, err := g.c.releases(name)
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	sort.SliceStable(rels, func(i, j int) bool { return rels[i].Published > rels[j].Published })
	for _, pre := range []bool{false, true} {
		tried := 0
		for _, r := range rels {
			if r.Draft || r.Prerelease != pre || tried == 4 || otherFirmwareRe.MatchString(r.Tag) && !strings.Contains(strings.ToLower(r.Tag), "onion") {
				continue
			}
			var zips []asset
			for _, a := range r.Assets {
				if strings.HasSuffix(strings.ToLower(a.Name), ".zip") && !otherFirmwareRe.MatchString(a.Name) && a.Size <= maxDownload {
					zips = append(zips, a)
				}
			}
			if len(zips) == 0 {
				continue
			}
			tried++
			if pins := g.releasePins(name, r, zips); len(pins) > 0 {
				return pins, nil
			}
		}
	}
	return g.commitPin(name, info)
}

// releasePins inspects a release's zips and keeps one zip per app folder.
func (g *gen) releasePins(name string, r release, zips []asset) []pin {
	sort.SliceStable(zips, func(i, j int) bool {
		oi, oj := strings.Contains(strings.ToLower(zips[i].Name), "onion"), strings.Contains(strings.ToLower(zips[j].Name), "onion")
		if oi != oj {
			return oi
		}
		return zips[i].Size < zips[j].Size
	})
	var pins []pin
	taken := map[string]bool{}
	for _, a := range zips {
		p := pin{url: a.URL, tag: r.Tag, ref: r.Tag, version: versionOf(r.Tag, r.Name), released: day(r.Published), prerelease: r.Prerelease, asset: a.Name}
		if prev := g.prevFor(a.URL); len(prev) > 0 {
			p.prebuilt = prev
			pins = append(pins, p)
			for _, pa := range prev {
				taken[pa.Folder] = true
			}
			continue
		}
		if err := g.inspect(&p, "", strings.TrimPrefix(a.Digest, "sha256:")); err != nil {
			log.Printf("%s %s: %v", name, a.Name, err)
			continue
		}
		var keep []zipApp
		for _, za := range p.info.Apps {
			key := za.Folder
			if za.Wrap {
				key = "wrap:" + za.Config.Label
			}
			if !taken[key] {
				taken[key] = true
				keep = append(keep, za)
			}
		}
		if len(keep) > 0 {
			p.info.Apps = keep
			pins = append(pins, p)
		}
	}
	return pins
}

// commitPin pins the default branch's newest commit, if its tree has an
// app folder: a config.json beside a launch.sh, or in an App folder.
func (g *gen) commitPin(name string, info *repoInfo) ([]pin, error) {
	sha, date, err := g.c.headCommit(name, info.DefaultBranch)
	if err != nil {
		return nil, err
	}
	paths, err := g.c.tree(name, sha)
	if err != nil {
		return nil, err
	}
	// A config.json with a launch entry, read from GitHub's raw files (not
	// the API), before downloading the whole commit.
	found, asked := false, 0
	for _, p := range paths {
		if path.Base(p) != "config.json" || asked == 12 || len(appDirs([]string{p}, func(string) bool { return true })) == 0 {
			continue
		}
		asked++
		if b, err := g.c.raw("https://raw.githubusercontent.com/" + name + "/" + sha + "/" + p); err == nil {
			if _, ok := readConfig(b); ok {
				found = true
				break
			}
		}
	}
	if !found {
		return nil, errors.New("no release with an Onion app zip, and no app folder in its files")
	}
	repoName := path.Base(name)
	p := pin{url: "https://github.com/" + name + "/archive/" + sha + ".zip", ref: sha, version: "commit " + sha[:7], released: day(date), commit: true}
	if prev := g.prevFor(p.url); len(prev) > 0 {
		p.prebuilt = prev
		return []pin{p}, nil
	}
	if err := g.inspect(&p, repoName+"-"+sha+"/", ""); err != nil {
		return nil, err
	}
	if len(p.info.Apps) == 0 {
		return nil, errors.New("no Onion app folder in its newest commit")
	}
	return []pin{p}, nil
}

// inspect downloads the pin's zip (checking GitHub's digest when there is
// one) and finds the apps in it.
func (g *gen) inspect(p *pin, top, digest string) error {
	if why, ok := g.noApp[p.url]; ok {
		g.scan.NoApp[p.url] = why
		return errors.New(why)
	}
	dst := filepath.Join(g.zips, hashName(p.url)+".zip")
	size, sum, err := g.c.download(p.url, dst)
	if err != nil {
		return err
	}
	if !g.keepZips {
		defer os.Remove(dst)
	}
	if digest != "" && !strings.EqualFold(digest, sum) {
		os.Remove(dst)
		return fmt.Errorf("SHA-256 %s doesn't match GitHub's digest %s", sum, digest)
	}
	zr, err := zip.OpenReader(dst)
	if err != nil {
		os.Remove(dst)
		g.scan.NoApp[p.url] = "not a zip"
		return err
	}
	defer zr.Close()
	p.size, p.sha = size, sum
	p.info = inspectZip(&zr.Reader, top)
	if len(p.info.Apps) == 0 {
		g.scan.NoApp[p.url] = "no Onion app folder"
		return errors.New("no Onion app folder")
	}
	return nil
}

// hashName names a download by a hash of its address.
func hashName(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}

// day is the date part of a GitHub time.
func day(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

var versionRe = regexp.MustCompile(`[0-9]+(\.[0-9]+)+([-+][0-9A-Za-z.]+)?`)

// versionOf is a release's version: its tag without a leading "v"; a
// version number inside the tag ("SearchFilter-1.2.3"); or, for a tag with
// none, the one in the release's name, else the tag as it is.
func versionOf(tag, name string) string {
	t := strings.TrimPrefix(tag, "v")
	if versionRe.FindString(t) == t {
		return t
	}
	if v := versionRe.FindString(t); v != "" {
		return v
	}
	if v := versionRe.FindString(name); v != "" {
		return v
	}
	return t
}

// readme is the repository's README at ref, or "".
func (g *gen) readme(name, ref string) string {
	for _, n := range []string{"README.md", "readme.md", "README", "Readme.md", "README.MD"} {
		b, err := g.c.raw("https://raw.githubusercontent.com/" + name + "/" + ref + "/" + n)
		if err == nil {
			return string(b)
		}
	}
	return ""
}

// dedupe keeps one app per folder (case-insensitive): the first found, so
// the repositories already in the catalogue keep theirs.
func (g *gen) dedupe(all []App) []App {
	seen := map[string]string{}
	var out []App
	for _, a := range all {
		k := strings.ToLower(a.Folder)
		if r, ok := seen[k]; ok {
			if r != a.Repo {
				g.scan.Skipped[a.Repo+"#"+a.Folder] = "folder App/" + a.Folder + " is taken by " + r
			}
			continue
		}
		seen[k] = a.Repo
		out = append(out, a)
	}
	return out
}
