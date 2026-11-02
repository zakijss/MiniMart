package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// opinionRe is the store's test for judgements in an app's texts
// (cmd/minimart/picks_test.go): text that matches is left out.
var opinionRe = regexp.MustCompile(`(?i)\b(recommended|use with care|likely|probably|may not work|won't work well|too heavy|trustworthy|safe to)\b`)

// bootNameRe is the store's rule for a boot script name.
var bootNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}\.sh$`)

// secureRe is the store's rule for a sign-in file path.
var secureRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)

// entry makes the catalogue entry for one app found in a pinned zip.
func (g *gen) entry(repo string, info *repoInfo, p pin, za zipApp, readme string) (App, error) {
	folder := za.Folder
	if za.Wrap {
		folder = g.wrapped[repo]
		if folder == "" || len(p.info.Apps) > 1 {
			folder = wrapFolder(za.Files, unescape(za.Config.Label), repo)
		}
	}
	if folder == "" || !folderRe.MatchString(folder) {
		return App{}, fmt.Errorf("no usable folder name for the app in %s", za.Dir)
	}
	if reserved(folder) {
		return App{}, fmt.Errorf("folder App/%s belongs to an Onion or MiniDeck app", folder)
	}
	if why, ok := g.excluded(repo + "#" + folder); ok {
		return App{}, errors.New("excluded: " + why)
	}
	if strings.Contains(za.Strip, "..") || strings.Contains(za.Strip, `\`) || za.Strip != "" && !strings.HasSuffix(za.Strip, "/") {
		return App{}, fmt.Errorf("unusable path %q in the zip", za.Strip)
	}
	a := App{
		Name: appName(za.Config.Label, folder), Folder: folder, Repo: repo,
		Zip: p.url, SHA256: p.sha, Version: p.version, Released: p.released, Size: p.size,
		Strip: za.Strip, Wrap: za.Wrap,
	}
	a.Desc = description(za.Config.Desc, info.Description, readme, a.Name)
	var licFacts []string
	a.License, licFacts = g.licence(repo, info)

	lines := scriptLines(za.Files)
	var facts []string
	if p.commit {
		facts = append(facts, fmt.Sprintf("The project has no release with an Onion app zip; this is commit %s of its default branch, from %s.", strings.TrimPrefix(p.version, "commit "), p.released))
	}
	if p.prerelease {
		facts = append(facts, "GitHub marks this release ("+p.tag+") as a pre-release.")
	}
	if info.Archived {
		facts = append(facts, "The repository is archived on GitHub (read-only).")
	}
	facts = append(facts, licFacts...)
	if n := len(p.info.Apps); n > 1 {
		var names []string
		for _, o := range p.info.Apps {
			names = append(names, appName(o.Config.Label, o.Folder))
		}
		facts = append(facts, fmt.Sprintf("One of %d apps in the same zip: %s.", n, joinAnd(names)))
	}

	// What it changes outside its folder.
	var system []string
	paths, from := outside(lines, folder)
	if len(paths) > 0 {
		system = append(system, "Its scripts write outside its folder: "+listFrom(paths, from, 6)+".")
	}
	boot, bootFacts := bootScripts(p, za, folder)
	if len(boot) > 0 {
		var names []string
		for _, b := range boot {
			names = append(names, ".tmp_update/startup/"+b)
		}
		system = append(system, "Its zip has boot scripts that run at every start-up ("+strings.Join(names, ", ")+"); the store installs them with the app and removes them with it.")
		a.Boot = boot
	}
	facts = append(facts, bootFacts...)
	if extra := extraTop(p.info, za); len(extra) > 0 {
		facts = append(facts, "The zip also has "+joinAnd(extra)+"; the store installs only App/"+folder+".")
	}
	if fs := cpuWrites(lines); len(fs) > 0 {
		facts = append(facts, "Its scripts set the CPU's governor or speed ("+strings.Join(fs, ", ")+").")
	}
	if fs := clockSets(lines); len(fs) > 0 {
		facts = append(facts, "Its scripts set the system clock ("+strings.Join(fs, ", ")+").")
	}
	if named, nfrom := namedPlaces(lines, folder, from); len(named) > 0 {
		facts = append(facts, "Its scripts also name these places outside its folder: "+listFrom(named, nfrom, 6)+".")
	}
	if named := programPaths(za.Bins, folder); len(named) > 0 {
		facts = append(facts, "Its programs name these places outside its folder: "+joinAnd(named)+".")
	}
	a.System = strings.Join(system, " ")

	// How it updates itself.
	a.Updates = strings.Join(selfUpdates(lines, za.Bins, repo), " ")

	// What it needs and contacts.
	hosts, hostFrom := hostsNamed(lines, za.Bins, repo)
	libs := netLibs(za.Libs)
	if len(hosts) > 0 || len(libs) > 0 {
		a.Needs = append(a.Needs, "Wi-Fi")
	}
	if len(hosts) > 0 {
		facts = append(facts, "Its files name these internet hosts: "+listFrom(hosts, hostFrom, 6)+".")
	} else if len(libs) > 0 {
		facts = append(facts, "Its programs load a network library: "+strings.Join(libs, ", ")+".")
	}
	needs, rtFacts := runtimeNeeds(lines, za.Names)
	a.Needs = append(a.Needs, needs...)
	facts = append(facts, rtFacts...)

	// What it asks for.
	var account []string
	if m := accountMentions(readme); len(m) > 0 {
		account = append(account, "Its README mentions "+joinAnd(m)+".")
	}
	if s := secretSettings(za.Files); len(s) > 0 {
		account = append(account, "Its settings files have fields named for a sign-in or key: "+strings.Join(s, ", ")+".")
	}
	h := g.hints[repo+"#"+folder]
	if h.Secure != "" && secureRe.MatchString(h.Secure) {
		a.Secure = h.Secure
		if len(account) == 0 {
			account = append(account, "It keeps a sign-in or key in "+h.Secure+".")
		}
	}
	a.Tailnet = h.Tailnet && a.Secure != ""
	a.Account = strings.Join(account, " ")

	// A game file from the user's own copy.
	if nf := needsFileFrom(readme, folder); nf != nil {
		a.NeedsFile = nf
		a.Needs = append(a.Needs, "your own copy of "+nf.What)
	}
	a.Category = category(a.Name, a.Desc, unescape(za.Config.Label), readme)
	if a.NeedsFile != nil {
		a.Category = "Emulators and ports"
	}
	for _, f := range facts {
		if !opinionRe.MatchString(f) {
			a.Facts = append(a.Facts, f)
		}
	}
	return a, nil
}

// unescape reads a JSON string's contents (a label found by pattern).
func unescape(s string) string {
	var out string
	if json.Unmarshal([]byte(`"`+s+`"`), &out) == nil {
		return out
	}
	return s
}

// appName is the app's label, cleaned, or its folder.
func appName(label, folder string) string {
	n := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, unescape(label))), " ")
	if n == "" || len(n) > 60 {
		return folder
	}
	return n
}

// description is the first of config.json's description, the
// repository's description and the README's first paragraph that is a
// plain statement, cut to about 180 characters.
func description(cfg, repo, readme, name string) string {
	for _, d := range []string{unescape(cfg), repo, firstParagraph(readme)} {
		d = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, d)), " ")
		d = strings.TrimFunc(d, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != ')' })
		if len(d) < 8 || opinionRe.MatchString(d) {
			continue
		}
		if len(d) > 180 {
			cut := strings.LastIndex(d[:180], " ")
			if cut < 100 {
				cut = 180
			}
			d = strings.TrimRight(d[:cut], ",;:") + "…"
		}
		return d
	}
	return name + ", an app for Onion."
}

var (
	mdLinkRe  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	htmlTagRe = regexp.MustCompile(`<[^>]+>`)
)

// firstParagraph is the README's first paragraph of plain text.
func firstParagraph(readme string) string {
	var para []string
	for _, l := range strings.Split(readme, "\n") {
		t := strings.TrimSpace(htmlTagRe.ReplaceAllString(mdLinkRe.ReplaceAllString(l, "$1"), ""))
		switch {
		case t == "" && len(para) > 0:
			return strings.Join(para, " ")
		case t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "```") || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "="):
			if len(para) > 0 {
				return strings.Join(para, " ")
			}
		default:
			para = append(para, strings.NewReplacer("**", "", "__", "", "`", "").Replace(t))
		}
	}
	return strings.Join(para, " ")
}

