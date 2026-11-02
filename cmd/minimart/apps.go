package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/zakijss/minideck/atomicfile"
)

// App is an app the store can install from a pinned zip. Besides what the
// store needs to install it, an entry carries facts a user can check for
// themselves (licence, release, repository, what it does outside its
// folder), shown on the app's page so they can decide whether to trust it.
type App struct {
	Name   string `json:"name"`   // shown in the store
	Folder string `json:"folder"` // App/<Folder> on the card
	Desc   string `json:"desc"`
	Repo   string `json:"repo"` // github.com/<Repo>
	// License is an SPDX name ("MIT", "GPL-3.0"); "None" or empty means the
	// project has no licence file.
	License  string `json:"license"`
	Manifest string `json:"manifest"` // a release manifest.json with the SHA-256, or:
	Zip      string `json:"zip"`      // a zip URL with its SHA-256 and version
	SHA256   string `json:"sha256"`
	Version  string `json:"version"`
	Released string `json:"released"` // the pinned release's date, YYYY-MM-DD
	// Secure: the app keeps a sign-in token or key in the named file, a
	// path relative to its folder ("session.txt", "data/config.json"); the
	// store keeps that file encrypted on the card (see validSecure).
	Secure string `json:"secure"`
	// Tailnet: while MiniScale is on, the app reaches the internet and
	// tailnet servers (MagicDNS names, 100.x) through its proxy. Only for
	// apps that honour https_proxy (libcurl does). Needs Secure's wrapper.
	Tailnet bool `json:"tailnet"`
	// Strip: a prefix the release zip puts before <Folder>/ (most Onion
	// releases ship "App/<Folder>/..."). With it, only <Strip><Folder>/ is
	// unpacked, so stray files in the zip (__MACOSX, docs) are left out.
	Strip string `json:"strip"`
	// Wrap: the release zip has the app's files (config.json, launch.sh...)
	// with no folder of their own: at its root, or with Strip, under that
	// prefix. They are unpacked into App/<Folder>/. This also gives an app
	// whose folder in the zip has another name (a commit archive's, say)
	// the folder its scripts expect.
	Wrap bool   `json:"wrap"`
	Size int64  `json:"size"` // download size in bytes, for the details screen
	Note string `json:"note"`
	// Updates: how the app downloads newer versions of itself, if it does.
	// Those downloads bypass the store's checksum check, so the store says
	// so before installing it.
	Updates string `json:"updates"`
	// Account: the sign-in, API key or password the app asks for, if any.
	Account string `json:"account"`
	// Needs: what it needs to work, one short phrase each ("Wi-Fi", "a
	// Jellyfin server", "a paid OpenAI API key").
	Needs []string `json:"needs"`
	// Facts: other statements a user can check against the repository, one
	// short sentence each. Catalogues written before this field existed
	// have "why" instead, which is read into Facts.
	Facts []string `json:"facts"`
	Why   []string `json:"why"`
	// System: what the app changes outside its folder (Onion's files, a
	// boot hook...). The store says so before installing it, and that
	// removing it here deletes only its folder.
	System string `json:"system"`
	// Boot: boot scripts the zip carries in .tmp_update/startup/ (file
	// names only, e.g. "miniscale.sh"). They are installed with the app,
	// except one the user turned off in MiniTop (renamed to .sh.off), and
	// removed with it. List it in System too, so the store asks first.
	Boot []string `json:"boot"`
	// Category: what kind of app it is (see categories), for sorting and
	// filtering the Apps list. Empty or unknown reads as "Other".
	Category string `json:"category,omitempty"`
	// NeedsFile: a game file the user must supply from their own copy of a
	// commercial game (a port or recompiled game). The store never
	// downloads it; it can copy the user's copy into place (gamefile.go).
	NeedsFile *NeedsFile `json:"needsfile,omitempty"`
	Source    string     `json:"-"` // the repository it came from
	// Own: the app comes from MiniDeck's own release catalogue, so the
	// due-diligence warnings for community apps don't apply to it.
	Own bool `json:"-"`
}

// noLicence reports whether the app's project has no licence file.
func (a App) noLicence() bool {
	l := strings.TrimSpace(a.License)
	return l == "" || strings.EqualFold(l, "none")
}

