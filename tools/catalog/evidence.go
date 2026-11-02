package main

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// The facts are derived from what the app's files and README say, by the
// rules below, and each names the file it was found in. None is an
// opinion: a reader can open the file and check.

// srcLine is a line of one of the app's scripts, comments removed.
type srcLine struct {
	file, text string
}

// scriptKind is the language of a text file, by extension or #! line: "sh",
// "py", "lua", or "" for a file that isn't a script.
func scriptKind(name string, b []byte) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".sh":
		return "sh"
	case ".py":
		return "py"
	case ".lua":
		return "lua"
	case "":
		first, _, _ := strings.Cut(string(b), "\n")
		switch {
		case strings.HasPrefix(first, "#!") && strings.Contains(first, "python"):
			return "py"
		case strings.HasPrefix(first, "#!") && strings.Contains(first, "sh"):
			return "sh"
		}
	}
	return ""
}

// scriptLines returns the lines of the app's scripts, comments removed,
// with the file each comes from; files in sorted order so the facts come
// out the same every run.
func scriptLines(files map[string][]byte) []srcLine {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []srcLine
	for _, n := range names {
		kind := scriptKind(n, files[n])
		if kind == "" {
			continue
		}
		vars := map[string]string{}
		for _, l := range strings.Split(string(files[n]), "\n") {
			t := strings.TrimSpace(l)
			if kind == "lua" {
				t, _, _ = strings.Cut(t, "--")
			} else if strings.HasPrefix(t, "#") {
				continue
			} else {
				t = stripComment(t)
			}
			if t == "" {
				continue
			}
			if kind == "sh" {
				t = expandVars(t, vars)
			}
			out = append(out, srcLine{n, t})
		}
	}
	return out
}

