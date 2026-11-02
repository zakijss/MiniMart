package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
	"github.com/zakijss/minideck/ui"
)

// Community apps are made by other people, and the store doesn't review
// them. What it does: it installs only a download that matches the checksum
// pinned in the app's catalogue, and it shows facts about each app that the
// user can check against the app's repository (licence, release, size, how
// it updates itself, what it changes outside its folder, what it asks for),
// with no judgement of its own. The user decides whether to trust the app.
// MiniDeck's own apps (App.Own) are built from this project's source, so
// they skip the warnings.

// warningVersion is the version of the due-diligence text. Raise it when
// the text changes in substance, so everyone sees the new text once.
const warningVersion = "1"

// warningFile, in the store's folder, holds the warning version the user
// acknowledged.
func warningFile() string { return filepath.Join(appdir.Dir(), "apps-warning-seen") }

func warningSeen() bool {
	b, err := os.ReadFile(warningFile())
	return err == nil && strings.TrimSpace(string(b)) == warningVersion
}

func rememberWarning() error {
	return atomicfile.Write(warningFile(), []byte(warningVersion+"\n"), 0644)
}

// The due-diligence text, shown the first time Apps opens (and on Y).
const warningLead = "Community apps are made by other people, not by MiniDeck."

var warningPoints = []string{
	"MiniDeck doesn't review or vouch for them. It only checks that each download matches its pinned checksum: that you get the file that was listed, not that the file is safe.",
	"An app has full access to your Miyoo. It can read, change or delete anything on the SD card, and use your network.",
	"Each app's page shows its repository, and facts you can check there.",
}

// diligence ends every community app's install confirmation.
const diligence = "MiniDeck doesn't review community apps. Read its repository and decide whether you trust it before installing."

// reminder is the line under the Apps list, in small capitals.
const reminder = "NOT REVIEWED BY MIYOONET: READ EACH APP'S REPOSITORY FIRST"

// warning shows the due-diligence text. With ack set it asks the user to
// acknowledge it (A) and remembers that; it reports whether they did.
func (s *store) warning(ack bool) bool {
	w := ui.W - 56
	rs := para(warningLead, ui.Body, ui.Text, 0, w)
	rs = append(rs, bullets(warningPoints, ui.Small, ui.Text, 10, w)...)
	rs = append(rs, para("Read an app's repository and decide whether you trust it before you install it.", ui.Body, ui.Warn, 14, w)...)
	yes := ""
	if ack {
		yes = "I understand"
	}
	if !s.textScreen("Before you install apps", "Community apps", rs, yes) {
		return false
	}
	if err := rememberWarning(); err != nil {
		log.Printf("saving the warning: %v", err)
	}
	return true
}

// sortApps puts MiniDeck's own apps first, then the rest by name.
func sortApps(list []App) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Own != list[j].Own {
			return list[i].Own
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
}

