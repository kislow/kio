package standup

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/kislow/kio/pkg/projects"
)

// ErrIdentity is returned when no identity can be used for a standup.
var ErrIdentity = errors.New("no recap identity")

// Board is a GitHub Project named on one of the identity's projects.
type Board struct {
	Owner string
	Title string
}

// Scope is what one identity's standup may read.
type Scope struct {
	Identity projects.Identity
	Boards   []Board
	// VaultFolders are folder names under the vault's projects/ that belong
	// to this identity and to no other.
	VaultFolders []string
	// foreign holds repo owners that projects.yaml gives only to other identities.
	foreign map[string]bool
}

// SelectIdentity picks the identity named by flag, else the current repo's.
// Identities marked vault: false are refused: their work never meets the
// vault or the knowledge base, so a standup could not stay inside it.
func SelectIdentity(cfg *projects.Config, flag string, current *projects.Identity) (projects.Identity, error) {
	var id projects.Identity
	switch {
	case flag != "":
		found, ok := cfg.Identities[flag]
		if !ok {
			return id, fmt.Errorf("%w: unknown identity %q (choose %s)", ErrIdentity, flag, strings.Join(Allowed(cfg), ", "))
		}
		id = found
	case current != nil:
		id = *current
	default:
		return id, fmt.Errorf("%w: this repo is not in projects.yaml; pass --identity (%s)", ErrIdentity, strings.Join(Allowed(cfg), ", "))
	}
	if !id.Vault {
		return id, fmt.Errorf("%w: identity %q is marked vault: false; recaps cover only %s", ErrIdentity, id.Name, strings.Join(Allowed(cfg), ", "))
	}
	return id, nil
}

// Allowed lists the identities a standup may be drafted for.
func Allowed(cfg *projects.Config) []string {
	var names []string
	for name, id := range cfg.Identities {
		if id.Vault {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// NewScope derives the boards, vault folders and foreign owners for id.
func NewScope(cfg *projects.Config, id projects.Identity) Scope {
	s := Scope{Identity: id, foreign: map[string]bool{}}
	owners := map[string][]string{}
	folders := map[string][]string{}
	for _, p := range cfg.Projects {
		owner := repoOwner(p.Repo)
		owners[owner] = appendNew(owners[owner], p.Identity.Name)
		for _, f := range folderNames(p) {
			folders[f] = appendNew(folders[f], p.Identity.Name)
		}
		if p.Identity.Name == id.Name && p.Board != "" {
			b := Board{Owner: owner, Title: p.Board}
			if !slices.Contains(s.Boards, b) {
				s.Boards = append(s.Boards, b)
			}
		}
	}
	for owner, ids := range owners {
		s.foreign[owner] = !slices.Contains(ids, id.Name)
	}
	for f, ids := range folders {
		if len(ids) == 1 && ids[0] == id.Name {
			s.VaultFolders = append(s.VaultFolders, f)
		}
	}
	sort.Strings(s.VaultFolders)
	return s
}

// Allows reports whether an "owner/name" repo may appear in this standup:
// owners projects.yaml gives only to other identities never do.
func (s Scope) Allows(nameWithOwner string) bool {
	owner, _, _ := strings.Cut(strings.ToLower(nameWithOwner), "/")
	return !s.foreign[owner]
}

// repoOwner returns the owner of a normalized https://github.com/owner/name URL.
func repoOwner(repo string) string {
	owner, _, _ := strings.Cut(strings.TrimPrefix(repo, "https://github.com/"), "/")
	return owner
}

// folderNames are the vault folders a project may read: one named after it,
// plus the ones its vault_folders list adds.
func folderNames(p projects.Project) []string {
	return append([]string{p.Name}, p.VaultFolders...)
}

func appendNew(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}