// bootRe: a plain lower-case script name, safe to join to the startup
// folder.
var bootRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}\.sh$`)

// validBoot checks an app's boot script names (see bootRe).
func validBoot(names []string) error {
	for _, n := range names {
		if !bootRe.MatchString(n) {
			return fmt.Errorf("bad boot script %q in the catalogue", n)
		}
	}
	return nil
}

// folderRe is what an app folder name may look like: letters, digits,
// spaces and - _ . ( ) +, at most 64 characters, not starting with a dot or
// a space and not ending with one. The folder becomes App/<Folder> on the
// card and goes into the root boot hook and wrapper scripts, so anything a
// shell or the FAT file system reads specially is kept out.
var folderRe = regexp.MustCompile(`^[A-Za-z0-9_()+-]([A-Za-z0-9 ._()+-]{0,62}[A-Za-z0-9_()+-])?$`)

// validFolder checks an app's folder name (see folderRe).
func validFolder(f string) error {
	if !folderRe.MatchString(f) {
		return fmt.Errorf("bad app folder %q in the catalogue", f)
	}
	return nil
}

func startupDir() string { return filepath.Join(sdcard(), ".tmp_update", "startup") }

// apps is the built-in "Community apps" catalogue (picks.go).
var apps []App

type manifest struct {
	Version string `json:"version"`
	Assets  struct {
		Zip struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"zip"`
	} `json:"assets"`
}

func (a App) dir() string { return filepath.Join(sdcard(), "App", a.Folder) }

// Installed reports whether the app's folder is on the card.
func (a App) Installed() bool {
	_, err := os.Stat(filepath.Join(a.dir(), "config.json"))
	return err == nil
}

// versionRe is what a version.txt must look like to be believed.
var versionRe = regexp.MustCompile(`^v?[0-9][0-9A-Za-z.+-]{0,30}$`)

// InstalledVersion is what the store installed or, for an app unzipped by
// hand, the version.txt its release carries (MiniDeck's apps do). Empty if
// unknown.
func (a App) InstalledVersion() string {
	if b, err := os.ReadFile(filepath.Join(a.dir(), ".store-version")); err == nil {
		return strings.TrimSpace(string(b))
	}
	b, _ := os.ReadFile(filepath.Join(a.dir(), "version.txt"))
	if v := strings.TrimSpace(string(b)); versionRe.MatchString(v) {
		return strings.TrimPrefix(v, "v")
	}
	return ""
}

// Manifest returns where the app's release zip is and its checksum: from
// the release's manifest.json, or from the catalogue entry itself.
func (f *fetcher) Manifest(a App) (*manifest, error) {
	if a.Manifest == "" { // the catalogue gives the zip and its checksum itself
		m := &manifest{Version: a.Version}
		m.Assets.Zip.URL, m.Assets.Zip.SHA256, m.Assets.Zip.Size = a.Zip, a.SHA256, a.Size
		if a.Zip == "" || len(a.SHA256) != 64 {
			return nil, errors.New("this app has no checksum: not installed")
		}
		return m, nil
	}
	b, err := f.get(a.Manifest)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Assets.Zip.URL == "" || len(m.Assets.Zip.SHA256) != 64 {
		return nil, errors.New("the release has no usable manifest")
	}
	return &m, nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// InstallApp downloads the release zip, checks its SHA-256 against the
// manifest, and unpacks it into App/. Settings and sign-ins already in the
// folder are kept (the zip doesn't contain them).
func (f *fetcher) InstallApp(a App, progress func(done, total int64)) (string, error) {
	m, err := f.Manifest(a)
	if err != nil {
		return "", err
	}
	if err := validFolder(a.Folder); err != nil {
		return "", err
	}
	tmp := filepath.Join(sdcard(), "App", "."+a.Folder+".zip")
	if err := os.MkdirAll(filepath.Dir(tmp), 0755); err != nil {
		return "", err
	}
	if err := f.Download(m.Assets.Zip.URL, tmp, progress); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	sum, err := sha256File(tmp)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, m.Assets.Zip.SHA256) {
		return "", errors.New("the download doesn't match the release's checksum: not installed")
	}
	if a.Secure != "" {
		if err := validSecure(a.Secure); err != nil {
			return "", err
		}
	}
	if err := validBoot(a.Boot); err != nil {
		return "", err
	}
	if err := unpackApp(a, tmp, filepath.Join(sdcard(), "App")); err != nil {
		return "", err
	}
	if err := installBoot(tmp, a.Boot); err != nil {
		return "", err
	}
	if a.Secure != "" {
		if err := secure(a); err != nil {
			return "", err
		}
	}
	if err := atomicfile.Write(filepath.Join(a.dir(), ".store-version"), []byte(m.Version+"\n"), 0644); err != nil {
		return "", err
	}
	return m.Version, nil
}

