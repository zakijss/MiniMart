package main

import (
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// An app's page ends with its repository's README for the pinned version,
// as plain text: headings, paragraphs, lists and code kept; images, badges,
// HTML and link addresses dropped.

// readmeNames are the file names tried, in order.
var readmeNames = []string{"README.md", "readme.md", "README"}

// readmeLimit is how much README text an app's page shows, in characters.
const readmeLimit = 6000

// readmeRef is the git ref the app's pinned zip was made from: the tag of
// a release download, the commit of an archive, or HEAD (the default
// branch) when neither shows in the address.
func readmeRef(a App) string {
	pre := "https://github.com/" + a.Repo + "/"
	switch {
	case strings.HasPrefix(a.Zip, pre+"releases/download/"):
		rest := strings.TrimPrefix(a.Zip, pre+"releases/download/")
		if i := strings.Index(rest, "/"); i > 0 {
			return rest[:i]
		}
	case strings.HasPrefix(a.Zip, pre+"archive/"):
		return strings.TrimSuffix(strings.TrimPrefix(a.Zip, pre+"archive/"), ".zip")
	}
	return "HEAD"
}

// repoRe is a GitHub owner/repo.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// errNoReadme: the repository has no README under any of readmeNames.
var errNoReadme = errors.New("no README")

// Readme returns the app's README as text blocks. A README of a release or
// commit never changes, so it is kept in the cache for good, as is the
// answer that there is none; one read from HEAD is refreshed in the
// background.
func (f *fetcher) Readme(a App) ([]mdBlock, error) {
	if !repoRe.MatchString(a.Repo) {
		return nil, errNoReadme
	}
	ref := readmeRef(a)
	pinned := ref != "HEAD"
	none := "readme-none|" + a.Repo + "@" + ref
	if _, ok := f.cache.Get(none); ok && pinned {
		return nil, errNoReadme
	}
	for _, n := range readmeNames {
		u := "https://raw.githubusercontent.com/" + a.Repo + "/" + ref + "/" + n
		b, err := f.cached(u, !pinned)
		if err == nil {
			return markdownText(string(b), readmeLimit), nil
		}
		var st statusError
		if !errors.As(err, &st) || st.code != http.StatusNotFound {
			return nil, err // offline, or the server failed: try again next time
		}
	}
	if pinned {
		f.cache.Put(none, []byte("1"))
	}
	return nil, errNoReadme
}

// mdKind is the kind of a block of README text.
type mdKind int

const (
	mdPara mdKind = iota
	mdHeading
	mdItem
	mdCode
)

// mdBlock is one heading, paragraph, list item or code line.
type mdBlock struct {
	Kind mdKind
	Text string
}

var (
	mdImage    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdRefLink  = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	mdRefDef   = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*\S+`)
	mdAutoLink = regexp.MustCompile(`<(https?://[^>\s]+)>`)
	mdTag      = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9-]*(\s[^<>]*)?/?>`)
	mdComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	mdHeadRe   = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdHTMLHead = regexp.MustCompile(`(?i)^\s*<h[1-6][^>]*>(.*)</h[1-6]>\s*$`)
	mdBullet   = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	mdNumbered = regexp.MustCompile(`^\s*(\d{1,3})[.)]\s+(.*)$`)
	mdRule     = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	mdSetext   = regexp.MustCompile(`^\s{0,3}(=+|-+)\s*$`)
	mdTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	mdAlert    = regexp.MustCompile(`^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*`)
	mdEmph     = strings.NewReplacer("**", "", "__", "", "~~", "", "`", "")
)

// inline turns a line of Markdown into plain text: images and badges
// dropped, links kept as their text, tags and emphasis marks removed,
// entities decoded.
func inline(s string) string {
	s = mdImage.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdRefLink.ReplaceAllString(s, "$1")
	s = mdAutoLink.ReplaceAllString(s, "$1")
	s = mdTag.ReplaceAllString(s, "")
	s = mdEmph.Replace(s)
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// markdownText converts a README to text blocks, at most limit characters
// in all; a longer one ends with a line saying the rest is in the
// repository.
func markdownText(md string, limit int) []mdBlock {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = mdComment.ReplaceAllString(md, "")
	var out []mdBlock
	var para []string
	total := 0
	full := false
	add := func(k mdKind, text string) {
		if full || text == "" {
			return
		}
		if n := utf8.RuneCountInString(text); total+n > limit {
			full = true
			return
		} else {
			total += n
		}
		out = append(out, mdBlock{k, text})
	}
	flush := func() {
		if len(para) > 0 {
			add(mdPara, inline(strings.Join(para, " ")))
			para = nil
		}
	}
	lines := strings.Split(md, "\n")
	fence := ""
	for i := 0; i < len(lines); i++ {
		line := strings.ReplaceAll(lines[i], "\t", "    ")
		trim := strings.TrimSpace(line)
		if fence != "" { // inside a code block: kept as it is
			if strings.HasPrefix(trim, fence) {
				fence = ""
				continue
			}
			if trim != "" {
				add(mdCode, strings.TrimRight(line, " "))
			}
			continue
		}
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			flush()
			fence = trim[:3]
			continue
		}
		for strings.HasPrefix(trim, ">") { // quotes read as plain text
			trim = strings.TrimSpace(strings.TrimPrefix(trim, ">"))
		}
		if m := mdAlert.FindStringSubmatch(trim); m != nil {
			trim = upperFirst(strings.ToLower(m[1])) + ": " + strings.TrimSpace(trim[len(m[0]):])
			if strings.HasSuffix(trim, ": ") {
				flush()
				para = append(para, strings.TrimSpace(trim))
				continue
			}
		}
		switch {
		case trim == "":
			flush()
		case mdRefDef.MatchString(line):
			flush()
		case mdHeadRe.MatchString(line):
			flush()
			add(mdHeading, inline(mdHeadRe.FindStringSubmatch(line)[2]))
		case mdHTMLHead.MatchString(trim):
			flush()
			add(mdHeading, inline(mdHTMLHead.FindStringSubmatch(trim)[1]))
		case len(para) == 1 && mdSetext.MatchString(trim): // the line above underlined
			add(mdHeading, inline(para[0]))
			para = nil
		case mdRule.MatchString(trim):
			flush()
		case mdTableSep.MatchString(trim) && strings.Contains(trim, "-"):
			// a table's header line: its rows follow as plain lines
		case strings.HasPrefix(trim, "|"):
			flush()
			var cells []string
			for _, c := range strings.Split(strings.Trim(trim, "|"), "|") {
				if c = inline(c); c != "" {
					cells = append(cells, c)
				}
			}
			add(mdPara, strings.Join(cells, " · "))
		case mdBullet.MatchString(trim):
			flush()
			add(mdItem, inline(mdBullet.FindStringSubmatch(trim)[1]))
		case mdNumbered.MatchString(trim):
			flush()
			m := mdNumbered.FindStringSubmatch(trim)
			add(mdPara, m[1]+". "+inline(m[2]))
		default:
			para = append(para, trim)
		}
	}
	flush()
	if full {
		out = append(out, mdBlock{mdPara, "… The rest is in the repository's README."})
	}
	return out
}
