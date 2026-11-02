package main

import (
	"fmt"
	"image/color"

	"github.com/zakijss/minideck/ui"
)

// humanSize is a byte count for people: "820 KB", "23.4 MB", "19.3 GB".
func humanSize(n int64) string {
	switch {
	case n < 0:
		return "unknown"
	case n < 1<<20:
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
}

// cardFree is the room left on the card, or -1 if it can't be read.
func cardFree() int64 { return freeSpace(sdcard()) }

// spaceText says how much room an install needs and how much the card has.
// need <= 0 means not known: only the free space is given. short reports
// whether the card has too little.
func spaceText(need int64) (text string, short bool) {
	free := cardFree()
	if free < 0 {
		if need > 0 {
			return "Needs about " + humanSize(need) + ". Couldn't read the card's free space.", false
		}
		return "Couldn't read the card's free space.", false
	}
	if need <= 0 {
		return humanSize(free) + " free on the card", false
	}
	if free < need {
		return fmt.Sprintf("Needs about %s, but only %s is free on the card: make room first (MiniTop › Storage).", humanSize(need), humanSize(free)), true
	}
	return fmt.Sprintf("Needs about %s · %s free on the card", humanSize(need), humanSize(free)), false
}

// spaceRows is spaceText as a labelled row of a text screen.
func spaceRows(need int64, gap, w int) []row {
	t, short := spaceText(need)
	var c color.Color = ui.Text
	if short {
		c = ui.Warn
	}
	return kv("Space", t, c, gap, w)
}

// downloadNeeds is the room a download of n bytes needs: the store refuses
// one the card hasn't three times the room for (fetchPart), since a zip is
// unpacked beside itself.
func downloadNeeds(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return n * 3
}

// drawSpace draws spaceText at x, y on a page drawn line by line, wrapped
// to w, and returns the y below it.
func drawSpace(sc *ui.Screen, x, y, w int, need int64) int {
	t, short := spaceText(need)
	c := ui.Muted
	if short {
		c = ui.Warn
	}
	for _, line := range ui.Wrap(ui.Small, t, w) {
		sc.Text(x, y, line, ui.Small, c)
		y += ui.LineHeight(ui.Small)
	}
	return y
}
