package main

// Bring your own game file. Some community apps are ports or recompiled
// games that need data from a copy of a commercial game the user owns (a
// ROM dumped from their own cartridge, say). The store never downloads
// such files from anywhere it ships or suggests. After installing the app
// it looks for the user's copy, in this order:
//
//  1. on the card (Roms/, Media/ and the card's top level, within bounds),
//     taking a file only if its hash matches a known-good dump the
//     catalogue lists;
//  2. at the user's own "game files" location, a folder address on their
//     own server that they typed in themselves (empty unless they did),
//     again only if the hash matches;
//  3. a file the user picks from the card, checked the same way when the
//     catalogue lists hashes.
//
// The file is copied to the place the app reads it from.

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// NeedsFile is a game file an app needs from the user's own copy of a game.
type NeedsFile struct {
	// What: the game and version, as its README names it ("Super Mario 64
	// (USA)").
	What string `json:"what"`
	// Path: where the app reads it, from the card's root; always inside the
	// app's folder ("App/SuperMario64/rom.z64").
	Path string `json:"path"`
	// Ext: the extensions a copy may have (".z64", ".v64"); Path's if empty.
	Ext []string `json:"ext,omitempty"`
	// SHA1: the SHA-1 of known-good dumps. Without any, a copy can't be
	// checked, so the store only offers the picker.
	SHA1 []string `json:"sha1,omitempty"`
	// ByteOrder "n64": the SHA-1s are of the .z64 byte order, and .v64 and
	// .n64 copies are converted before hashing (the copy itself is kept as
	// it is; the app converts it).
	ByteOrder string `json:"byteorder,omitempty"`
	// Names: file names to ask the game files location for; What with each
	// extension, and Path's own name, if empty.
	Names []string `json:"names,omitempty"`
}

var (
	extRe  = regexp.MustCompile(`^\.[a-z0-9]{1,6}$`)
	sha1Re = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// fileNameRe: a plain file name, for the app's folder and for asking
	// the game files location.
	fileNameRe = regexp.MustCompile(`^[A-Za-z0-9 _.,'!&()+-]{1,120}$`)
)

// validNeedsFile checks an app's needed file: a plain name inside the
// app's folder, plain extensions, SHA-1s in hex, and names that are file
// names only.
func validNeedsFile(a App) error {
	nf := a.NeedsFile
	bad := func(what string) error { return fmt.Errorf("bad needed file (%s) in the catalogue", what) }
	if strings.TrimSpace(nf.What) == "" || len(nf.What) > 200 || strings.IndexFunc(nf.What, unicode.IsControl) >= 0 {
		return bad("what")
	}
	dir, name := path.Split(nf.Path)
	if dir != "App/"+a.Folder+"/" || !fileNameRe.MatchString(name) || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return bad("path")
	}
	for _, e := range nf.Ext {
		if !extRe.MatchString(e) {
			return bad("extension")
		}
	}
	for _, h := range nf.SHA1 {
		if !sha1Re.MatchString(h) {
			return bad("sha1")
		}
	}
	if nf.ByteOrder != "" && nf.ByteOrder != "n64" {
		return bad("byte order")
	}
	if len(nf.Names) > 8 {
		return bad("names")
	}
	for _, n := range nf.Names {
		if !fileNameRe.MatchString(n) || strings.HasPrefix(n, ".") || strings.Contains(n, "..") {
			return bad("names")
		}
	}
	return nil
}

// exts are the extensions a copy may have, lower case.
func (nf *NeedsFile) exts() []string {
	if len(nf.Ext) > 0 {
		return nf.Ext
	}
	return []string{strings.ToLower(path.Ext(nf.Path))}
}

// target is where the file goes on the card.
func (nf *NeedsFile) target() string {
	return filepath.Join(sdcard(), filepath.FromSlash(nf.Path))
}

