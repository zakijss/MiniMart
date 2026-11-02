package main

import (
	"image/color"
	"strings"

	"golang.org/x/image/font"

	"github.com/zakijss/minideck/ui"
)

// row is one line of a scrolling text screen (app details, warnings,
// confirmations).
type row struct {
	text   string
	face   font.Face
	col    color.Color
	gap    int    // space above
	indent int    // from the left edge
	bullet bool   // a bullet before the text
	label  string // a label in the left column (see kv)
}

func (r row) height() int { return r.gap + ui.LineHeight(r.face) }

// wrap is ui.Wrap, with a word too long for a line (a repository's
// address, a path) broken after a '/' or '-', or wherever it must.
func wrap(f font.Face, text string, w int) []string {
	var out []string
	for _, l := range ui.Wrap(f, text, w) {
		for ui.Measure(f, l) > w {
			cut := 0
			for i, r := range l {
				if (r == '/' || r == '-') && ui.Measure(f, l[:i+1]) <= w {
					cut = i + 1
				}
			}
			if cut == 0 {
				for i := range l {
					if i > 0 && ui.Measure(f, l[:i]) > w {
						break
					}
					cut = i
				}
			}
			if cut == 0 {
				break
			}
			out = append(out, l[:cut])
			l = l[cut:]
		}
		out = append(out, l)
	}
	return out
}

// para wraps text into rows of width w; the first gets the gap.
func para(text string, f font.Face, c color.Color, gap, w int) []row {
	var out []row
	for i, l := range wrap(f, text, w) {
		r := row{text: l, face: f, col: c}
		if i == 0 {
			r.gap = gap
		}
		out = append(out, r)
	}
	return out
}

// bullets wraps each item with a bullet and a hanging indent.
func bullets(items []string, f font.Face, c color.Color, gap, w int) []row {
	var out []row
	for i, it := range items {
		for j, l := range wrap(f, it, w-20) {
			r := row{text: l, face: f, col: c, indent: 20, bullet: j == 0}
			if j == 0 && i == 0 {
				r.gap = gap
			} else if j == 0 {
				r.gap = 4
			}
			out = append(out, r)
		}
	}
	return out
}

// labelW is the width of kv's label column.
const labelW = 156

// kv is a fact as a label and a value, the value wrapped beside the label
// so a column of them lines up.
func kv(label, value string, c color.Color, gap, w int) []row {
	out := para(value, ui.Small, c, gap, w-labelW)
	for i := range out {
		out[i].indent = labelW
	}
	if len(out) > 0 {
		out[0].label = strings.ToUpper(label)
	}
	return out
}

// drawRows draws rows from off between top and bottom, and returns the
// largest useful offset (0 when everything fits).
func drawRows(sc *ui.Screen, rs []row, off, x, top, bottom int) int {
	total := 0
	for _, r := range rs {
		total += r.height()
	}
	maxOff := 0
	for rest := total; maxOff < len(rs) && rest > bottom-top; maxOff++ {
		rest -= rs[maxOff].height()
	}
	off = min(max(off, 0), maxOff)
	y := top
	for i := off; i < len(rs); i++ {
		r := rs[i]
		gap := r.gap
		if i == off {
			gap = 0
		}
		h := r.height() - r.gap
		if y+gap+h > bottom {
			break
		}
		y += gap
		if r.label != "" {
			// small capitals, centred on the value's first line
			ly := y + (ui.LineHeight(r.face)-ui.LineHeight(ui.Caps))/4*2
			sc.Text(x, ly, r.label, ui.Caps, ui.Muted)
		}
		if r.bullet {
			sc.Text(x+4, y, "•", r.face, r.col)
		}
		sc.Text(x+r.indent, y, r.text, r.face, r.col)
		y += h
	}
	return maxOff
}

// textScreen shows rows under a header until B (false) or, with yes set,
// A (true). Up/Down scroll when the rows don't fit.
func (s *store) textScreen(title, sub string, rs []row, yes string) bool {
	off := 0
	for {
		sc := s.scr
		sc.Background()
		top := sc.Header(title, sub)
		maxOff := drawRows(sc, rs, off, 28, top+10, ui.H-62)
		var hints []ui.Hint
		if yes != "" {
			hints = append(hints, ui.Hint{Button: "A", Label: yes})
		}
		if maxOff > 0 {
			hints = append(hints, ui.Hint{Button: "UP/DOWN", Label: "Scroll"})
		}
		sc.Footer(append(hints, ui.Hint{Button: "B", Label: "Back"})...)
		sc.Present()
		ev, ok := s.next()
		if !ok {
			return false
		}
		if !ev.Pressed {
			continue
		}
		switch ev.Button {
		case ui.A:
			if yes != "" {
				return true
			}
		case ui.B, ui.Menu:
			return false
		case ui.Up:
			off = max(0, off-1)
		case ui.Down:
			off = min(maxOff, off+1)
		}
	}
}
