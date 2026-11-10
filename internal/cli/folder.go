package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// workDir is the directory ki runs in: App.Dir, else the process's.
func (a *App) workDir() (string, error) {
	if a.Dir != "" {
		return a.Dir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding the current directory: %w", err)
	}
	return wd, nil
}

// folderRemotes are the origin remotes of the git repos directly under dir.
// Hidden directories and `-worktrees` folders (where ki keeps job worktrees)
// are skipped, and a repo without an origin counts for nothing.
func folderRemotes(ctx context.Context, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var remotes []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "-worktrees") {
			continue
		}
		child := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(child, ".git")); err != nil {
			continue
		}
		if remote := git(ctx, child, "remote", "get-url", "origin"); remote != "" {
			remotes = append(remotes, remote)
		}
	}
	return remotes
}

// folderSession resolves a directory that is not inside a git repo. A folder
// of several identities has no session: it comes back as an unmapped one with
// the identities named, for the caller to refuse or to override with --identity.
func folderSession(ctx context.Context, cfg *projects.Config, dir string, extra []string) (ki.Session, []string) {
	s, err := ki.ResolveFolder(cfg, folderRemotes(ctx, dir), extra)
	var mixed ki.MixedIdentitiesError
	if errors.As(err, &mixed) {
		s = ki.Resolve(cfg, "", extra)
		s.Folder = true
		return s, mixed.Identities
	}
	return s, nil
}

// sessionIdentity applies --identity to the session found here. Outside a repo
// it picks the identity; inside a mapped repo it must be the repo's own, unless
// --force. The machine check stays with the caller.
func sessionIdentity(h *here, name string, force bool, extra []string) (ki.Session, error) {
	id, err := lookupIdentity(h.cfg, name)
	if err != nil {
		return ki.Session{}, err
	}
	s := h.session
	if h.root == "" {
		if s.Identity != nil && s.Identity.Name == id.Name {
			return s, nil
		}
		return ki.IdentitySession(h.cfg, *id, extra), nil
	}
	switch {
	case s.Identity != nil && s.Identity.Name == id.Name:
		return s, nil
	case s.Identity != nil && !force:
		return s, fail("this repo belongs to identity %q, not %q; --identity picks one only outside a repo (or pass --force)", s.Identity.Name, id.Name)
	case s.Identity == nil && !force:
		return s, nil // the unmapped-repo refusal explains it
	}
	s = ki.IdentitySession(h.cfg, *id, extra)
	s.Folder = false
	return s, nil
}

// mixedProblem is the refusal for a folder of several identities.
func (h *here) mixedProblem() error {
	return fail("%s", ki.MixedIdentitiesError{Identities: h.mixed})
}

// folderNotice is the line a folder session prints to say who it runs as.
func folderNotice(s ki.Session) string {
	switch {
	case s.Identity == nil:
		return "ki: not in a git repo; no identity, gh's active account"
	case len(s.Projects) == 0:
		return fmt.Sprintf("ki: not in a git repo; identity %s (--identity)", s.Identity.Name)
	}
	return fmt.Sprintf("ki: not in a git repo; identity %s, projects %s", s.Identity.Name, strings.Join(s.Projects, ", "))
}

// runKey names the session's generated files: the repo, or for a folder the
// directory and identity, since the same folder can start as several.
func (h *here) runKey(s ki.Session) string {
	if h.root != "" {
		return h.root
	}
	if s.Identity == nil {
		return h.dir
	}
	return h.dir + "\x00" + s.Identity.Name
}