// unpackApp unpacks the app's folder from its release zip into appRoot
// (App/ on the card), following the entry's strip or wrap, and checks that
// the result is an Onion app: appRoot/<Folder>/config.json.
func unpackApp(a App, zipPath, appRoot string) error {
	var err error
	if a.Strip != "" {
		if err := validStrip(a.Strip); err != nil {
			return err
		}
	}
	switch {
	case a.Wrap:
		err = extractApp(zipPath, appRoot, a.Strip, a.Folder, true)
	case a.Strip == "": // <Folder>/ at the zip's root
		err = extractApp(zipPath, appRoot, "", a.Folder, false)
	default:
		err = unzipApp(zipPath, appRoot, a.Strip, a.Folder)
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(appRoot, a.Folder, "config.json")); err != nil {
		return fmt.Errorf("the zip didn't contain App/%s", a.Folder)
	}
	return nil
}

// installBoot copies the named boot scripts from the zip's
// .tmp_update/startup/ into the card's. One the user turned off in MiniTop
// (name.off) stays off, and is not replaced.
func installBoot(zipPath string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	dir := startupDir()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n+".off")); err == nil {
			continue
		}
		var zf *zip.File
		for _, f := range r.File {
			if f.Name == ".tmp_update/startup/"+n && f.Mode().IsRegular() {
				zf = f
			}
		}
		if zf == nil {
			return fmt.Errorf("the zip has no boot script %s", n)
		}
		in, err := zf.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(in, 1<<20))
		in.Close()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating the startup folder: %w", err)
		}
		if err := atomicfile.Write(filepath.Join(dir, n), b, 0755); err != nil {
			return err
		}
	}
	return nil
}

// unzipApp unpacks only the entries under strip+folder+"/", without strip,
// into dir. It refuses unsafe prefixes and paths, links and devices.
func unzipApp(zipPath, dir, strip, folder string) error {
	if err := validStrip(strip); err != nil {
		return err
	}
	return extractApp(zipPath, dir, strip, folder, false)
}

