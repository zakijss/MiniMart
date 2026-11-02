package main

// The update count on the home screen. MiniMart checks for updates in the
// background when it starts, at most once a day, and keeps what it found
// in updates.json; checking from the Updates screen refreshes it too. The
// count shown leaves out anything installed since (an update from an
// app's own page, say), by comparing the kept versions with what is on
// the card now.

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/atomicfile"
	"github.com/zakijss/minideck/ui"
)

// checkEvery is how often the background check runs.
const checkEvery = 20 * time.Hour

type noteItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Folder    string `json:"folder,omitempty"` // an app's
	Port      string `json:"port,omitempty"`   // a port's key
	Available string `json:"available"`
}

// updateNote is what the last check found.
type updateNote struct {
	Checked time.Time  `json:"checked"`
	Items   []noteItem `json:"items"`
}

func notePath() string { return filepath.Join(appdir.Dir(), "updates.json") }

func loadNote() updateNote {
	var n updateNote
	if err := loadJSON(notePath(), &n); err != nil {
		log.Print(err)
	}
	return n
}

// saveNote keeps the result of a check.
func saveNote(ups []update, at time.Time) error {
	n := updateNote{Checked: at}
	for _, u := range ups {
		it := noteItem{Kind: u.Kind, Name: u.Name, Available: u.Available}
		if u.app != nil {
			it.Folder = u.app.Folder
		} else {
			it.Port = u.port.key()
		}
		n.Items = append(n.Items, it)
	}
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(notePath(), b, 0644)
}

// pending is how many of the kept updates are still to install.
func (n updateNote) pending() int {
	recs := loadPortRecords()
	c := 0
	for _, it := range n.Items {
		switch {
		case it.Folder != "":
			if validFolder(it.Folder) != nil {
				continue
			}
			a := App{Folder: it.Folder}
			if a.Installed() && !sameVersion(a.InstalledVersion(), it.Available) {
				c++
			}
		case it.Port != "":
			if r, ok := recs[it.Port]; ok && !sameVersion(r.Version, it.Available) {
				c++
			}
		}
	}
	return c
}

// updatesItem is the home screen's Updates row, with the count.
func updatesItem(n updateNote, now time.Time) ui.Item {
	it := ui.Item{Label: "Updates", Sub: "Check for updates, and update everything at once", Icon: "check", Chevron: true}
	if n.Checked.IsZero() {
		return it
	}
	when := "checked " + ago(n.Checked, now)
	switch c := n.pending(); c {
	case 0:
		it.Sub = "Up to date · " + when
	case 1:
		it.Sub, it.Detail = "1 update · "+when, "1 new"
	default:
		it.Sub, it.Detail = fmt.Sprintf("%d updates · %s", c, when), fmt.Sprintf("%d new", c)
	}
	return it
}

// ago says how long ago t was: "just now", "5 min ago", "3 h ago",
// "2 days ago".
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// checkInBackground checks for updates if the last check is older than
// checkEvery, and redraws the screen when done. A check that reached no
// repository at all (no Wi-Fi) isn't kept, so the next start tries again.
func (s *store) checkInBackground(now time.Time) {
	if n := loadNote(); !n.Checked.IsZero() && now.Sub(n.Checked) < checkEvery && !n.Checked.After(now) {
		return
	}
	src := append([]Source(nil), s.src...)
	go func() {
		ups, errs := s.f.findUpdates(src)
		if len(ups) == 0 && len(errs) > 0 {
			log.Printf("update check: %v", errs[0])
			return
		}
		if err := saveNote(ups, time.Now()); err != nil {
			log.Printf("update check: %v", err)
		}
		s.redraw()
	}()
}
