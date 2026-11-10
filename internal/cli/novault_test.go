package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

// Without KI_VAULT and without a vault workspace in projects.yaml, the vault
// and the diagram builder are not part of the machine's setup at all.
func TestNoVaultConfiguration(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }
	e.vars["CODEX_HOME"] = filepath.Join(e.vars["HOME"], "codex")
	write(t, filepath.Join(e.harness, "diagram", "bin", "ki-diagram.mjs"), "// tool\n")
	ctx := context.Background()

	_ = e.app.Run(ctx, []string{"doctor"}) // other checks may fail in a temp env
	for _, banned := range []string{"KI_VAULT", "\nVault\n", "\nDiagrams\n", "ki diagram", "vault server", "ki-vault"} {
		if strings.Contains(e.out.String(), banned) {
			t.Errorf("doctor mentions %q without a vault:\n%s", banned, e.out)
		}
	}

	e.out.Reset()
	if err := e.app.Run(ctx, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.out.String(), "KI_VAULT") {
		t.Errorf("status mentions the vault:\n%s", e.out)
	}

	e.out.Reset()
	if err := e.app.Run(ctx, []string{"setup", "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "config.toml"))
	if strings.Contains(string(cfg), ki.CodexVaultTable) || strings.Contains(e.out.String(), "ki-vault") {
		t.Errorf("setup codex wrote or named the vault server:\n%s\n%s", cfg, e.out)
	}
}

func TestDoctorChecksTheVaultOnceConfigured(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.vars["KI_VAULT"] = t.TempDir()
	_ = e.app.Run(context.Background(), []string{"doctor"})
	if out := e.out.String(); !strings.Contains(out, "\nVault\n") || !strings.Contains(out, "KI_VAULT") {
		t.Fatalf("doctor lacks the Vault section:\n%s", out)
	}
}
