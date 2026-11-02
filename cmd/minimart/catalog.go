package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/image/draw"

	"github.com/zakijss/minideck/cache"
)

// sdcard is the card's root, or STORE_SDCARD in tests.
func sdcard() string {
	if d := os.Getenv("STORE_SDCARD"); d != "" {
		return d
	}
	return "/mnt/SDCARD"
}

// Item is a theme or an icon pack from the OnionUI/Themes repository.
type Item struct {
	Base     string // the repository's raw base URL, ending in /
	Name     string // folder name, e.g. "Hippo Stock" or "Pixel by Jeltron"
	Icons    bool   // an icon pack, not a theme
	Remix    bool   // listed under "Remixed"
	Featured bool   // listed under "Featured"
	Pos      int    // its place in the repository's list, from 0
}

// Title is the part of the name before " by " (the whole name if it has no
// author).
func (it Item) Title() string {
	if i := strings.LastIndex(it.Name, " by "); i > 0 {
		return it.Name[:i]
	}
	return it.Name
}

// Author is the part of the name after " by ", or "".
func (it Item) Author() string {
	if i := strings.LastIndex(it.Name, " by "); i > 0 {
		return it.Name[i+4:]
	}
	return ""
}

func esc(s string) string { return url.PathEscape(s) }

// ZipURL is where the item's release zip is.
func (it Item) ZipURL() string {
	if it.Icons {
		return it.Base + "release/icons/" + esc(it.Name) + ".zip"
	}
	return it.Base + "release/" + esc(it.Name) + ".zip"
}

// PreviewURL is where the item's preview picture is.
func (it Item) PreviewURL() string {
	if it.Icons {
		return it.Base + "icons/" + esc(it.Name) + "/preview.png"
	}
	return it.Base + "themes/" + esc(it.Name) + "/preview.png"
}