// stripComment removes a trailing " # comment" that isn't inside quotes.
func stripComment(line string) string {
	var q rune
	for i, r := range line {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '#' && i > 0 && (line[i-1] == ' ' || line[i-1] == '\t'):
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

var (
	assignRe = regexp.MustCompile(`^(?:export\s+|local\s+)?([A-Za-z_][A-Za-z0-9_]*)=("([^"$` + "`" + `]*)"|'([^']*)'|([^\s;"'$` + "`" + `]*))\s*$`)
	varRe    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

// wellKnown are variables Onion's own scripts set, with the values they
// have on the device.
var wellKnown = map[string]string{
	"SDCARD": "/mnt/SDCARD", "SDCARD_PATH": "/mnt/SDCARD", "sdcard": "/mnt/SDCARD",
	"sysdir": "/mnt/SDCARD/.tmp_update", "miyoodir": "/mnt/SDCARD/miyoo",
}

// expandVars replaces $NAME and ${NAME} with values assigned earlier in the
// same script (plain values only) or set by Onion, and records this line's
// assignment.
func expandVars(line string, vars map[string]string) string {
	line = varRe.ReplaceAllStringFunc(line, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if v, ok := wellKnown[name]; ok {
			return v
		}
		return m
	})
	if m := assignRe.FindStringSubmatch(line); m != nil {
		vars[m[1]] = m[3] + m[4] + m[5]
	}
	return line
}

// sysPathRe finds absolute paths a script may write to (the card's own
// folder names are matched case-insensitively: FAT doesn't care).
var sysPathRe = regexp.MustCompile(`((?i:/mnt/SDCARD)|/appconfigs|/config|/etc|/customer|/misc|/usr|/lib|/sys/devices/system/cpu)(/[^\s"'` + "`" + `;|&)(<>]*)?`)

// writeTargets returns the paths a shell line writes to: the destination
// of cp, mv, rclone and the like, every path of rm, mkdir, touch, sed -i
// and the like, and the target of a > or >> redirection. Relative paths
// are taken from cwd, the folder an earlier "cd" moved to: "" before any
// (the app's folder, where Onion starts launch.sh; only "../" paths are
// read then), "?" after a cd to a folder that isn't known (none are read).
func writeTargets(line, folder, cwd string) []string {
	appDir := "/mnt/SDCARD/App/" + folder
	var out []string
	for _, m := range redirectRe.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	for _, cmd := range splitCommands(line) {
		words := strings.Fields(strings.NewReplacer(`"`, "", `'`, "").Replace(cmd))
		for len(words) > 0 && (words[0] == "sudo" || words[0] == "busybox" || words[0] == "nohup" || words[0] == "eval" || strings.Contains(words[0], "=")) {
			words = words[1:]
		}
		if len(words) < 2 {
			continue
		}
		name := path.Base(words[0])
		args := words[1:]
		var paths []string
		for i, w := range args {
			if i > 0 {
				switch prev := args[i-1]; {
				case prev == "-d" && name == "unzip", prev == "-C" && name == "tar", (prev == "-O" || prev == "-P") && name == "wget", prev == "-o" && name == "curl":
					out = append(out, w)
				}
			}
			if name == "dd" && strings.HasPrefix(w, "of=") {
				out = append(out, strings.TrimPrefix(w, "of="))
			}
			if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "${}=`:") || w == "{}" || w == ";" || w == `\;` {
				continue
			}
			paths = append(paths, w)
		}
		switch name {
		case "cp", "mv", "rsync", "install", "ln", "rclone":
			// the destination is the last argument; a remote one
			// ("cloud:saves/") isn't on the card
			if last := args[len(args)-1]; len(paths) > 1 && paths[len(paths)-1] == last {
				out = append(out, last)
			}
		case "rm", "mkdir", "touch", "chmod", "chown", "tee", "truncate":
			out = append(out, paths...)
		case "sed":
			if strings.Contains(cmd, " -i") && len(paths) > 0 {
				out = append(out, paths[len(paths)-1])
			}
		case "find":
			if (strings.Contains(cmd, "-delete") || strings.Contains(cmd, "-exec rm")) && len(paths) > 0 {
				out = append(out, paths[0])
			}
		}
	}
	var abs []string
	for _, p := range out {
		switch {
		case strings.HasPrefix(p, "/"):
		case cwd == "?" || strings.ContainsAny(p, "$`"):
			continue // relative to a folder that isn't known
		case cwd == "" && (strings.HasPrefix(p, "../") || p == ".."):
			p = path.Join(appDir, p)
		case cwd != "":
			p = path.Join(cwd, p)
		default:
			continue
		}
		if m := sysPathRe.FindString(p); m != "" && strings.HasPrefix(p, m) {
			abs = append(abs, strings.TrimRight(m, "/"))
		}
	}
	return abs
}

var (
	redirectRe = regexp.MustCompile(`[^0-9&>]>>?\s*"?([^\s"';|&>]+)`)
	cdRe       = regexp.MustCompile(`^cd\s+"?([^\s";&|]+)"?\s*$`)
)

// cdTarget is the folder a "cd" line moves to: a literal absolute path,
// the app's folder for cd "$(dirname "$0")", or "?" when unknown.
func cdTarget(line, folder, cwd string) (string, bool) {
	if strings.HasPrefix(line, "cd ") && strings.Contains(line, "dirname") {
		return "/mnt/SDCARD/App/" + folder, true
	}
	m := cdRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	switch t := m[1]; {
	case strings.HasPrefix(t, "/"):
		return path.Clean(t), true
	case strings.ContainsAny(t, "$`") || cwd == "?":
		return "?", true
	case cwd != "":
		return path.Join(cwd, t), true
	}
	return path.Join("/mnt/SDCARD/App", folder, m[1]), true
}

// splitCommands splits a shell line at ;, && and || (not inside quotes,
// roughly).
func splitCommands(line string) []string {
	return regexp.MustCompile(`\s*(?:;|&&|\|\|)\s*`).Split(line, -1)
}

// pyWriteRe finds Python writing to an absolute path.
var pyWriteRe = regexp.MustCompile(`(?:open\(\s*["'](/[^"']+)["']\s*,\s*["'][wax]|(?:shutil\.(?:copy\w*|move|rmtree)|os\.(?:remove|unlink|makedirs|mkdir|rename|replace))\([^)]*?["'](/[^"']+)["']\s*\))`)

// outside summarises where the app writes outside App/<folder>: card paths
// relative to the card ("Roms/PORTS"), other paths in full, each with the
// scripts that write there.
func outside(lines []srcLine, folder string) (paths []string, from map[string][]string) {
	from = map[string][]string{}
	own := strings.ToLower("/mnt/SDCARD/App/" + folder)
	add := func(p, file string) {
		p = strings.TrimRight(p, "/")
		if len(p) >= 11 && strings.EqualFold(p[:11], "/mnt/SDCARD") {
			p = "/mnt/SDCARD" + p[11:]
		}
		if lp := strings.ToLower(p); lp == own || strings.HasPrefix(lp, own+"/") {
			return
		}
		s := summarisePath(p)
		if s == "" {
			return
		}
		if _, ok := from[s]; !ok {
			paths = append(paths, s)
		}
		if !contains(from[s], file) {
			from[s] = append(from[s], file)
		}
	}
	cwd, file := "", ""
	for _, l := range lines {
		if l.file != file {
			cwd, file = "", l.file
		}
		switch scriptKind(l.file, nil) {
		case "py":
			for _, m := range pyWriteRe.FindAllStringSubmatch(l.text, -1) {
				add(m[1]+m[2], l.file)
			}
		case "sh":
			if d, ok := cdTarget(l.text, folder, cwd); ok {
				cwd = d
				continue
			}
			for _, p := range writeTargets(l.text, folder, cwd) {
				add(p, l.file)
			}
		}
	}
	return paths, from
}

// summarisePath shortens a path for a fact: on the card, relative to it
// and at most two parts deep ("Saves/CurrentProfile"); elsewhere, at most
// three parts ("/appconfigs/wpa_supplicant.conf"). CPU
// settings in /sys and temporary paths give "".
func summarisePath(p string) string {
	p = strings.TrimRight(p, ":.,/")
	if strings.HasPrefix(p, "/sys/") {
		return ""
	}
	card := strings.HasPrefix(p, "/mnt/SDCARD")
	rel := strings.TrimPrefix(strings.TrimPrefix(p, "/mnt/SDCARD"), "/")
	if card && rel == "" {
		return "the card's top level"
	}
	if !card {
		rel = strings.TrimPrefix(p, "/")
	}
	segs := strings.Split(rel, "/")
	if n := 3; card && len(segs) > 2 || !card && len(segs) > n {
		if card {
			n = 2
		}
		segs = segs[:n]
	}
	for i, s := range segs {
		if strings.ContainsAny(s, "*?$") {
			segs = segs[:i]
			break
		}
	}
	if len(segs) == 0 {
		return ""
	}
	out := strings.Join(segs, "/")
	if !card {
		out = "/" + out
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// listFrom joins paths with the files they were found in:
// "Roms/PORTS (launch.sh), Saves (sync.sh)".
func listFrom(paths []string, from map[string][]string, max int) string {
	var parts []string
	for i, p := range paths {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(paths)-max))
			break
		}
		parts = append(parts, p+" ("+strings.Join(from[p], ", ")+")")
	}
	return strings.Join(parts, ", ")
}