// licence names the repository's licence by its SPDX id, "None" when it
// has no licence file. When GitHub can't name it (NOASSERTION), the
// licence file's first line is read for a well-known licence, and a fact
// says so.
func (g *gen) licence(repo string, info *repoInfo) (string, []string) {
	if info.License == nil || info.License.SPDX == "" {
		return "None", nil
	}
	if info.License.SPDX != "NOASSERTION" {
		return info.License.SPDX, nil
	}
	text, err := g.c.licenceText(repo)
	if err != nil {
		return "Other", []string{"GitHub can't name its licence (NOASSERTION)."}
	}
	first := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			first = l
			break
		}
	}
	known := []struct{ re, id string }{
		{`(?i)^MIT License|^The MIT License|^MIT$`, "MIT"},
		{`(?i)GNU GENERAL PUBLIC LICENSE`, ""},
		{`(?i)Apache License`, "Apache-2.0"},
		{`(?i)BSD`, ""},
		{`(?i)Mozilla Public License`, "MPL-2.0"},
		{`(?i)This is free and unencumbered software`, "Unlicense"},
	}
	for _, k := range known {
		if k.id != "" && regexp.MustCompile(k.re).MatchString(first) {
			return k.id, []string{fmt.Sprintf("GitHub can't name its licence (NOASSERTION); its licence file starts %q.", shortText(first, 60))}
		}
	}
	return "Other", []string{fmt.Sprintf("GitHub can't name its licence (NOASSERTION); its licence file starts %q.", shortText(first, 60))}
}