// present reports whether the app's file is in place.
func (nf *NeedsFile) present() bool {
	fi, err := os.Stat(nf.target())
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// accepts reports whether name has one of the file's extensions.
func (nf *NeedsFile) accepts(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range nf.exts() {
		if ext == e {
			return true
		}
	}
	return false
}

// known reports whether sum is one of the listed dumps.
func (nf *NeedsFile) known(sum string) bool {
	for _, h := range nf.SHA1 {
		if strings.EqualFold(h, sum) {
			return true
		}
	}
	return false
}

// candidateNames are the file names asked of the game files location.
func (nf *NeedsFile) candidateNames() []string {
	names := nf.Names
	if len(names) == 0 {
		for _, e := range nf.exts() {
			names = append(names, nf.What+e)
		}
	}
	names = append(names, path.Base(nf.Path))
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] && fileNameRe.MatchString(n) && !strings.HasPrefix(n, ".") && !strings.Contains(n, "..") {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// errNotKnown: a file that isn't one of the listed dumps.
var errNotKnown = errors.New("it doesn't match any of the known-good dumps the catalogue lists, so it wasn't used")

// romSHA1 is the SHA-1 of the file at p. With byteOrder "n64", a .v64
// (byte-swapped) or .n64 (little-endian) dump is hashed as if it were in
// the .z64 byte order, which is what published hashes are of.
func romSHA1(p, byteOrder string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	swap := 0 // 2: swap byte pairs, 4: reverse each 4 bytes
	if byteOrder == "n64" {
		var head [4]byte
		if _, err := io.ReadFull(f, head[:]); err == nil {
			switch {
			case head == [4]byte{0x37, 0x80, 0x40, 0x12}:
				swap = 2
			case head == [4]byte{0x40, 0x12, 0x37, 0x80}:
				swap = 4
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := io.ReadFull(f, buf)
		chunk := buf[:n]
		switch swap {
		case 2:
			for i := 0; i+1 < len(chunk); i += 2 {
				chunk[i], chunk[i+1] = chunk[i+1], chunk[i]
			}
		case 4:
			for i := 0; i+3 < len(chunk); i += 4 {
				chunk[i], chunk[i+1], chunk[i+2], chunk[i+3] = chunk[i+3], chunk[i+2], chunk[i+1], chunk[i]
			}
		}
		h.Write(chunk)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkGameFile reports whether the file at p is a listed dump; with no
// dumps listed, any file is taken.
func checkGameFile(p string, nf *NeedsFile) error {
	if len(nf.SHA1) == 0 {
		return nil
	}
	sum, err := romSHA1(p, nf.ByteOrder)
	if err != nil {
		return err
	}
	if !nf.known(sum) {
		return errNotKnown
	}
	return nil
}

// scanLimits bound the search of the card: how deep it looks under each
// folder, how many entries it reads, how many files it hashes, and the
// sizes of file it considers.
type scanLimits struct {
	Entries, Hashes       int
	MinSize, MaxSize      int64
	RomsDepth, MediaDepth int
}

var defaultScan = scanLimits{Entries: 6000, Hashes: 24, MinSize: 1 << 10, MaxSize: 512 << 20, RomsDepth: 4, MediaDepth: 3}

// scanCard looks for a copy of the file on the card: under Roms/ and Media/
// (down to their depth limits) and at the card's top level. Only files with
// one of the extensions, within the size limits, are hashed, and the first
// whose hash is a listed dump is returned. Without listed dumps nothing can
// be matched, so it returns "".
func scanCard(nf *NeedsFile, lim scanLimits) string {
	if len(nf.SHA1) == 0 {
		return ""
	}
	seen, hashed := 0, 0
	var walk func(dir string, depth int) string
	walk = func(dir string, depth int) string {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		var subs []string
		for _, e := range ents {
			if seen++; seen > lim.Entries || hashed >= lim.Hashes {
				return ""
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(dir, name)
			if e.IsDir() {
				if depth > 0 {
					subs = append(subs, p)
				}
				continue
			}
			if !e.Type().IsRegular() || !nf.accepts(name) {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.Size() < lim.MinSize || fi.Size() > lim.MaxSize {
				continue
			}
			hashed++
			if sum, err := romSHA1(p, nf.ByteOrder); err == nil && nf.known(sum) {
				return p
			}
		}
		for _, sub := range subs {
			if p := walk(sub, depth-1); p != "" {
				return p
			}
		}
		return ""
	}
	root := sdcard()
	for _, r := range []struct {
		dir   string
		depth int
	}{{"Roms", lim.RomsDepth}, {"Media", lim.MediaDepth}, {"", 0}} {
		if p := walk(filepath.Join(root, r.dir), r.depth); p != "" {
			return p
		}
	}
	return ""
}

// copyGameFile copies the user's copy at src to where the app reads it,
// beside its final name first and then renamed into place.
func copyGameFile(src string, nf *NeedsFile, progress func(done, total int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	dst := nf.target()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if free := freeSpace(filepath.Dir(dst)); free >= 0 && free < fi.Size() {
		return fmt.Errorf("not enough space on the card (%d MB free): make room first (MiniTop › Storage)", free>>20)
	}
	tmp := dst + ".store-new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				os.Remove(tmp)
				return err
			}
			done += int64(n)
			progress(done, fi.Size())
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// normalizeGameFiles checks a game files location the user typed: an
// http:// or https:// folder address (https:// is added when there's no
// scheme). Plain http is allowed on any host here, since a file from it is
// used only if its hash is a listed dump.
func normalizeGameFiles(in string) (string, error) {
	u := strings.TrimSpace(in)
	if u == "" {
		return "", nil
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https") || pu.User != nil {
		return "", errors.New("enter an http:// or https:// folder address, without a user name")
	}
	return strings.TrimRight(u, "/") + "/", nil
}

// gameFileLimit is the largest game file the store fetches from the game
// files location.
const gameFileLimit = 1 << 30

// FetchGameFile asks the user's game files location (base, ending in /)
// for the file under each candidate name, and puts the first one whose hash
// is a listed dump in place. It returns the name used. Without listed dumps
// it fetches nothing: a file from a server is used only once checked.
func (f *fetcher) FetchGameFile(base string, nf *NeedsFile, progress func(done, total int64)) (string, error) {
	if len(nf.SHA1) == 0 {
		return "", errors.New("the catalogue lists no known-good dumps to check a download against")
	}
	dst := nf.target()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return "", err
	}
	tmp := dst + ".store-download"
	var last error
	for _, name := range nf.candidateNames() {
		u := base + url.PathEscape(name)
		resp, err := f.http.Head(u)
		if err != nil {
			last = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		if err := f.fetchTo(u, tmp, gameFileLimit, progress); err != nil {
			last = err
			continue
		}
		if err := checkGameFile(tmp, nf); err != nil {
			os.Remove(tmp)
			last = fmt.Errorf("%s: %w", name, err)
			continue
		}
		return name, os.Rename(tmp, dst)
	}
	if last != nil {
		return "", last
	}
	return "", fmt.Errorf("no file named %s there", strings.Join(nf.candidateNames(), " or "))
}