// cpuWrites reports the scripts that write the CPU's governor or speed.
func cpuWrites(lines []srcLine) []string {
	var files []string
	for _, l := range lines {
		if strings.Contains(l.text, "/sys/devices/system/cpu") && strings.Contains(l.text, ">") && !contains(files, l.file) {
			files = append(files, l.file)
		}
	}
	return files
}

// clockSets reports the scripts that set the system clock.
func clockSets(lines []srcLine) []string {
	var files []string
	re := regexp.MustCompile(`(^|[\s;&|])(date\s+(-s|--set)|hwclock\s+(-w|--systohc))`)
	for _, l := range lines {
		if re.MatchString(l.text) && !contains(files, l.file) {
			files = append(files, l.file)
		}
	}
	return files
}

var (
	ipRe       = regexp.MustCompile(`^[0-9.]+$`)
	urlRe      = regexp.MustCompile(`https?://([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)(/[^\s"'<>()\\]*)?`)
	fetchCmdRe = regexp.MustCompile(`(^|[\s;&|(])(curl|wget|git\s+(?:clone|pull|fetch))\b|requests\.(get|post)|urllib|urlopen|http\.request`)
)

// ignoredHosts appear in programs and scripts without the app contacting
// them: standards, licences, documentation, examples.
var ignoredHosts = map[string]bool{
	"www.w3.org": true, "w3.org": true, "www.gnu.org": true, "gnu.org": true, "golang.org": true, "go.dev": true,
	"pkg.go.dev": true, "curl.se": true, "curl.haxx.se": true, "www.openssl.org": true, "openssl.org": true,
	"www.libsdl.org": true, "libsdl.org": true, "xmlsoft.org": true, "json-schema.org": true, "example.com": true,
	"www.example.com": true, "localhost": true, "www.apache.org": true, "opensource.org": true, "creativecommons.org": true,
	"ns.adobe.com": true, "purl.org": true, "www.zlib.net": true, "zlib.net": true, "www.freetype.org": true,
	"freetype.org": true, "www.libpng.org": true, "libpng.org": true, "bugs.python.org": true, "www.python.org": true,
	"docs.python.org": true, "python.org": true, "sourceware.org": true, "gcc.gnu.org": true, "www.sqlite.org": true,
	"sqlite.org": true, "www.ietf.org": true, "tools.ietf.org": true, "schemas.xmlsoap.org": true, "www.iana.org": true,
	"github.com/golang": true, "www.unicode.org": true, "unicode.org": true, "www.mozilla.org": true, "mozilla.org": true,
	"crl.pki.goog": true, "ocsp.pki.goog": true, "x.org": true, "www.x.org": true, "invisible-island.net": true,
	"www.lua.org": true, "lua.org": true, "www.boost.org": true, "nothings.org": true, "github.com/nothings": true,
	"0.0.0.0": true, "127.0.0.1": true, "www.ffmpeg.org": true, "ffmpeg.org": true, "www.videolan.org": true,
	"tukaani.org": true, "www.bzip.org": true, "sourceforge.net": true, "www.gstatic.com": true, "rust-lang.org": true,
	"www.rust-lang.org": true, "doc.rust-lang.org": true, "crates.io": true, "docs.rs": true,
}

