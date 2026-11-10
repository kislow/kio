package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) claudeDir() string {
	return orDefault(a.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(a.home(), ".claude"))
}

// skillDestinations are the folders each agent reads skills from: Claude Code
// its own, Codex and Cursor the shared one.
func (a *App) skillDestinations() []string {
	return []string{filepath.Join(a.claudeDir(), "skills"), filepath.Join(a.home(), ".agents", "skills")}
}

// versionDir is the version as a folder name.
func (a *App) versionDir() string {
	v := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".+-_", r) {
			return r
		}
		return '_'
	}, a.Version)
	if strings.Trim(v, ".") == "" {
		return "dev"
	}
	return v
}

// skillsRoot is the folder holding the skills as plain files: the checkout's
// skills/, else the embedded ones written under ~/.config/ki/skills/<version>,
// so an upgrade links a new folder and never edits the one agents read.
func (a *App) skillsRoot() (dir string, embedded bool, err error) {
	harness, err := a.harness()
	if err != nil {
		return "", false, err
	}
	if harness != "" {
		return filepath.Join(harness, "skills"), false, nil
	}
	dir = filepath.Join(a.confDir(), "skills", a.versionDir())
	data := a.dataFS("")
	err = fs.WalkDir(data, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(data, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		return writeFile(filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, "skills/"))), string(body), 0o644)
	})
	if err != nil {
		return "", true, fmt.Errorf("writing the embedded skills to %s: %w", a.short(dir), err)
	}
	return dir, true, nil
}

// setupSkills links the /ki-* skills into the folders agents read: to the
// checkout when there is one, else to a ki-owned copy of the embedded ones.
// A destination that exists and is not a link is never touched.
func (a *App) setupSkills() error {
	root, embedded, err := a.skillsRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("reading %s: %w", root, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	from := "the checkout's skills"
	if embedded {
		from = "the copy embedded in ki " + a.Version
	}
	for _, dir := range a.skillDestinations() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
		linked := 0
		for _, name := range names {
			dest := filepath.Join(dir, name)
			if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink == 0 {
				fmt.Fprintf(a.Stdout, "skip %s: exists and is not a link\n", a.short(dest))
				continue
			}
			_ = os.Remove(dest) // an older link; absent is fine
			if err := os.Symlink(filepath.Join(root, name), dest); err != nil {
				return fmt.Errorf("linking %s: %w", dest, err)
			}
			linked++
		}
		fmt.Fprintf(a.Stdout, "linked %d skills into %s (%s)\n", linked, a.short(dir), from)
	}
	a.removeRetiredLinks()
	return nil
}

// removeRetiredLinks drops the links an older `make skills` left for a skill
// that was renamed (ki-standup, now ki-recap) and a subagent that was retired
// (ki-resolver); anything that is not a link stays.
func (a *App) removeRetiredLinks() {
	retired := make([]string, 0, 3)
	for _, dir := range a.skillDestinations() {
		retired = append(retired, filepath.Join(dir, "ki-standup"))
	}
	retired = append(retired, filepath.Join(a.claudeDir(), "agents", "ki-resolver.md"))
	for _, path := range retired {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 && os.Remove(path) == nil {
			fmt.Fprintf(a.Stdout, "removed the retired link %s\n", a.short(path))
		}
	}
}
