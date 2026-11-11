// Package projects parses and validates deploy/config/projects.yaml: which
// repos exist, which brains they feed, and which GitHub identity owns them.
//
// The Python ingest service validates the same file with the same rules
// (kb/src/ki_kb/projects.py). Both are tested against testdata/projects so
// they cannot drift apart.
package projects

import (
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/kislow/kio/pkg/scope"
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid projects file")

// githubRE also takes an SSH host alias after github.com (`git@github.com-work:`),
// the usual way to keep one key per GitHub account in ~/.ssh/config.
var githubRE = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com(?:-[\w.-]+)?:|ssh://git@github\.com(?:-[\w.-]+)?/)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// NormalizeRepo returns the canonical lower-case https URL for any GitHub remote form.
func NormalizeRepo(url string) (string, error) {
	m := githubRE.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", fmt.Errorf("%w: not a GitHub repo URL: %q", ErrInvalid, url)
	}
	return strings.ToLower("https://github.com/" + m[1] + "/" + m[2]), nil
}

// Identity is one GitHub account.
type Identity struct {
	Name       string
	GitHubUser string
	Email      string
	Credential string
	// Vault is false for employers or clients whose content must never be
	// copied into the vault.
	Vault bool
	// Ingest is false for employers or clients whose repos must never be
	// ingested into any brain. It has no default: every identity states it.
	Ingest bool
}

// IngestRule maps a repo path prefix to a brain.
type IngestRule struct {
	Path  string
	Brain string
}

// Project is one entry in the projects file.
type Project struct {
	Name     string
	Repo     string
	Branch   string
	Identity Identity
	Brains   []string
	Ingest   []IngestRule
	Board    string
	// VaultFolders are extra folders under the vault's projects/ that belong to
	// this project besides the one named after it.
	VaultFolders []string
	// Bulk projects are off-peak only: the kb worker holds their ingest back
	// in peak windows and on a low provider balance, and syncs them last.
	Bulk bool
}

// Workspace is a named view over projects, brains, a board, and vault folders.
type Workspace struct {
	Name     string
	Title    string
	Brains   []string
	Projects []string
	Board    string
	Vault    []string
}