// releaseDate is a YYYY-MM-DD date as "9 Oct 2026", or "" if it isn't one.
func releaseDate(d string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(d))
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// versionLabel puts a "v" before a version number ("0.1.0" -> "v0.1.0"),
// but not before a pinned commit ("commit a1b2c3d").
func versionLabel(v string) string {
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

func sizeText(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// listLine is the app's second line in the Apps list: who made it and the
// facts that most need a look, then its description.
func listLine(a App) string {
	var parts []string
	if a.Own {
		parts = append(parts, "MiniDeck's own app")
	} else {
		if a.Source != "" && a.Source != "Community apps" {
			parts = append(parts, a.Source)
		}
		if a.noLicence() {
			parts = append(parts, "No licence")
		} else {
			parts = append(parts, a.License)
		}
		if t, err := time.Parse("2006-01-02", a.Released); err == nil {
			parts = append(parts, t.Format("Jan 2006"))
		}
		if a.Updates != "" {
			parts = append(parts, "Updates itself")
		}
		if a.System != "" {
			parts = append(parts, "Writes outside its folder")
		}
		if a.NeedsFile != nil {
			parts = append(parts, "Needs your own game file")
		}
	}
	return strings.Join(append(parts, a.Desc), " · ")
}

// factRows are the app's facts for its page, as label and value rows of
// width w. ver and size are the release's, from its manifest when it has
// one; verNote stands in for the version while that is being read.
func factRows(a App, ver, verNote string, size int64, w int) []row {
	var rs []row
	add := func(label, value string, warn bool) {
		c := ui.Text
		if warn {
			c = ui.Warn
		}
		gap := 6
		if len(rs) == 0 {
			gap = 12
		}
		rs = append(rs, kv(label, value, c, gap, w)...)
	}
	repo := "Not given"
	if a.Repo != "" {
		repo = "github.com/" + a.Repo
	}
	add("Repository", repo, false)
	if a.noLicence() {
		add("Licence", noLicenceText, true)
	} else {
		add("Licence", a.License, false)
	}
	rel := verNote
	if ver != "" {
		rel = versionLabel(ver)
		if d := releaseDate(a.Released); d != "" && ver == a.Version {
			rel += ", released " + d
		}
	}
	add("Release", rel, false)
	if size > 0 {
		add("Download", sizeText(size), false)
	}
	t, short := spaceText(downloadNeeds(size))
	add("Space", t, short)
	none := "None stated"
	if a.Updates != "" {
		add("Updates itself", updatesText(a), true)
	} else {
		add("Updates itself", none, false)
	}
	if a.System != "" {
		add("Outside its folder", systemText(a), true)
	} else {
		add("Outside its folder", none, false)
	}
	sign := none
	if a.Account != "" {
		sign = a.Account
		if a.Secure != "" {
			sign += " The store keeps " + a.Secure + " encrypted on the card."
		}
	}
	add("Sign-in", sign, false)
	if len(a.Needs) > 0 {
		add("Needs", strings.Join(a.Needs, "; "), false)
	}
	if a.NeedsFile != nil {
		add("Game file", needsFileText(a.NeedsFile), false)
	}
	if len(a.Facts) > 0 {
		rs = append(rs, bullets(a.Facts, ui.Small, ui.Text, 10, w)...)
	}
	return rs
}

// needsFileText says which game file the app needs and where it comes
// from.
func needsFileText(nf *NeedsFile) string {
	return "Needs your own copy of " + nf.What + ". The store never downloads it: after installing, it looks for your copy on the card."
}

// The facts a user should weigh before installing a community app, in the
// warning colour where they apply: how it updates itself beyond the store's
// checks, what it changes outside its folder, and a missing licence.

const noLicenceText = "None: there's no licence file, so its author hasn't said how it may be used or shared."

func updatesText(a App) string { return a.Updates + " The store doesn't check those updates." }

func systemText(a App) string {
	return a.System + " Removing it here deletes only App/" + a.Folder + "."
}

// needsConfirm: every community app asks before it is installed or
// updated; MiniDeck's own apps ask only when they change files outside
// their folder.
func needsConfirm(a App) bool { return !a.Own || a.System != "" }

// confirmInstall shows what to check before installing (or updating) the
// app, and asks.
func (s *store) confirmInstall(a App, update bool) bool {
	w := ui.W - 56
	verb := "Install"
	if update {
		verb = "Update"
	}
	var rs []row
	sub := "From " + a.Source
	if a.Own {
		sub = "MiniDeck's own app"
		rs = append(rs, para("It changes files outside its folder:", ui.Body, ui.Text, 0, w)...)
		rs = append(rs, para(a.System, ui.Small, ui.Warn, 8, w)...)
		rs = append(rs, para("Removing it here deletes only App/"+a.Folder+": it may not undo these changes.", ui.Small, ui.Muted, 10, w)...)
		return s.textScreen(verb+" "+a.Name+"?", sub, rs, verb)
	}
	none := "None stated"
	repo := "Not given: the catalogue doesn't say where its source is."
	if a.Repo != "" {
		repo = "github.com/" + a.Repo
	}
	rs = append(rs, kv("Repository", repo, ui.Text, 0, w)...)
	rs = append(rs, spaceRows(downloadNeeds(a.Size), 6, w)...)
	if a.noLicence() {
		rs = append(rs, kv("Licence", noLicenceText, ui.Warn, 6, w)...)
	} else {
		rs = append(rs, kv("Licence", a.License, ui.Text, 6, w)...)
	}
	if a.Updates != "" {
		rs = append(rs, kv("Updates itself", updatesText(a), ui.Warn, 6, w)...)
	} else {
		rs = append(rs, kv("Updates itself", none, ui.Text, 6, w)...)
	}
	if a.System != "" {
		rs = append(rs, kv("Outside its folder", systemText(a), ui.Warn, 6, w)...)
	} else {
		rs = append(rs, kv("Outside its folder", none, ui.Text, 6, w)...)
	}
	if a.NeedsFile != nil {
		rs = append(rs, kv("Game file", needsFileText(a.NeedsFile), ui.Text, 6, w)...)
	}
	rs = append(rs, para(diligence, ui.Body, ui.Text, 16, w)...)
	return s.textScreen(verb+" "+a.Name+"?", sub, rs, verb)
}
