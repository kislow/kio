package standup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maxNotes bounds how many vault notes one report lists.
const maxNotes = 20

// Note is a vault note changed in the window.
type Note struct {
	Path     string    `json:"path"` // relative to the vault
	Modified time.Time `json:"modified"`
}

// recentNotes lists Markdown notes under <vault>/projects/<folder>/ modified
// at or after since, newest first, at most maxNotes of them, and how many
// there were in all. Only the given folders are read.
func recentNotes(vault string, folders []string, since time.Time) ([]Note, int, error) {
	var notes []Note
	for _, f := range folders {
		root := filepath.Join(vault, "projects", f)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".") && path != root {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}
			if info.ModTime().Before(since) {
				return nil
			}
			rel, err := filepath.Rel(vault, path)
			if err != nil {
				return fmt.Errorf("relative path of %s: %w", path, err)
			}
			notes = append(notes, Note{Path: filepath.ToSlash(rel), Modified: info.ModTime()})
			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, fmt.Errorf("listing vault folder %s: %w", f, err)
		}
	}
	slices.SortFunc(notes, func(a, b Note) int { return b.Modified.Compare(a.Modified) })
	total := len(notes)
	if total > maxNotes {
		notes = notes[:maxNotes]
	}
	return notes, total, nil
}

// vaultUsable reports whether dir is an existing directory.
func vaultUsable(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