// BrainFor returns the brain a repo-relative file feeds; the longest matching rule wins.
func (p Project) BrainFor(rel string) (string, bool) {
	best := -1
	for i, r := range p.Ingest {
		prefix := strings.TrimSuffix(r.Path, "/")
		if (rel == prefix || strings.HasPrefix(rel, prefix+"/")) && (best < 0 || len(r.Path) > len(p.Ingest[best].Path)) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return p.Ingest[best].Brain, true
}

// Config is a parsed, validated projects file.
type Config struct {
	// SensitiveBrains holds names and families ("client-*").
	SensitiveBrains []string
	Identities      map[string]Identity
	Projects        []Project
	Workspaces      []Workspace
}

// IsSensitive reports whether a brain may only be read when named explicitly.
func (c *Config) IsSensitive(brain string) bool { return scope.MatchesAny(c.SensitiveBrains, brain) }

type rawIdentity struct {
	GitHubUser string `yaml:"github_user"`
	Email      string `yaml:"email"`
	Credential string `yaml:"credential"`
	Vault      *bool  `yaml:"vault"`
	Ingest     *bool  `yaml:"ingest"`
}

type rawRule struct {
	Path  string `yaml:"path"`
	Brain string `yaml:"brain"`
}

type rawProject struct {
	Name     string    `yaml:"name"`
	Repo     string    `yaml:"repo"`
	Branch   *string   `yaml:"branch"`
	Identity string    `yaml:"identity"`
	Brains   []string  `yaml:"brains"`
	Ingest   []rawRule `yaml:"ingest"`
	Board    string    `yaml:"board"`
	Bulk     bool      `yaml:"bulk"`

	VaultFolders []string `yaml:"vault_folders"`
}

type rawWorkspace struct {
	Name     string   `yaml:"name"`
	Title    string   `yaml:"title"`
	Brains   []string `yaml:"brains"`
	Projects []string `yaml:"projects"`
	Board    string   `yaml:"board"`
	Vault    []string `yaml:"vault"`
}

type rawFile struct {
	SensitiveBrains []string               `yaml:"sensitive_brains"`
	Identities      map[string]rawIdentity `yaml:"identities"`
	Projects        []rawProject           `yaml:"projects"`
	Workspaces      []rawWorkspace         `yaml:"workspaces"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Parse validates and returns a projects file.
func Parse(data []byte) (*Config, error) {
	var raw rawFile
	// Strict: an unknown key (a typo such as ingest_rules) is an error, not silently ignored.
	if err := yaml.UnmarshalWithOptions(data, &raw, yaml.Strict()); err != nil {
		return nil, invalid("%v", err)
	}

	cfg := &Config{Identities: map[string]Identity{}}
	for _, b := range raw.SensitiveBrains {
		if !scope.ValidEntry(b) {
			return nil, invalid("invalid brain name %q", b)
		}
		if b == scope.Cortex || scope.Matches(b, scope.Cortex) {
			return nil, invalid("cortex cannot be sensitive")
		}
		cfg.SensitiveBrains = append(cfg.SensitiveBrains, b)
	}

	for name, ri := range raw.Identities {
		where := fmt.Sprintf("identity %q", name)
		if !scope.ValidName(name) || !scope.ValidName(ri.Credential) {
			return nil, invalid("%s: invalid name or credential", where)
		}
		if ri.GitHubUser == "" || ri.Email == "" {
			return nil, invalid("%s: github_user and email are required", where)
		}
		if ri.Ingest == nil {
			return nil, invalid("%s: ingest must be set to true or false", where)
		}
		vault := ri.Vault == nil || *ri.Vault
		cfg.Identities[name] = Identity{
			Name: name, GitHubUser: ri.GitHubUser, Email: ri.Email, Credential: ri.Credential,
			Vault: vault, Ingest: *ri.Ingest,
		}
	}

	names := map[string]bool{}
	repoOwner := map[string]string{}
	for _, rp := range raw.Projects {
		where := fmt.Sprintf("project %q", rp.Name)
		if !scope.ValidName(rp.Name) || names[rp.Name] {
			return nil, invalid("%s: invalid or duplicate name", where)
		}
		names[rp.Name] = true

		ident, ok := cfg.Identities[rp.Identity]
		if !ok {
			return nil, invalid("%s: unknown identity %q", where, rp.Identity)
		}
		repo, err := NormalizeRepo(rp.Repo)
		if err != nil {
			return nil, invalid("%s: %v", where, err)
		}
		if owner, seen := repoOwner[repo]; seen && owner != ident.Name {
			return nil, invalid("%s: %s is listed under two identities", where, repo)
		}
		repoOwner[repo] = ident.Name

		if len(rp.Brains) == 0 {
			return nil, invalid("%s: brains must be a non-empty list", where)
		}
		inScope := map[string]bool{scope.Cortex: true}
		for _, b := range rp.Brains {
			if !scope.ValidName(b) {
				return nil, invalid("%s: invalid brain name %q", where, b)
			}
			inScope[b] = true
		}

		if len(rp.Ingest) > 0 && !ident.Ingest {
			return nil, invalid("%s: identity %q has ingest: false, so the project may not have ingest rules", where, ident.Name)
		}

		var rules []IngestRule
		for _, r := range rp.Ingest {
			if r.Path == "" || !scope.ValidName(r.Brain) {
				return nil, invalid("%s: ingest rules need a path and a valid brain", where)
			}
			if !inScope[r.Brain] {
				return nil, invalid("%s: ingest brain %q is not in brains", where, r.Brain)
			}
			if path.IsAbs(r.Path) || containsDotDot(r.Path) {
				return nil, invalid("%s: ingest path %q must stay inside the repo", where, r.Path)
			}
			rules = append(rules, IngestRule(r))
		}

		branch := "main"
		if rp.Branch != nil {
			if *rp.Branch == "" {
				return nil, invalid("%s: branch must not be empty", where)
			}
			branch = *rp.Branch
		}
		seenFolders := map[string]bool{}
		for _, f := range rp.VaultFolders {
			if !scope.ValidName(f) || seenFolders[f] {
				return nil, invalid("%s: vault_folders entries must be unique valid names, got %q", where, f)
			}
			seenFolders[f] = true
		}
		cfg.Projects = append(cfg.Projects, Project{
			Name: rp.Name, Repo: repo, Branch: branch, Identity: ident,
			Brains: rp.Brains, Ingest: rules, Board: rp.Board, Bulk: rp.Bulk,
			VaultFolders: rp.VaultFolders,
		})
	}
	if err := validateWorkspaces(raw.Workspaces, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validateWorkspaces(raw []rawWorkspace, cfg *Config) error {
	projectBrains := make(map[string]map[string]bool, len(cfg.Projects))
	projectNames := make(map[string]bool, len(cfg.Projects))
	for _, project := range cfg.Projects {
		projectNames[project.Name] = true
		projectBrains[project.Name] = make(map[string]bool, len(project.Brains))
		for _, brain := range project.Brains {
			projectBrains[project.Name][brain] = true
		}
	}
	workspaceNames := map[string]bool{}
	type vaultFolder struct{ path, owner string }
	var vaultFolders []vaultFolder
	for _, rw := range raw {
		where := fmt.Sprintf("workspace %q", rw.Name)
		if !scope.ValidName(rw.Name) || workspaceNames[rw.Name] {
			return invalid("%s: invalid or duplicate name", where)
		}
		workspaceNames[rw.Name] = true
		if len(rw.Brains) == 0 {
			return invalid("%s: brains must be a non-empty list", where)
		}
		if len(rw.Vault) == 0 {
			return invalid("%s: vault must be a non-empty list", where)
		}
		listedProjects := map[string]bool{}
		for _, name := range rw.Projects {
			if !projectNames[name] {
				return invalid("%s: unknown project %q", where, name)
			}
			if listedProjects[name] {
				return invalid("%s: duplicate project %q", where, name)
			}
			listedProjects[name] = true
		}
		seenBrains := map[string]bool{}
		sensitive, nonsensitive := false, false
		for _, brain := range rw.Brains {
			if !scope.ValidName(brain) {
				return invalid("%s: invalid brain name %q", where, brain)
			}
			if brain == "voice" || brain == "security" {
				return invalid("%s: %q is not allowed as a workspace brain", where, brain)
			}
			if seenBrains[brain] {
				return invalid("%s: duplicate brain %q", where, brain)
			}
			seenBrains[brain] = true
			if brain != scope.Cortex {
				found := false
				for project := range listedProjects {
					if projectBrains[project][brain] {
						found = true
						break
					}
				}
				if !found {
					return invalid("%s: brain %q is not listed by any workspace project", where, brain)
				}
			}
			if cfg.IsSensitive(brain) {
				sensitive = true
			} else {
				nonsensitive = true
			}
		}
		if sensitive && nonsensitive {
			return invalid("%s: brains may not mix sensitive and non-sensitive", where)
		}
		for _, folder := range rw.Vault {
			if !validWorkspaceVault(folder) {
				return invalid("%s: vault path %q must be a relative folder under projects/ or diagrams/projects/, ending in /", where, folder)
			}
			for _, used := range vaultFolders {
				if used.path == folder {
					return invalid("%s: vault folder %q is already used by workspace %q", where, folder, used.owner)
				}
				// Folders end in "/", so a prefix match is a match on whole path segments.
				if used.owner != rw.Name && (strings.HasPrefix(used.path, folder) || strings.HasPrefix(folder, used.path)) {
					return invalid("%s: vault folder %q is nested with folder %q of workspace %q", where, folder, used.path, used.owner)
				}
			}
			vaultFolders = append(vaultFolders, vaultFolder{folder, rw.Name})
		}
		cfg.Workspaces = append(cfg.Workspaces, Workspace(rw))
	}
	return nil
}

func validWorkspaceVault(folder string) bool {
	if path.IsAbs(folder) || !strings.HasSuffix(folder, "/") || containsDotDot(folder) {
		return false
	}
	if !strings.HasPrefix(folder, "projects/") && !strings.HasPrefix(folder, "diagrams/projects/") {
		return false
	}
	clean := path.Clean(folder)
	return clean != "projects" && clean != "diagrams/projects" && clean == strings.TrimSuffix(folder, "/")
}

func containsDotDot(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// Load reads and validates a projects file.
func Load(file string) (*Config, error) {
	data, err := os.ReadFile(file) // #nosec G304 -- path chosen by the operator
	if err != nil {
		return nil, fmt.Errorf("reading projects file: %w", err)
	}
	return Parse(data)
}