// hostRe finds a URL's host in a program's strings, where strings often
// run together: the host must end in a common top-level domain.
var hostRe = regexp.MustCompile(`(?i)https?://((?:[a-z0-9-]+\.)+(?:com|org|net|io|app|dev|website|online|tv|me|co|uk|de|fr|jp|info|xyz|gg|so|ai|cloud|page|site|live|fm|link|us|ca|eu|ru|cn|be|nl|it|es|se|no|fi|pl|cz|at|ch|au|nz|br|in|kr|tw|sh|to|ly|cc|im|is|news|blog|tech|top|run|space|tools|games|stream|radio|music|video|ws))(?:[/:?#"'\s]|$)`)

// library reports whether a program file is a shared library or a
// common tool an app bundles (ffmpeg, curl...): their strings say little
// about the app itself.
func library(name string) bool {
	b := path.Base(name)
	return strings.Contains(b, ".so") || bundledTools[strings.TrimSuffix(b, path.Ext(b))]
}

var bundledTools = map[string]bool{
	"ffmpeg": true, "ffprobe": true, "ffplay": true, "curl": true, "wget": true, "busybox": true, "sqlite3": true,
	"openssl": true, "rclone": true, "git": true, "ssh": true, "dropbear": true, "zsh": true, "bash": true,
	"dialog": true, "python": true, "python2": true, "python3": true, "jq": true, "mpv": true, "mpg123": true,
	"htop": true, "st": true, "unzip": true, "7z": true, "7za": true, "syncthing": true,
}

