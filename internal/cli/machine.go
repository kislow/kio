package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// machinePath is where this machine's identity list and model policy live.
func (a *App) machinePath() string { return filepath.Join(a.confDir(), ki.MachineFile) }

// machine loads the machine file against the identities projects.yaml knows.
// A missing file is a nil machine: every identity, no model policy.
func (a *App) machine(cfg *projects.Config) (*ki.Machine, error) {
	known := make(map[string]struct{}, len(cfg.Identities))
	for name := range cfg.Identities {
		known[name] = struct{}{}
	}
	m, err := ki.LoadMachine(a.machinePath(), known)
	if err != nil {
		return nil, fail("%v", err)
	}
	return m, nil
}

// setupMachine writes the machine file from the identities given, keeping a
// model policy the file already has. A file that does not validate (an
// identity renamed in projects.yaml, for example) still keeps models, effort
// and roles when those blocks parse; only the identity list is replaced.
func (a *App) setupMachine(h *here, identities []string) error {
	cfg := h.cfg
	if len(identities) == 0 {
		return fail("usage: ki setup machine <identity>...   (identities from projects.yaml this machine carries)")
	}
	for _, id := range identities {
		if _, ok := cfg.Identities[id]; !ok {
			names := make([]string, 0, len(cfg.Identities))
			for n := range cfg.Identities {
				names = append(names, n)
			}
			return fail("identity %q is not in projects.yaml (known: %s)", id, strings.Join(names, ", "))
		}
	}
	existing, err := a.machine(cfg)
	if err != nil {
		existing, err = ki.LoadPolicy(a.machinePath())
		if err != nil {
			return fail("%v", err)
		}
	}
	m := ki.Machine{Identities: identities}
	if existing != nil {
		m.Models, m.Effort, m.Roles, m.Delegate, m.Sections = existing.Models, existing.Effort, existing.Roles, existing.Delegate, existing.Sections
	}
	path := a.machinePath()
	if err := writeFile(path, ki.RenderMachine(m), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintf(a.Stdout, "wrote %s: this machine carries %s\n", a.short(path), strings.Join(identities, ", "))
	return nil
}
