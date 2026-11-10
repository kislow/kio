package ki

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/kislow/kio/pkg/projects"
)

// MeccaConfigFile is the per-machine file, under ki's config dir, that says
// where checkouts live. projects.yaml has no local paths: it is shared with the
// cluster and every machine lays out its folders differently.
const MeccaConfigFile = "mecca.yaml"

// DefaultCheckoutRoot is the identity-folder parent from docs/laptop-setup.md.
const DefaultCheckoutRoot = "~/code"

// ErrNoCheckout means a project name did not resolve to exactly one checkout.
var ErrNoCheckout = errors.New("no checkout found")

// Checkouts maps project names to local checkouts.
type Checkouts struct {
	// Roots are searched one and two levels deep, so ~/code finds
	// ~/code/acme/app.
	Roots []string
	// Paths name a checkout directly and win over Roots.
	Paths map[string]string
}

type rawCheckouts struct {
	Roots     []string          `yaml:"roots"`
	Checkouts map[string]string `yaml:"checkouts"`
}

// ParseCheckouts reads mecca.yaml, expanding a leading ~ to home.
func ParseCheckouts(data []byte, home string) (*Checkouts, error) {
	var raw rawCheckouts
	if err := yaml.UnmarshalWithOptions(data, &raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", MeccaConfigFile, err)
	}
	c := &Checkouts{Paths: map[string]string{}}
	for _, r := range raw.Roots {
		c.Roots = append(c.Roots, ExpandHome(r, home))
	}
	for name, p := range raw.Checkouts {
		c.Paths[name] = ExpandHome(p, home)
	}
	if len(raw.Roots) == 0 {
		c.Roots = []string{ExpandHome(DefaultCheckoutRoot, home)}
	}
	return c, nil
}

// LoadCheckouts reads mecca.yaml; a missing file means the default root only.
func LoadCheckouts(file, home string) (*Checkouts, error) {
	data, err := os.ReadFile(file) // #nosec G304 -- the user's own config file
	if errors.Is(err, os.ErrNotExist) {
		return ParseCheckouts(nil, home)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return ParseCheckouts(data, home)
}

// ExpandHome replaces a leading ~ with home.
func ExpandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// Find returns the checkout for a project name. repo is the project's
// normalised GitHub URL from projects.yaml, or "" for a name it does not list;
// with a repo, a candidate counts only if originOf(dir) is that repo, so a
// same-named folder of another owner is never picked.
func (c *Checkouts) Find(name, repo string, originOf func(dir string) string) (string, error) {
	if p, ok := c.Paths[name]; ok {
		if !isDir(p) {
			return "", fmt.Errorf("%w: %s maps %s to %s, which is not a directory", ErrNoCheckout, MeccaConfigFile, name, p)
		}
		return p, nil
	}
	names := []string{name}
	if repo != "" && path.Base(repo) != name {
		names = append(names, path.Base(repo))
	}
	var hits []string
	for _, dir := range c.candidates(names) {
		if repo != "" {
			got, err := projects.NormalizeRepo(originOf(dir))
			if err != nil || got != repo {
				continue
			}
		}
		if !slices.Contains(hits, dir) {
			hits = append(hits, dir)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%w for %q under %s; pass a path, or map it under checkouts: in %s",
			ErrNoCheckout, name, strings.Join(c.Roots, ", "), MeccaConfigFile)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("%w: %q matches %s; map it under checkouts: in %s",
		ErrNoCheckout, name, strings.Join(hits, ", "), MeccaConfigFile)
}

// candidates are git checkouts named like one of names, directly under a root
// or one folder below it, symlinks resolved so one checkout counts once.
func (c *Checkouts) candidates(names []string) []string {
	var out []string
	for _, root := range c.Roots {
		for _, n := range names {
			matches, _ := filepath.Glob(filepath.Join(root, "*", n))
			for _, dir := range append([]string{filepath.Join(root, n)}, matches...) {
				if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
					continue
				}
				if real, err := filepath.EvalSymlinks(dir); err == nil {
					out = append(out, real)
				}
			}
		}
	}
	return out
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
