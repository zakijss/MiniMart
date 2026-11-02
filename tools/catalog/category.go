package main

import (
	"regexp"
	"strings"
)

// categories are the store's (cmd/minimart/sort.go), each with the words that
// put an app in it. An app's name counts most, then its description, then
// its config.json label, then its README; the category with the highest
// score wins, the earlier one in this list on a tie, and an app that
// matches none is a Tool.
var categories = []struct {
	name  string
	words *regexp.Regexp
}{
	{"Emulators and ports", regexp.MustCompile(`(?i)\b(emulators?|emulation|emulates?|decomp(ilation)?s?|recomp(iled|ilation)?|ports?\b of|native ports?|n64|pico-?8|j2me|mrp|your own rom|bring your own|baserom|rom\.z64)\b`)},
	{"Games", regexp.MustCompile(`(?i)\b(games?|puzzles?|arcade|dice|card game|roguelike|platformer|shooter|tetris|chess|solitaire|minesweeper|snake|pong|adventure)\b`)},
	{"Media", regexp.MustCompile(`(?i)\b(music|audio|audiobooks?|podcasts?|radio|videos?|movies?|films?|jellyfin|plex|emby|subsonic|navidrome|mp3|flac|player|stream(ing|s)?|tv|sdr|ipod|spotify)\b`)},
	{"Reading", regexp.MustCompile(`(?i)\b(readers?|reading|books?|ebooks?|epub|pdf|comics?|manga|bible|wiki(pedia)?|guides?|walkthroughs?|news|rss|feeds?|dictionary|pokedex|retrodex)\b`)},
	{"Network", regexp.MustCompile(`(?i)\b(wi-?fi|wlan|ssh|vnc|web ?server|http server|file ?browser|ftp|syncthing|sync|mqtt|chat|torrents?|qbittorrent|cloud|rclone|telnet|tailscale|home assistant|remote|meshtastic|mesh|server|client|romm)\b`)},
	{"System", regexp.MustCompile(`(?i)\b(overclock(ing)?|cpu|governor|clock|rtc|time|date|boot ?logo|logo|clean(er|s|ing)?|backups?|favou?rites?|launcher|parental|kids?|play ?time|play activity|battery|search|filter|cheats?|retroachievements|achievements|saves?)\b`)},
	{"Tools", regexp.MustCompile(`(?i)\b(tools?|utility|utilities|manager|editor|htop|terminal|files?|calculator|notes?|tracker|maker|installer|demo|sample|store|install|browse|apps)\b`)},
}

// category scores the app's texts against the categories' words.
func category(name, desc, label, readme string) string {
	if len(readme) > 4000 {
		readme = readme[:4000]
	}
	best, score := "Tools", 0
	for _, c := range categories {
		s := 6*count(c.words, name) + 4*count(c.words, desc) + 2*count(c.words, label) + min(count(c.words, readme), 4)
		if s > score {
			best, score = c.name, s
		}
	}
	return best
}

func count(re *regexp.Regexp, s string) int {
	return len(re.FindAllString(strings.ReplaceAll(s, "_", " "), -1))
}