// validStrip checks a strip prefix: relative, ending in "/", no "..".
func validStrip(strip string) error {
	if !strings.HasSuffix(strip, "/") || strings.HasPrefix(strip, "/") || strings.Contains(strip, "..") || strings.Contains(strip, `\`) {
		return fmt.Errorf("bad strip prefix %q in the catalogue", strip)
	}
	return nil
}

// unzipWrapped unpacks a zip that has the app's files at its root (no
// folder) into dir/folder.
func unzipWrapped(zipPath, dir, folder string) error {
	return extractApp(zipPath, dir, "", folder, true)
}

// junk is macOS clutter that never belongs in an app folder.
func junk(name string) bool {
	if name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") {
		return true
	}
	base := path.Base(strings.TrimSuffix(name, "/"))
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

// extractApp unpacks the app's folder from a release zip into dir/folder.
// Without wrap it takes only the entries under prefix+folder+"/" (prefix
// removed); with wrap it takes every entry under prefix and puts it under
// folder/. Stray
// files, __MACOSX, links and devices are left out. Every path is checked
// before anything is written: one that would land outside dir/folder (zip
// slip) refuses the whole zip.
func extractApp(zipPath, dir, prefix, folder string, wrap bool) error {
	if err := validFolder(folder); err != nil {
		return err
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	root, _ := filepath.Abs(dir)
	base := filepath.Join(root, folder)
	want := prefix + folder + "/"
	type entry struct {
		zf *zip.File
		to string
	}
	var todo []entry
	for _, zf := range r.File {
		name := zf.Name
		if junk(name) {
			continue
		}
		var rel string
		if wrap {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rel = strings.TrimPrefix(name, prefix)
		} else {
			if !strings.HasPrefix(name, want) {
				continue
			}
			rel = strings.TrimPrefix(name, want)
		}
		if strings.Contains(rel, `\`) || strings.HasPrefix(rel, "/") {
			return fmt.Errorf("unsafe path in zip: %s", name)
		}
		p := filepath.Join(base, rel)
		if p != base && !strings.HasPrefix(p, base+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in zip: %s", name)
		}
		todo = append(todo, entry{zf, p})
	}
	n := 0
	for _, e := range todo {
		if e.zf.FileInfo().IsDir() {
			if err := os.MkdirAll(e.to, 0755); err != nil {
				return fmt.Errorf("unpacking %s: %w", e.zf.Name, err)
			}
			continue
		}
		if !e.zf.Mode().IsRegular() {
			continue // no links or devices
		}
		if err := os.MkdirAll(filepath.Dir(e.to), 0755); err != nil {
			return fmt.Errorf("unpacking %s: %w", e.zf.Name, err)
		}
		in, err := e.zf.Open()
		if err != nil {
			return err
		}
		// Each file is written beside its final name and renamed into
		// place (see writeFileAtomic): updating the store itself, or an app
		// whose daemon is running, replaces files that are in use.
		tmp := e.to + ".store-new"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, 256<<20))
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp, e.to)
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		n++
	}
	if n == 0 {
		if wrap && prefix == "" {
			return errors.New("the zip has no files")
		}
		if wrap {
			want = prefix
		}
		return fmt.Errorf("the zip has nothing under %s", want)
	}
	return nil
}

// secureRe: one or more path segments of letters, digits, '.', '_' and
// '-', none starting with a dot. The path goes into the wrapper and boot
// hook scripts, so nothing a shell or sed would read specially.
var secureRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)

// validSecure checks a Secure path: relative to the app's folder
// ("session.txt", "data/config.json"); no "..", no absolute path, no
// segment starting with a dot.
func validSecure(p string) error {
	if len(p) > 200 || !secureRe.MatchString(p) {
		return fmt.Errorf("bad sign-in file %q in the catalogue", p)
	}
	return nil
}

// RemoveApp deletes the app's folder (with its settings and sign-in) and
// its boot hooks.
func RemoveApp(a App) error {
	// The folder is deleted whole, so a name that isn't a plain folder
	// (".." or one with a slash) must never get this far.
	if err := validFolder(a.Folder); err != nil {
		return err
	}
	os.Remove(filepath.Join(startupDir(), hookName(a)))
	if validBoot(a.Boot) == nil {
		for _, n := range a.Boot {
			os.Remove(filepath.Join(startupDir(), n))
			os.Remove(filepath.Join(startupDir(), n+".off"))
		}
	}
	return os.RemoveAll(a.dir())
}

// hookName is the app's boot hook in .tmp_update/startup/: its folder in
// lower case, with anything but letters, digits, - and _ made a dash so the
// script's name is plain. A folder that needed that gets a short hash too,
// so "My App" and "My-App" don't share a hook.
func hookName(a App) string {
	low := strings.ToLower(a.Folder)
	n := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, low)
	if n != low {
		h := sha256.Sum256([]byte(a.Folder))
		n += "-" + hex.EncodeToString(h[:3])
	}
	return n + "-secure.sh"
}

// shellQuote makes s a single word for sh whatever it holds: it goes in
// single quotes, inside which nothing is special, and each ' in it becomes
// '\” (close the quotes, an escaped quote, open them again).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commentText makes s safe on a "# ..." line of a script: control
// characters become spaces (a newline would end the comment and make the
// rest a command) and it is cut to 60 characters.
func commentText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

// launchRe finds config.json's launch entry; the group is the JSON string's
// contents, escapes and all.
var launchRe = regexp.MustCompile(`"launch"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// wrapperName is the store's wrapper script, in the app's folder.
const wrapperName = "secure-launch.sh"

// launchFile, in the app's folder, holds the app's own launch command:
// config.json's "launch" before it was pointed at the wrapper. The wrapper
// runs it, and the boot hook rewrites it when an update brings a new
// config.json.
const launchFile = ".store-launch"

// isWrapper reports whether a launch command already runs the wrapper. The
// boot hook makes the same test in sh (*secure-launch.sh).
func isWrapper(launch string) bool { return strings.HasSuffix(launch, wrapperName) }

// secure installs the wrapper that keeps the app's sign-in file encrypted,
// points the menu entry at it, and adds a boot hook that does both again
// after the app updates itself (its updates replace config.json).
func secure(a App) error {
	if err := validFolder(a.Folder); err != nil {
		return err
	}
	if err := validSecure(a.Secure); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(a.dir(), wrapperName), []byte(wrapperFor(a)), 0755); err != nil {
		return err
	}
	if err := pointAtWrapper(a.dir()); err != nil {
		return err
	}
	hdir := startupDir()
	if err := os.MkdirAll(hdir, 0755); err != nil {
		return fmt.Errorf("creating the startup folder: %w", err)
	}
	return atomicfile.Write(filepath.Join(hdir, hookName(a)), []byte(hookFor(a, "/mnt/SDCARD/App/"+a.Folder)), 0755)
}

// wrapperFor fills in the wrapper script for the app. Values that the shell
// reads are single-quoted; those in comments are made one line.
func wrapperFor(a App) string {
	proxy := ""
	if a.Tailnet {
		proxy = tailnetProxy
	}
	return strings.NewReplacer(
		"@APP@", commentText(a.Name),
		"@FILE@", commentText(a.Secure),
		"@SECRET@", shellQuote(a.Secure),
		"@PROXY@\n", proxy,
	).Replace(wrapperScript)
}

// hookFor fills in the boot hook for the app, whose folder on the device
// is dir.
func hookFor(a App, dir string) string {
	return strings.NewReplacer(
		"@APP@", commentText(a.Name),
		"@DIR@", shellQuote(dir),
		"@SECRET@", shellQuote(a.Secure),
	).Replace(hookScript)
}

// pointAtWrapper keeps the app's own launch command in launchFile and
// points config.json at the wrapper. A config.json that already points at
// the wrapper is left alone, so installing again never wraps the wrapper.
func pointAtWrapper(dir string) error {
	cfg := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	m := launchRe.FindSubmatch(b)
	if m == nil {
		return errors.New("the app's config.json has no launch entry")
	}
	var launch string
	if err := json.Unmarshal([]byte(`"`+string(m[1])+`"`), &launch); err != nil {
		return errors.New("the app's config.json has a launch entry the store can't read")
	}
	if isWrapper(launch) {
		return nil
	}
	if launch == "" || len(launch) > 255 || strings.IndexFunc(launch, unicode.IsControl) >= 0 {
		return errors.New("the app's config.json has a launch entry the store can't use")
	}
	// The launch command is saved before config.json changes: if the power
	// goes off between the two, the menu still starts the app directly.
	if err := atomicfile.Write(filepath.Join(dir, launchFile), []byte(launch+"\n"), 0644); err != nil {
		return err
	}
	out := launchRe.ReplaceAllLiteral(b, []byte(`"launch": "./`+wrapperName+`"`))
	return atomicfile.Write(cfg, out, 0644)
}

// The wrapper: decrypt the sign-in, run the app, encrypt it again and wipe
// the plain copy. The key is the device key in internal flash (as for
// MiniDeck's own apps), so the card alone opens nothing.
const wrapperScript = `#!/bin/sh
# Added by MiniMart: keeps @APP@'s sign-in (@FILE@) encrypted on the card.
# It is only in the clear while @APP@ is open.
#   secure-launch.sh        open the app
#   secure-launch.sh seal   encrypt a sign-in left in the clear
cd "$(dirname "$0")"
SYS=/mnt/SDCARD/.tmp_update
export LD_LIBRARY_PATH="/lib:/config/lib:/mnt/SDCARD/miyoo/lib:$SYS/lib:$SYS/lib/parasyte"
OSSL=$SYS/bin/openssl
KEY=/appconfigs/minideck.key
S=@SECRET@

seal() {
    [ -f "$S" ] || return 0
    if [ ! -s "$KEY" ]; then
        umask 077; "$OSSL" rand -hex 32 > "$KEY.tmp" && mv -f "$KEY.tmp" "$KEY" && sync
    fi
    "$OSSL" enc -aes-256-cbc -pbkdf2 -iter 4096 -salt -pass "file:$KEY" -in "$S" -out "$S.enc.tmp" 2>/dev/null \
        && mv -f "$S.enc.tmp" "$S.enc" && sync || return 1
    # wipe the plain copy (best effort on an SD card), then delete it
    n=$(wc -c < "$S"); dd if=/dev/zero of="$S" bs=1 count="$n" conv=notrunc 2>/dev/null; sync
    rm -f "$S"
}

[ "$1" = seal ] && { seal; exit $?; }
@PROXY@
# A plain copy is newer than the sealed one (you put a new file on the card,
# or the power went off while the app was open): keep it.
if [ ! -f "$S" ] && [ -s "$S.enc" ] && [ -s "$KEY" ]; then
    "$OSSL" enc -d -aes-256-cbc -pbkdf2 -iter 4096 -pass "file:$KEY" -in "$S.enc" -out "$S" 2>/dev/null || rm -f "$S"
fi
# The app's own launch command, from its config.json before MiniMart pointed
# the menu entry here. It is run as a path, never evaluated.
T=
[ -f .store-launch ] && T=$(head -n 1 .store-launch)
case "$T" in ""|*secure-launch.sh) T=./launch.sh ;; esac
case "$T" in /*|./*|../*) ;; *) T=./$T ;; esac
case "$T" in
    *.sh) sh "$T" ;;
    *) "$T" ;;
esac
rc=$?
seal
exit $rc
`

// tailnetProxy goes in the wrapper of apps with Tailnet set. Home network
// addresses stay direct; everything else goes through MiniScale's proxy,
// which reaches tailnet names and the internet alike. The port is read
// from miniscale.conf each time (it can be changed there); the file is only
// read, never sourced, since this runs as root.
const tailnetProxy = `if [ -S /tmp/miniscale/tailscaled.sock ]; then
    PORT=$(sed -n 's/^[[:space:]]*HTTP_PROXY_PORT=["'"'"']\{0,1\}\([0-9][0-9]*\).*/\1/p' /mnt/SDCARD/App/MiniScale/miniscale.conf 2>/dev/null | tail -n 1)
    [ -n "$PORT" ] && [ "$PORT" -ge 1 ] 2>/dev/null && [ "$PORT" -le 65535 ] || PORT=1056
    P=http://127.0.0.1:$PORT
    export http_proxy=$P https_proxy=$P HTTPS_PROXY=$P
    export no_proxy=localhost,127.0.0.1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
    export NO_PROXY=$no_proxy
fi
`

// hookScript runs as root at every boot.
const hookScript = `#!/bin/sh
# Added by MiniMart. At boot: point @APP@'s menu entry at secure-launch.sh again
# (its updates replace config.json), and encrypt a sign-in left in the clear
# (if the power went off while @APP@ was open).
A=@DIR@
S=@SECRET@
[ -f "$A/secure-launch.sh" ] || exit 0
L=$(sed -n 's/.*"launch"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$A/config.json" 2>/dev/null | head -n 1 | sed 's#\\/#/#g')
case "$L" in
    ""|*secure-launch.sh) ;;
    *)
        # an update brought a new config.json: keep its launch command for
        # the wrapper, then point the menu entry at the wrapper
        printf '%s\n' "$L" > "$A/.store-launch.tmp" && mv -f "$A/.store-launch.tmp" "$A/.store-launch" &&
            sed -i 's#"launch"[[:space:]]*:[[:space:]]*"[^"]*"#"launch": "./secure-launch.sh"#' "$A/config.json"
        ;;
esac
[ -f "$A/$S" ] && sh "$A/secure-launch.sh" seal
exit 0
`