// Installed reports whether the theme is on the card (unpacked, or as a
// zip that Theme Switcher unpacks when applied), or the icon pack is.
func (it Item) Installed() bool {
	var paths []string
	if it.Icons {
		paths = []string{filepath.Join(sdcard(), "Icons", it.Name)}
	} else {
		paths = []string{filepath.Join(sdcard(), "Themes", it.Name), filepath.Join(sdcard(), "Themes", it.Name+".zip")}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Active reports whether this is the theme or icon pack in use. Onion
// keeps its folder's path ("/mnt/SDCARD/Themes/Pixel by Jeltron/"); the
// last element must be the name exactly, so "Pixel" isn't taken for
// "Pixel by Jeltron".
func (it Item) Active() bool {
	return activeName(it.Icons) == it.Name
}

// activeName is the folder name of the theme (or icon pack) in use, or "".
func activeName(icons bool) string {
	f := "active_theme"
	if icons {
		f = "active_icon_pack"
	}
	b, err := os.ReadFile(filepath.Join(sdcard(), ".tmp_update/config", f))
	if err != nil {
		return ""
	}
	p := strings.TrimRight(strings.TrimSpace(string(b)), "/")
	if p == "" {
		return ""
	}
	return path.Base(p)
}

// fetcher gets files over HTTPS, keeping text lists and previews on the
// card so the store opens at once; lists refresh in the background.
type fetcher struct {
	http  *http.Client
	cache *cache.Disk
}

// statusError is a server's answer other than 200 OK.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// get fetches u whole (up to 16 MB).
func (f *fetcher) get(u string) ([]byte, error) {
	resp, err := f.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// cached returns the saved copy if there is one (fetching a fresh one in
// the background for next time), or fetches and saves it.
func (f *fetcher) cached(u string, refresh bool) ([]byte, error) {
	if b, ok := f.cache.Get(u); ok {
		if refresh {
			go func() {
				if nb, err := f.get(u); err == nil {
					f.cache.Put(u, nb)
				}
			}()
		}
		return b, nil
	}
	b, err := f.get(u)
	if err == nil {
		f.cache.Put(u, b)
	}
	return b, err
}

// lines returns the non-empty lines of a list, without # comments.
func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// validName reports whether a theme or icon pack name from a repository's
// list can be used as a file name: the name becomes Themes/<name>.zip or
// Icons/<name>/, and Remove deletes that path, so it must not climb out of
// the folder or hide in it.
func validName(n string) bool {
	if n == "" || len(n) > 200 || strings.HasPrefix(n, ".") || strings.ContainsAny(n, "/\\:*?\"<>|") {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Themes lists the themes of every enabled theme repository: featured
// first, then the rest. A name already listed by an earlier repository is
// skipped.
func (f *fetcher) Themes(ss []Source) ([]Item, error) {
	var out []Item
	seen := map[string]bool{}
	var lastErr error
	for _, s := range ss {
		if !s.Enabled || s.Type != "themes" {
			continue
		}
		for _, list := range []struct {
			file            string
			featured, remix bool
		}{{"featured", true, false}, {"custom", false, false}, {"remixed", false, true}} {
			b, err := f.cached(s.URL+".github/data/"+list.file+".txt", true)
			if err != nil {
				if list.file == "custom" {
					lastErr = fmt.Errorf("%s: %w", s.Name, err)
				}
				continue // a fork may not have every list
			}
			for i, n := range lines(b) {
				if !seen[n] && validName(n) {
					seen[n] = true
					out = append(out, Item{Base: s.URL, Name: n, Featured: list.featured, Remix: list.remix, Pos: i})
				}
			}
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// IconPacks lists the standalone icon packs that have a download, from
// every enabled theme repository.
func (f *fetcher) IconPacks(ss []Source) ([]Item, error) {
	var out []Item
	for _, s := range ss {
		if !s.Enabled || s.Type != "themes" {
			continue
		}
		packs, err := f.iconPacks(s.URL)
		if err != nil {
			continue
		}
		out = append(out, packs...)
	}
	return out, nil
}

// existsTTL is how long a "no download" answer for an icon pack is kept
// before the server is asked again: a pack's zip may appear later. A "yes"
// is kept until the cache drops it.
const existsTTL = 24 * time.Hour

// headWorkers is how many icon packs are checked at once.
const headWorkers = 4

func (f *fetcher) iconPacks(base string) ([]Item, error) {
	b, err := f.cached(base+".github/data/icons.txt", true)
	if err != nil {
		return nil, err
	}
	var items []Item
	for i, n := range lines(b) {
		if validName(n) {
			items = append(items, Item{Base: base, Name: n, Icons: true, Pos: i})
		}
	}
	ok := make([]bool, len(items))
	var ask []int // the packs with no answer cached
	for i, it := range items {
		switch known, exists := f.knownExists(it.ZipURL()); {
		case !known:
			ask = append(ask, i)
		case exists:
			ok[i] = true
		}
	}
	// Each check is a HEAD with the client's own timeout; a few at once
	// keeps the first load short without crowding the Wi-Fi.
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(headWorkers, len(ask)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ok[i] = f.checkExists(items[i].ZipURL())
			}
		}()
	}
	for _, i := range ask {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var out []Item
	for i, it := range items {
		if ok[i] {
			out = append(out, it)
		}
	}
	return out, nil
}

// knownExists returns the cached answer to whether u can be downloaded:
// "1" for yes, "0 <unix time>" for a no given then, which counts only for
// existsTTL. An entry without a time is a no from before it was kept.
func (f *fetcher) knownExists(u string) (known, exists bool) {
	v, ok := f.cache.Get("exists|" + u)
	if !ok {
		return false, false
	}
	s := string(v)
	if s == "1" {
		return true, true
	}
	var at int64
	if _, err := fmt.Sscanf(s, "0 %d", &at); err != nil || time.Since(time.Unix(at, 0)) > existsTTL {
		return false, false
	}
	return true, false
}

// checkExists asks the server whether u can be downloaded and caches the
// answer. A failed request isn't cached: the next load asks again.
func (f *fetcher) checkExists(u string) bool {
	resp, err := f.http.Head(u)
	if err != nil {
		return false
	}
	resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK
	v := fmt.Sprintf("0 %d", time.Now().Unix())
	if ok {
		v = "1"
	}
	f.cache.Put("exists|"+u, []byte(v))
	return ok
}

// Preview returns the item's preview image.
func (f *fetcher) Preview(it Item) (image.Image, error) {
	b, err := f.cached(it.PreviewURL(), false)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// Thumb scales img to w x h, cropping to fill.
func Thumb(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	src := b
	if b.Dx()*h > b.Dy()*w { // wider: crop the sides
		cw := b.Dy() * w / h
		src = image.Rect(b.Min.X+(b.Dx()-cw)/2, b.Min.Y, b.Min.X+(b.Dx()+cw)/2, b.Max.Y)
	} else {
		ch := b.Dx() * h / w
		src = image.Rect(b.Min.X, b.Min.Y+(b.Dy()-ch)/2, b.Max.X, b.Min.Y+(b.Dy()+ch)/2)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), img, src, draw.Src, nil)
	return out
}

// Size asks the server how big the download is: 0 if it doesn't say.
func (f *fetcher) Size(it Item) int64 { return f.headSize(it.ZipURL()) }

// rawRe splits a GitHub raw base URL into owner, repository and branch.
var rawRe = regexp.MustCompile(`^https://raw\.githubusercontent\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/([A-Za-z0-9_./-]+?)/$`)

// listingURL is the GitHub API address that lists a theme repository's
// release folder (or release/icons), with each zip's size; "" for a
// repository that isn't on GitHub.
func listingURL(base string, icons bool) string {
	m := rawRe.FindStringSubmatch(base)
	if m == nil {
		return ""
	}
	dir := "release"
	if icons {
		dir = "release/icons"
	}
	return "https://api.github.com/repos/" + m[1] + "/" + m[2] + "/contents/" + dir + "?ref=" + url.QueryEscape(m[3])
}

// ItemSizes returns the download size of the items' zips, by ZipURL, from
// one GitHub listing per repository (kept in the cache). Items whose size
// isn't known are left out.
func (f *fetcher) ItemSizes(list []Item) map[string]int64 {
	out := map[string]int64{}
	done := map[string]bool{}
	for _, it := range list {
		u := listingURL(it.Base, it.Icons)
		if u == "" || done[u] {
			continue
		}
		done[u] = true
		b, err := f.cached(u, true)
		if err != nil {
			continue
		}
		for name, n := range parseListing(b) {
			out[(Item{Base: it.Base, Name: name, Icons: it.Icons}).ZipURL()] = n
		}
	}
	return out
}

// parseListing reads a GitHub contents listing: each .zip file's name,
// without .zip, and size.
func parseListing(b []byte) map[string]int64 {
	var files []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	out := map[string]int64{}
	if json.Unmarshal(b, &files) != nil {
		return out
	}
	for _, f := range files {
		if f.Type == "file" && strings.HasSuffix(f.Name, ".zip") && f.Size > 0 {
			out[strings.TrimSuffix(f.Name, ".zip")] = f.Size
		}
	}
	return out
}

// GameSizes returns the download size of each game, by URL: from its
// catalogue, or asked of the server (a few at once) and kept in the cache.
func (f *fetcher) GameSizes(list []Game) map[string]int64 {
	out := map[string]int64{}
	var ask []string
	for _, g := range list {
		switch {
		case g.Size > 0:
			out[g.URL] = g.Size
		default:
			if b, ok := f.cache.Get("size|" + g.URL); ok {
				var n int64
				if _, err := fmt.Sscanf(string(b), "%d", &n); err == nil && n > 0 {
					out[g.URL] = n
					continue
				}
			}
			ask = append(ask, g.URL)
		}
	}
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(headWorkers, len(ask)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range jobs {
				n := f.headSize(u)
				if n <= 0 {
					continue
				}
				f.cache.Put("size|"+u, []byte(fmt.Sprint(n)))
				mu.Lock()
				out[u] = n
				mu.Unlock()
			}
		}()
	}
	for _, u := range ask {
		jobs <- u
	}
	close(jobs)
	wg.Wait()
	return out
}

// headSize asks the server how big u is: 0 if it doesn't say.
func (f *fetcher) headSize(u string) int64 {
	resp, err := f.http.Head(u)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 {
		return 0
	}
	return resp.ContentLength
}

// freeSpace is the room left on dir's file system in bytes, or -1.
func freeSpace(dir string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// stallTimeout is how long a download may go without receiving anything
// before it is given up.
var stallTimeout = 45 * time.Second

var errStalled = errors.New("the download stopped arriving: check the Wi-Fi and try again")

// stallBody cancels its request when no data has arrived for d.
type stallBody struct {
	io.ReadCloser
	t       *time.Timer
	d       time.Duration
	cancel  context.CancelFunc
	stalled atomic.Bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.t.Reset(b.d)
	}
	if err != nil && err != io.EOF && b.stalled.Load() {
		err = errStalled
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.t.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

// getLong starts a download. The usual client gives up on the whole
// request after under a minute, which cuts off a big theme or game on slow
// Wi-Fi; this one has no overall limit and gives up only when the data
// stops arriving (and on the transport's own connect and header timeouts).
func (f *fetcher) getLong(u string) (*http.Response, error) {
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	c := *f.http
	c.Timeout = 0
	// no headers within the stall time either: give up
	t := time.AfterFunc(stallTimeout, cancel)
	resp, err := c.Do(req)
	if err != nil {
		t.Stop()
		cancel()
		return nil, err
	}
	b := &stallBody{ReadCloser: resp.Body, d: stallTimeout, cancel: cancel}
	t.Stop()
	b.t = time.AfterFunc(stallTimeout, func() { b.stalled.Store(true); cancel() })
	resp.Body = b
	return resp, nil
}

// Download saves the zip to dst, calling progress with bytes done and
// total. It writes to dst.part and renames it once the whole file has
// arrived and opens as a zip.
func (f *fetcher) Download(u, dst string, progress func(done, total int64)) error {
	part := dst + ".part"
	if err := f.fetchPart(u, part, 0, progress); err != nil {
		return err
	}
	zr, err := zip.OpenReader(part)
	if err != nil {
		os.Remove(part)
		return errors.New("the download isn't a valid zip")
	}
	zr.Close()
	return os.Rename(part, dst)
}

// fetchTo saves u to dst as Download does, for files that aren't zips.
// Downloads larger than limit bytes are refused.
func (f *fetcher) fetchTo(u, dst string, limit int64, progress func(done, total int64)) error {
	part := dst + ".part"
	if err := f.fetchPart(u, part, limit, progress); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

// fetchPart downloads u into part, which is removed again on any error. It
// refuses a download the card has no room for (three times its size: an
// icon pack is unpacked beside its zip), and one over limit bytes when
// limit > 0.
func (f *fetcher) fetchPart(u, part string, limit int64, progress func(done, total int64)) error {
	resp, err := f.getLong(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the download isn't available (HTTP %d)", resp.StatusCode)
	}
	if limit > 0 && resp.ContentLength > limit {
		return fmt.Errorf("the download is too big (%d MB)", resp.ContentLength>>20)
	}
	if free := freeSpace(filepath.Dir(part)); free >= 0 && resp.ContentLength > 0 && free < resp.ContentLength*3 {
		return fmt.Errorf("not enough space on the card (%d MB free)", free>>20)
	}
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		out.Close()
		os.Remove(part)
		return err
	}
	var done int64
	buf := make([]byte, 64<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if done += int64(n); limit > 0 && done > limit {
				return fail(errors.New("the download is too big"))
			}
			if _, err := out.Write(buf[:n]); err != nil {
				return fail(err)
			}
			if time.Since(last) > 100*time.Millisecond {
				progress(done, resp.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if resp.ContentLength > 0 && done != resp.ContentLength {
		os.Remove(part)
		return errors.New("the download was cut short: try again")
	}
	progress(done, resp.ContentLength)
	return nil
}

// Unzip extracts a zip into dir, refusing paths that would leave it.
func Unzip(zipPath, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	root, _ := filepath.Abs(dir)
	for _, zf := range r.File {
		p := filepath.Join(root, zf.Name)
		if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in zip: %s", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			os.MkdirAll(p, 0755)
			continue
		}
		if !zf.Mode().IsRegular() {
			continue // no links or devices
		}
		os.MkdirAll(filepath.Dir(p), 0755)
		in, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(p)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, 256<<20))
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Install puts a theme zip in Themes/ (Theme Switcher unpacks it when it's
// applied, as with Onion's own themes) or unpacks an icon pack into Icons/.
func (f *fetcher) Install(it Item, progress func(done, total int64)) error {
	if !it.Icons {
		return f.Download(it.ZipURL(), filepath.Join(sdcard(), "Themes", it.Name+".zip"), progress)
	}
	tmp := filepath.Join(sdcard(), "Icons", "."+it.Name+".zip")
	if err := f.Download(it.ZipURL(), tmp, progress); err != nil {
		return err
	}
	defer os.Remove(tmp)
	return Unzip(tmp, filepath.Join(sdcard(), "Icons"))
}

// Remove deletes an installed theme (zip, folder and preview) or icon pack.
func Remove(it Item) error {
	if it.Active() {
		return errors.New("it's the one in use: switch to another first")
	}
	if it.Icons {
		return os.RemoveAll(filepath.Join(sdcard(), "Icons", it.Name))
	}
	t := filepath.Join(sdcard(), "Themes")
	os.Remove(filepath.Join(t, it.Name+".zip"))
	os.RemoveAll(filepath.Join(t, ".previews", it.Name))
	return os.RemoveAll(filepath.Join(t, it.Name))
}
