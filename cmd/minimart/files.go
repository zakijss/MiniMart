package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// loadJSON reads the store's own JSON file at path into v. A missing file
// leaves v as it is. A file that doesn't parse is renamed (and reported), so
// the next save starts afresh without destroying what the user had: the
// list of installed games, say, is what lets the store remove them later.
// It goes to path+".unreadable", or, when an earlier copy already has that
// name, to ".unreadable.2", ".3" and so on: an earlier copy is never
// replaced, since it may be the one with the user's data.
func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		if aside, ok := freeName(path + ".unreadable"); ok {
			if rerr := os.Rename(path, aside); rerr == nil {
				return fmt.Errorf("%s: %w (kept as %s)", filepath.Base(path), err, filepath.Base(aside))
			}
		}
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// freeName returns base, or base with ".2", ".3"... added, whichever names
// no file yet. It gives up after a thousand tries, so a card that can't be
// read doesn't keep it looping.
func freeName(base string) (string, bool) {
	for i := 1; i <= 1000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return name, true
		}
	}
	return "", false
}