// hostsNamed lists the internet hosts the app's scripts fetch from and its
// programs name, with the files they are in. Links to the app's own
// repository, and hosts only named in shared libraries, are left out.
func hostsNamed(lines []srcLine, bins map[string][]string, repo string) (hosts []string, from map[string][]string) {
	from = map[string][]string{}
	own := "github.com/" + strings.ToLower(repo)
	add := func(u, h, file string) {
		h = strings.ToLower(h)
		lu := strings.ToLower(u)
		if ignoredHosts[h] || !strings.Contains(h, ".") || ipRe.MatchString(h) || strings.Count(h, ".") == 1 && strings.HasPrefix(h, "www.") ||
			strings.Contains(lu, own+"/") || strings.HasSuffix(lu, own) {
			return
		}
		if _, ok := from[h]; !ok {
			hosts = append(hosts, h)
		}
		if !contains(from[h], file) {
			from[h] = append(from[h], file)
		}
	}
	for _, l := range lines {
		if !fetchCmdRe.MatchString(l.text) {
			continue
		}
		for _, m := range urlRe.FindAllStringSubmatch(l.text, -1) {
			add(m[0], m[1], l.file)
		}
	}
	var names []string
	for n := range bins {
		if !library(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		for _, s := range bins[n] {
			for _, m := range hostRe.FindAllStringSubmatchIndex(s, -1) {
				u := s[m[0]:]
				if i := strings.IndexAny(u, " \"'<>,"); i > 0 {
					u = u[:i]
				}
				add(u, s[m[2]:m[3]], path.Base(n))
			}
		}
	}
	return hosts, from
}

// netLibs lists programs that load an HTTP or TLS library.
func netLibs(libs map[string][]string) []string {
	var out []string
	for n, ls := range libs {
		for _, l := range ls {
			if strings.HasPrefix(l, "libcurl") || strings.HasPrefix(l, "libssl") {
				out = append(out, path.Base(n)+" ("+l+")")
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// selfUpdates finds how the app fetches newer versions of itself, as
// sentences naming the files: scripts that download from its own GitHub
// repository or run git pull, programs holding the address of its own
// releases, and the words apps show when they update themselves.
func selfUpdates(lines []srcLine, bins map[string][]string, repo string) []string {
	own := strings.ToLower(repo)
	var gitFiles, ownFiles, progs []string
	for _, l := range lines {
		t := strings.ToLower(l.text)
		if !fetchCmdRe.MatchString(l.text) {
			continue
		}
		switch {
		case strings.Contains(t, "git pull") || strings.Contains(t, "git fetch"):
			gitFiles = appendNew(gitFiles, l.file)
		case strings.Contains(t, "github.com/"+own) || strings.Contains(t, "api.github.com/repos/"+own) || strings.Contains(t, "githubusercontent.com/"+own):
			ownFiles = appendNew(ownFiles, l.file)
		}
	}
	var names []string
	for n := range bins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, s := range bins[n] {
			ls := strings.ToLower(s)
			if strings.Contains(ls, "api.github.com/repos/"+own+"/releases") || strings.Contains(ls, "github.com/"+own+"/releases") {
				progs = appendNew(progs, path.Base(n))
				break
			}
		}
	}
	var phrases []string
	for _, l := range lines {
		if m := updatePhraseRe.FindString(l.text); m != "" {
			phrases = appendNew(phrases, fmt.Sprintf("%q in %s", m, l.file))
		}
	}
	for _, n := range names {
		for _, s := range bins[n] {
			if m := updatePhraseRe.FindString(s); m != "" && !library(n) {
				phrases = appendNew(phrases, fmt.Sprintf("%q in its program %s", m, path.Base(n)))
				break
			}
		}
	}
	var out []string
	if len(ownFiles) > 0 {
		out = append(out, "Its scripts download from its own GitHub repository ("+strings.Join(ownFiles, ", ")+").")
	}
	if len(gitFiles) > 0 {
		out = append(out, "Its scripts run git to fetch new code ("+strings.Join(gitFiles, ", ")+").")
	}
	if len(progs) > 0 {
		out = append(out, "Its program "+joinAnd(progs)+" holds the address of its own GitHub releases.")
	}
	if len(phrases) > 0 {
		if len(phrases) > 2 {
			phrases = phrases[:2]
		}
		out = append(out, "Its files have code for updating itself: "+joinAnd(phrases)+".")
	}
	return out
}

// updatePhraseRe finds the words an app shows or runs when it updates
// itself.
var updatePhraseRe = regexp.MustCompile(`(?i)(check(ing)? for updates?|new version (is )?available|download(ing)? (the )?(update|new version)|apply[-_]update|self[-_ ]update|auto[-_ ]?update|you're up to date|install(ing)? (the )?update)`)

func appendNew(ss []string, s string) []string {
	if contains(ss, s) {
		return ss
	}
	return append(ss, s)
}

// runtimeNeeds finds interpreters the app's scripts run, and whether the
// zip brings its own.
func runtimeNeeds(lines []srcLine, names []string) (needs, facts []string) {
	type rt struct {
		label string
		cmd   *regexp.Regexp
		own   *regexp.Regexp
	}
	rts := []rt{
		{"Python", regexp.MustCompile(`(^|[\s;&|/"'])python[23]?(\.[0-9]+)?(\s|$|"|')`), regexp.MustCompile(`(^|/)(python[23]?(\.[0-9]+)?|libpython[0-9.]*\.so[0-9.]*)$`)},
		{"LÖVE", regexp.MustCompile(`(^|[\s;&|/"'])love(\s|$)`), regexp.MustCompile(`(^|/)(love|liblove[^/]*\.so[0-9.]*)$`)},
		{"Java", regexp.MustCompile(`(^|[\s;&|/"'])java\s+-`), regexp.MustCompile(`(^|/)(java)$`)},
	}
	for _, r := range rts {
		var files []string
		for _, l := range lines {
			if r.cmd.MatchString(l.text) && !contains(files, l.file) {
				files = append(files, l.file)
			}
		}
		if len(files) == 0 {
			continue
		}
		if r.label == "Python" && onionPython(lines, files) {
			facts = append(facts, "Runs the Python 2.7 that comes with Onion (.tmp_update/bin/parasyte), from "+strings.Join(files, ", ")+".")
			continue
		}
		bundled := ""
		for _, n := range names {
			if r.own.MatchString(n) {
				bundled = n
				break
			}
		}
		if bundled != "" {
			facts = append(facts, fmt.Sprintf("Runs %s, which it brings in its folder (%s; started from %s).", r.label, bundled, strings.Join(files, ", ")))
		} else {
			needs = append(needs, fmt.Sprintf("%s: %s runs it and the zip doesn't include it", r.label, strings.Join(files, ", ")))
		}
	}
	return needs, facts
}

// onionPython reports whether the scripts that run Python run Onion's own
// (parasyte, in .tmp_update/bin).
func onionPython(lines []srcLine, files []string) bool {
	for _, l := range lines {
		if contains(files, l.file) && strings.Contains(strings.ToLower(l.text), "parasyte") {
			return true
		}
	}
	return false
}

// readOnlyPlaces are Onion's folders of programs and libraries that apps
// run or load: naming them says nothing about what an app changes.
var readOnlyPlaces = []string{"miyoo", ".tmp_update/bin", ".tmp_update/lib", ".tmp_update/script", ".tmp_update/res", ".tmp_update/onionVersion"}

// namedPlaces are the places on the card outside App/<folder> that the
// app's scripts name, other than those in skip and Onion's program
// folders, with the scripts naming them.
func namedPlaces(lines []srcLine, folder string, skip map[string][]string) (paths []string, from map[string][]string) {
	from = map[string][]string{}
	own := strings.ToLower("App/" + folder)
	re := regexp.MustCompile(`(?i)/mnt/SDCARD(/[^\s"'` + "`" + `;|&)(<>$*]*)?`)
	for _, l := range lines {
		for _, m := range re.FindAllString(l.text, -1) {
			p := summarisePath("/mnt/SDCARD" + strings.TrimRight(m[11:], "/."))
			lp := strings.ToLower(p)
			if p == "" || p == "the card's top level" || lp == own || strings.HasPrefix(lp, own+"/") || skip[p] != nil {
				continue
			}
			ro := false
			for _, r := range readOnlyPlaces {
				if p == r || strings.HasPrefix(p, r+"/") {
					ro = true
				}
			}
			if ro {
				continue
			}
			if _, ok := from[p]; !ok {
				paths = append(paths, p)
			}
			from[p] = appendNew(from[p], l.file)
		}
	}
	return paths, from
}

// accountTerms are what a README says when an app asks for a sign-in,
// key, password or PIN. A term counts only after words that ask for it
// ("your", "enter", "set"...), so a README that only mentions one in
// passing doesn't.
var accountTerms = []struct {
	label string
	re    *regexp.Regexp
}{
	{"an API key", regexp.MustCompile(`(?i)\b(your|enter|set|add|put|paste|requires?|needs?|free|own)\b[^.\n]{0,40}\bapi[ _-]?keys?\b`)},
	{"a token", regexp.MustCompile(`(?i)\b(your|enter|set|add|put|paste|requires?|needs?|own)\b[^.\n]{0,40}\b(access|api|auth|client|device|personal access|bearer|github)[ _-]?tokens?\b`)},
	{"a password", regexp.MustCompile(`(?i)\b(your|enter|set|add|put|requires?|needs?|default)\b[^.\n]{0,40}\bpass(word|wd)s?\b`)},
	{"an account or login", regexp.MustCompile(`(?i)\b(your|enter|set|add|requires?|needs?|with)\b[^.\n]{0,40}\b(user ?names?|log ?ins?|sign[ -]?in|accounts?|credentials)\b`)},
	{"a PIN", regexp.MustCompile(`\bPIN\b`)},
}

// accountMentions lists the account terms found in text.
func accountMentions(text string) []string {
	var out []string
	for _, t := range accountTerms {
		if t.re.MatchString(text) {
			out = append(out, t.label)
		}
	}
	return out
}

// settingKeyRe finds settings named for secrets in a settings file.
var settingKeyRe = regexp.MustCompile(`(?i)["']?\b([a-z_]*(?:api_?key|token|password|passwd|secret|username|_user|_pass)[a-z_]*)\b["']?\s*[:=]`)

// secretSettings lists the app's settings files that have fields named for
// a sign-in or key, with those field names.
func secretSettings(files map[string][]byte) []string {
	var names []string
	for n := range files {
		switch strings.ToLower(path.Ext(n)) {
		case ".json", ".cfg", ".conf", ".ini", ".toml", ".yaml", ".yml":
			if n != "config.json" && len(files[n]) <= 64<<10 {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		var keys []string
		for _, m := range settingKeyRe.FindAllStringSubmatch(string(files[n]), -1) {
			if k := strings.ToLower(m[1]); !contains(keys, k) {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			if len(keys) > 4 {
				keys = keys[:4]
			}
			out = append(out, n+" ("+strings.Join(keys, ", ")+")")
		}
	}
	return out
}

// joinAnd joins words as "a, b and c".
func joinAnd(ws []string) string {
	switch len(ws) {
	case 0:
		return ""
	case 1:
		return ws[0]
	}
	return strings.Join(ws[:len(ws)-1], ", ") + " and " + ws[len(ws)-1]
}

// romFileRe finds where a README says to put a game file in the app's
// folder: "App/<Game>/rom.z64", "App/SuperMario64/baserom.us.z64".
var romFileRe = regexp.MustCompile(`App/(?:<[^>]+>|[A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+\.(z64|n64|v64|sfc|smc|nes|gba|gb|gbc|nds|iso|bin|cue|chd|wad|pk3|grp|dat|exe|zip))\b`)

var hexSHA1Re = regexp.MustCompile(`\b[0-9a-fA-F]{40}\b`)

// needsFileFrom reads a README's table of the game files the app's
// folder needs: a row naming App/<folder>/ and a 40-digit SHA-1, as in
// Miyoo64's "Which ROM" table. The file name is the one the README says to
// put in the folder. It returns nil when the README has no such row.
func needsFileFrom(readme, folder string) *NeedsFile {
	file := ""
	for _, m := range romFileRe.FindAllStringSubmatch(readme, -1) {
		file = m[1]
		break
	}
	if file == "" {
		return nil
	}
	var what string
	var sums []string
	for _, line := range strings.Split(readme, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") || !strings.Contains(line, "App/"+folder+"/") {
			continue
		}
		for _, h := range hexSHA1Re.FindAllString(line, -1) {
			if h = strings.ToLower(h); !contains(sums, h) {
				sums = append(sums, h)
			}
		}
		// the game: the longest cell that isn't the folder or a checksum
		for _, c := range strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|") {
			c = strings.TrimSpace(strings.NewReplacer("`", "", "*", "").Replace(c))
			if strings.Contains(c, "App/") || hexSHA1Re.MatchString(c) {
				continue
			}
			if len(c) > len(what) {
				what = c
			}
		}
	}
	if len(sums) == 0 || what == "" {
		return nil
	}
	nf := &NeedsFile{What: what, Path: "App/" + folder + "/" + file, SHA1: sums}
	switch ext := strings.ToLower(path.Ext(file)); ext {
	case ".z64", ".n64", ".v64":
		nf.Ext = []string{".z64", ".v64", ".n64"}
		if strings.Contains(readme, ".v64") || strings.Contains(strings.ToLower(readme), "byte order") {
			nf.ByteOrder = "n64"
		}
	default:
		nf.Ext = []string{ext}
	}
	return nf
}
