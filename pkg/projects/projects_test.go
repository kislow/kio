package projects

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The same fixtures are checked by kb/tests/test_projects_parity.py, so the Go
// and Python validators accept and reject exactly the same files.
const fixtures = "../../testdata/projects"

func TestFixtures(t *testing.T) {
	for _, dir := range []string{"valid", "invalid"} {
		files, err := filepath.Glob(filepath.Join(fixtures, dir, "*.yaml"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no %s fixtures: %v", dir, err)
		}
		for _, f := range files {
			t.Run(dir+"/"+filepath.Base(f), func(t *testing.T) {
				_, err := Load(f)
				if dir == "valid" && err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				if dir == "invalid" && !errors.Is(err, ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %v", err)
				}
			})
		}
	}
}

func TestShippedProjectsFile(t *testing.T) {
	if _, err := Load("../../deploy/config/projects.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestFamilies(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "families.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsSensitive("client") || !cfg.IsSensitive("client-acme") || cfg.IsSensitive("clientx") || cfg.IsSensitive("cortex") {
		t.Fatal("family sensitivity")
	}
}

func TestBulkFlag(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "bulk.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d", len(cfg.Projects))
	}
	if !cfg.Projects[0].Bulk || cfg.Projects[1].Bulk {
		t.Fatalf("bulk flags: want [true, false], got [%v, %v]", cfg.Projects[0].Bulk, cfg.Projects[1].Bulk)
	}
}

func TestVaultFolders(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "vault-folders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Projects[0].VaultFolders; !slices.Equal(got, []string{"acme", "acme-platform"}) || cfg.Projects[1].VaultFolders != nil {
		t.Fatalf("vault folders: %v, %v", got, cfg.Projects[1].VaultFolders)
	}
}

func TestVaultRead(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "vault-read.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Identities["work"].VaultRead; !slices.Equal(got, []string{"projects/acme", "engineering", "reference"}) {
		t.Fatalf("work vault_read: %v", got)
	}
	if got := cfg.Identities["other"].VaultRead; got != nil {
		t.Fatalf("an identity without vault_read must read the whole vault, got %v", got)
	}
}

func TestParsedWorkspaces(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "workspaces.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Workspaces) != 2 || cfg.Workspaces[0].Name != "work" || cfg.Workspaces[1].Name != "demo" {
		t.Fatalf("unexpected workspaces: %+v", cfg.Workspaces)
	}
	if got := cfg.Workspaces[0].Vault; len(got) != 1 || got[0] != "projects/work/" {
		t.Fatalf("unexpected first workspace vault: %v", got)
	}
}

func TestParsedValues(t *testing.T) {
	cfg, err := Load(filepath.Join(fixtures, "valid", "full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	co := cfg.Projects[0]
	if co.Repo != "https://github.com/client-a/acme" || co.Board != "Acme" {
		t.Fatalf("unexpected project %+v", co)
	}
	if b, _ := co.BrainFor("docs/patterns/retry.md"); b != "cortex" {
		t.Fatalf("longest rule should win, got %q", b)
	}
	if _, ok := co.BrainFor("src/main.go"); ok {
		t.Fatal("unmatched path must not map to a brain")
	}
	if !cfg.IsSensitive("client") || cfg.IsSensitive("acme") {
		t.Fatal("client is sensitive, acme is not")
	}
	if cfg.Identities["client-a"].Vault || !cfg.Identities["personal"].Vault {
		t.Fatal("vault flag: default true, explicit false honoured")
	}
}

func TestNormalizeRepo(t *testing.T) {
	for _, in := range []string{
		"git@github.com:Org/Repo.git",
		"https://github.com/org/repo",
		"https://github.com/org/repo.git",
		"ssh://git@github.com/org/repo.git",
		"git@github.com-work:org/repo.git",
		"ssh://git@github.com-work.2/org/repo",
	} {
		got, err := NormalizeRepo(in)
		if err != nil || got != "https://github.com/org/repo" {
			t.Errorf("%s: got %q, %v", in, got, err)
		}
	}
}

func TestNormalizeRepoRefusesOtherHosts(t *testing.T) {
	for _, in := range []string{
		"git@gitlab.com:org/repo.git",
		"git@github.company.com:org/repo.git",
		"git@github.com-:org/repo.git",
		"https://github.com-work/org/repo",
	} {
		if got, err := NormalizeRepo(in); err == nil {
			t.Errorf("%s: want refusal, got %q", in, got)
		}
	}
}

func TestParseErrorsMentionProject(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join(fixtures, "invalid", "unknown-identity.yaml"))
	_, err := Parse(data)
	if err == nil || !strings.Contains(err.Error(), `project "x"`) {
		t.Fatalf("error should name the project: %v", err)
	}
}
