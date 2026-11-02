package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// client reads GitHub: the REST API (with a token when there is one, which
// raises the limit from 60 to 5,000 requests an hour) and plain downloads.
// API replies and raw files are kept under cache for ttl, so running the
// tool again within that time asks GitHub nothing new.
type client struct {
	http  *http.Client
	token string
	cache string
	ttl   time.Duration
	calls int // API requests made (not answered from the cache)
}

func newClient(cacheDir, token string, ttl time.Duration) *client {
	return &client{http: &http.Client{Timeout: 10 * time.Minute}, token: token, cache: cacheDir, ttl: ttl}
}

// cachePath is where the reply for key is kept.
func (c *client) cachePath(kind, key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(c.cache, kind, hex.EncodeToString(h[:12]))
}

// fresh returns the cached copy of key if it is younger than ttl.
func (c *client) fresh(kind, key string) ([]byte, bool) {
	p := c.cachePath(kind, key)
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > c.ttl {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil
}

func (c *client) keep(kind, key string, b []byte) {
	p := c.cachePath(kind, key)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err == nil {
		os.WriteFile(p, b, 0644)
	}
}

// statusError is an answer other than 200 OK.
type statusError struct {
	code int
	url  string
}

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d from %s", e.code, e.url) }

func isNotFound(err error) bool {
	var st statusError
	return errors.As(err, &st) && (st.code == http.StatusNotFound || st.code == http.StatusGone || st.code == 451)
}

// api GETs https://api.github.com/<path> into v. A 404 is cached too, as
// an empty reply, so a missing repository costs one request per ttl.
func (c *client) api(path string, v any) error {
	u := "https://api.github.com/" + path
	if b, ok := c.fresh("api", u); ok {
		if len(b) == 0 {
			return statusError{http.StatusNotFound, u}
		}
		return json.Unmarshal(b, v)
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for try := 0; ; try++ {
		c.calls++
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			c.keep("api", u, b)
			return json.Unmarshal(b, v)
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == 451:
			c.keep("api", u, nil)
			return statusError{resp.StatusCode, u}
		case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && try < 3:
			wait := rateWait(resp.Header)
			if wait > 5*time.Minute {
				return fmt.Errorf("GitHub's rate limit is used up until %s: set GITHUB_TOKEN", time.Now().Add(wait).Format(time.Kitchen))
			}
			log.Printf("rate limited; waiting %s", wait)
			time.Sleep(wait)
		default:
			return statusError{resp.StatusCode, u}
		}
	}
}

// rateWait is how long GitHub asks to wait before the next request.
func rateWait(h http.Header) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return time.Duration(s) * time.Second
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if t, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return time.Until(time.Unix(t, 0)) + time.Second
		}
	}
	return 30 * time.Second
}

// raw GETs a file (a README, a LICENSE) from raw.githubusercontent.com.
// Missing files are cached as missing.
func (c *client) raw(u string) ([]byte, error) {
	if b, ok := c.fresh("raw", u); ok {
		if len(b) == 0 {
			return nil, statusError{http.StatusNotFound, u}
		}
		return b[1:], nil
	}
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.keep("raw", u, nil)
		return nil, statusError{resp.StatusCode, u}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode, u}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	c.keep("raw", u, append([]byte{1}, b...)) // a leading byte, so an empty file isn't "missing"
	return b, nil
}

// maxDownload is the largest zip the tool fetches.
const maxDownload = 400 << 20

// download saves u to dst (kept: a pinned release or commit never changes)
// and returns its size and SHA-256.
func (c *client) download(u, dst string) (int64, string, error) {
	if fi, err := os.Stat(dst); err == nil {
		sum, err := fileSHA256(dst)
		return fi.Size(), sum, err
	}
	resp, err := c.http.Get(u)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", statusError{resp.StatusCode, u}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return 0, "", err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = fmt.Errorf("%s is over %d MB", u, maxDownload>>20)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), os.Rename(tmp, dst)
}

func fileSHA256(p string) (string, error) {
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

// repoInfo is what the tool reads about a repository.
type repoInfo struct {
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	DefaultBranch string   `json:"default_branch"`
	Archived      bool     `json:"archived"`
	Fork          bool     `json:"fork"`
	Private       bool     `json:"private"`
	Stars         int      `json:"stargazers_count"`
	Topics        []string `json:"topics"`
	PushedAt      string   `json:"pushed_at"`
	License       *struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
}

// release is a GitHub release with its assets.
type release struct {
	Tag        string  `json:"tag_name"`
	Name       string  `json:"name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Published  string  `json:"published_at"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name    string `json:"name"`
	URL     string `json:"browser_download_url"`
	Size    int64  `json:"size"`
	Digest  string `json:"digest"` // "sha256:...", when GitHub gives one
	Updated string `json:"updated_at"`
}

func (c *client) repo(name string) (*repoInfo, error) {
	var r repoInfo
	if err := c.api("repos/"+name, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *client) releases(name string) ([]release, error) {
	var rs []release
	err := c.api("repos/"+name+"/releases?per_page=30", &rs)
	return rs, err
}

// headCommit is the default branch's newest commit and its date.
func (c *client) headCommit(name, branch string) (sha, date string, err error) {
	var cm struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := c.api("repos/"+name+"/commits/"+branch, &cm); err != nil {
		return "", "", err
	}
	return cm.SHA, cm.Commit.Committer.Date, nil
}

// tree lists the paths in a commit (GitHub cuts very large trees short).
func (c *client) tree(name, sha string) ([]string, error) {
	var t struct {
		Tree []struct {
			Path string `json:"path"`
		} `json:"tree"`
	}
	if err := c.api("repos/"+name+"/git/trees/"+sha+"?recursive=1", &t); err != nil {
		return nil, err
	}
	out := make([]string, len(t.Tree))
	for i, e := range t.Tree {
		out[i] = e.Path
	}
	return out, nil
}

// licenceText is the repository's licence file, for when GitHub can't name
// the licence.
func (c *client) licenceText(name string) (string, error) {
	var l struct {
		DownloadURL string `json:"download_url"`
	}
	if err := c.api("repos/"+name+"/license", &l); err != nil {
		return "", err
	}
	if !strings.HasPrefix(l.DownloadURL, "https://raw.githubusercontent.com/") {
		return "", errors.New("no licence file address")
	}
	b, err := c.raw(l.DownloadURL)
	return string(b), err
}

// search returns the repositories a GitHub search finds, up to pages of
// 100.
func (c *client) search(q string, pages int) ([]repoInfo, error) {
	var out []repoInfo
	for p := 1; p <= pages; p++ {
		var r struct {
			Total int        `json:"total_count"`
			Items []repoInfo `json:"items"`
		}
		path := "search/repositories?per_page=100&page=" + strconv.Itoa(p) + "&q=" + queryEscape(q)
		if err := c.api(path, &r); err != nil {
			return out, err
		}
		out = append(out, r.Items...)
		if len(r.Items) < 100 || len(out) >= r.Total {
			break
		}
		time.Sleep(2 * time.Second) // the search API allows 30 requests a minute
	}
	return out, nil
}

func queryEscape(q string) string {
	return strings.NewReplacer("%", "%25", " ", "+", "\"", "%22", ":", "%3A", "#", "%23", "&", "%26").Replace(q)
}