func shortText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// bootScripts are the zip's boot scripts (.tmp_update/startup/*.sh at its
// root) that belong to this app: all of them when the zip has one app,
// else those that name its folder. The store can install those; any it
// can't (named outside its rule, or not at the zip's root) are a fact.
func bootScripts(p pin, za zipApp, folder string) (boot, facts []string) {
	for name, b := range p.info.Boot {
		if len(p.info.Apps) > 1 && !strings.Contains(string(b), "App/"+folder) {
			continue
		}
		if !p.commit && bootNameRe.MatchString(name) {
			boot = append(boot, name)
		} else {
			facts = append(facts, "The zip has a boot script .tmp_update/startup/"+name+" that the store doesn't install.")
		}
	}
	sortStrings(boot)
	sortStrings(facts)
	return boot, facts
}

// extraTop are the zip's other top-level folders (besides the app's own
// path and .tmp_update), which the store leaves out.
func extraTop(info zipInfo, za zipApp) []string {
	own, _, _ := strings.Cut(strings.TrimPrefix(za.Dir, info.Root), "/")
	var out []string
	for _, t := range info.Top {
		if t == own || t == ".tmp_update" || t == "__MACOSX" || za.Dir == info.Root || strings.HasPrefix(t, ".") {
			continue
		}
		out = append(out, t+"/")
	}
	return out
}

// cardFolders are the card's top-level folders a program may name.
var cardFolders = map[string]bool{
	"App": true, "BIOS": true, "Emu": true, "Icons": true, "Media": true, "RApp": true, "RetroArch": true,
	"Roms": true, "Saves": true, "Screenshots": true, "Themes": true, ".tmp_update": true, "miyoo": true,
	"Backups": true, "Imgs": true, "Music": true, "Best": true,
}

// programPaths are the card's top-level folders, other than App/<folder>,
// that the app's programs name ("Roms", "Saves"). Libraries are left out.
func programPaths(bins map[string][]string, folder string) []string {
	var out []string
	re := regexp.MustCompile(`/mnt/SDCARD/([A-Za-z0-9_.]+)(/[A-Za-z0-9_.-]*)?`)
	for n, ss := range bins {
		if library(n) {
			continue
		}
		for _, s := range ss {
			for _, m := range re.FindAllStringSubmatch(s, -1) {
				top := m[1]
				if !cardFolders[top] || top == "App" && strings.HasPrefix(m[2], "/"+folder) {
					continue
				}
				out = appendNew(out, top)
			}
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

// picksGo is the store's compiled-in list (cmd/minimart/picks.go) for the
// catalogue.
func picksGo(c Catalog) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`// Code generated by tools/catalog; DO NOT EDIT.

package main

// Community apps: the catalogue tools/catalog made from GitHub on ` + c.Generated + `.
// The store shows the copy published with the release tagged "catalog"
// (see communityCatalog) and falls back to this list when that can't be
// read. Being listed here is not a recommendation: each entry's facts are
// derived from the app's own files and README by rules that name where
// each was found, and the user decides (see facts.go).
//
// Each zip is pinned to a release asset or, for a project with no release
// that holds an Onion app, to one commit's GitHub archive. Its SHA-256 was
// computed from a download and matches GitHub's asset digest where GitHub
// publishes one. tools/catalog/exclude.json lists what is left out and
// why: apps that download ROMs, BIOS or other copyrighted games, apps that
// can't install as one Onion app folder, and ones with security problems.
func init() {
	apps = append(apps,
`)
	for _, a := range c.Apps {
		b.WriteString("App{\n")
		field := func(name, v string) {
			if v != "" {
				fmt.Fprintf(&b, "%s: %q,\n", name, v)
			}
		}
		list := func(name string, vs []string) {
			if len(vs) > 0 {
				fmt.Fprintf(&b, "%s: %#v,\n", name, vs)
			}
		}
		field("Name", a.Name)
		field("Folder", a.Folder)
		field("Desc", a.Desc)
		field("Repo", a.Repo)
		field("License", a.License)
		field("Category", a.Category)
		field("Zip", a.Zip)
		field("SHA256", a.SHA256)
		field("Version", a.Version)
		field("Released", a.Released)
		fmt.Fprintf(&b, "Size: %d,\n", a.Size)
		field("Strip", a.Strip)
		if a.Wrap {
			b.WriteString("Wrap: true,\n")
		}
		field("Secure", a.Secure)
		if a.Tailnet {
			b.WriteString("Tailnet: true,\n")
		}
		field("Account", a.Account)
		field("Updates", a.Updates)
		field("System", a.System)
		list("Boot", a.Boot)
		list("Needs", a.Needs)
		list("Facts", a.Facts)
		if nf := a.NeedsFile; nf != nil {
			fmt.Fprintf(&b, "NeedsFile: &NeedsFile{What: %q, Path: %q, Ext: %#v, SHA1: %#v, ByteOrder: %q},\n", nf.What, nf.Path, nf.Ext, nf.SHA1, nf.ByteOrder)
		}
		b.WriteString("},\n")
	}
	b.WriteString(")\n}\n")
	return format.Source(b.Bytes())
}
