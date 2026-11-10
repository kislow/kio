package ki

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/kislow/kio/pkg/projects"
	"github.com/kislow/kio/pkg/scope"
)

// MixedIdentitiesError is the refusal for a folder whose mapped repos belong
// to more than one identity: a session has one GH_TOKEN and one knowledge-base
// token for its whole life, so it cannot follow the agent across them.
type MixedIdentitiesError struct{ Identities []string }

func (e MixedIdentitiesError) Error() string {
	return fmt.Sprintf("the repos in this folder belong to several identities (%s); pass --identity NAME to start as one of them",
		strings.Join(e.Identities, ", "))
}

// ResolveFolder is Resolve for a folder that is not inside a repo: remotes are
// the origin remotes of the git repos directly under it. Mapped repos of one
// identity give that identity and the union of their projects' brains,
// sensitive ones included exactly as Resolve does for one repo; repos that are
// not in projects.yaml are ignored; none mapped gives a cortex-only session
// with no identity. Several identities are a MixedIdentitiesError.
func ResolveFolder(cfg *projects.Config, remotes, extra []string) (Session, error) {
	s := Session{Folder: true}
	var identities []string
	for _, remote := range remotes {
		r := Resolve(cfg, remote, nil)
		if r.Unmapped {
			continue
		}
		if !slices.Contains(identities, r.Identity.Name) {
			identities = append(identities, r.Identity.Name)
		}
		if s.Identity == nil {
			s.Identity, s.Board = r.Identity, r.Board
		}
		s.Projects = appendUnique(s.Projects, r.Projects...)
		s.Brains = appendUnique(s.Brains, r.Brains...)
	}
	if len(identities) > 1 {
		sort.Strings(identities)
		return Session{}, MixedIdentitiesError{Identities: identities}
	}
	if s.Identity == nil {
		s.Unmapped = true
		s.Brains = []string{scope.Cortex}
	}
	s.Brains = appendUnique(s.Brains, extra...)
	for _, b := range s.Brains {
		if cfg.IsSensitive(b) {
			s.Sensitive = true
		}
	}
	return s, nil
}

// IdentitySession is the session `--identity NAME` asks for outside a repo:
// that identity's gh account, cortex and the brains of its projects that are
// not sensitive (a sensitive brain is never in a default scope and has to be
// named), plus extra.
func IdentitySession(cfg *projects.Config, id projects.Identity, extra []string) Session {
	s := Session{Folder: true, Identity: &id, Brains: []string{scope.Cortex}}
	for _, p := range cfg.Projects {
		if p.Identity.Name != id.Name {
			continue
		}
		for _, b := range p.Brains {
			if !cfg.IsSensitive(b) {
				s.Brains = appendUnique(s.Brains, b)
			}
		}
	}
	s.Brains = appendUnique(s.Brains, extra...)
	for _, b := range s.Brains {
		if cfg.IsSensitive(b) {
			s.Sensitive = true
		}
	}
	return s
}
