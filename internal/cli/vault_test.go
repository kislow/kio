package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vaultProjectsYAML = `sensitive_brains: [client, client-*]
identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: false, ingest: true}
  personal: {github_user: me, email: me@personal.example, credential: personal, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app, voice]}
  - {name: vault, repo: https://github.com/me/vault, identity: personal, brains: [cortex]}
  - {name: work-vault, repo: https://github.com/work/vault, identity: work, brains: [app]}
`

// vaultEnv is a ki environment plus a bare origin and two clones of it, the
// way the vault is shared between two laptops.
type vaultEnv struct {
	*env
	origin, a, b string
}

func newVaultEnv(t *testing.T) *vaultEnv {
	t.Helper()
	// The user's own git config must not leak in: signing or hooks would break the commits.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	e := newEnv(t, "")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), vaultProjectsYAML)
	base := t.TempDir()
	v := &vaultEnv{env: e, origin: filepath.Join(base, "origin.git"), a: filepath.Join(base, "a"), b: filepath.Join(base, "b")}

	seed := filepath.Join(base, "seed")
	gitIn(t, base, "init", "-q", "-b", "main", seed)
	gitIn(t, seed, "config", "user.email", "me@personal.example")
	gitIn(t, seed, "config", "user.name", "me")
	write(t, filepath.Join(seed, "Home.md"), "# Home\n")
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "seed")
	gitIn(t, base, "clone", "-q", "--bare", seed, v.origin)
	for _, clone := range []string{v.a, v.b} {
		gitIn(t, base, "clone", "-q", v.origin, clone)
		gitIn(t, clone, "config", "user.email", "me@personal.example")
		gitIn(t, clone, "config", "user.name", "me")
	}
	e.vars["KI_VAULT"] = v.a
	return v
}

func (v *vaultEnv) push(t *testing.T, vault string, args ...string) (string, error) {
	t.Helper()
	v.vars["KI_VAULT"] = vault
	v.out.Reset()
	err := v.app.Run(context.Background(), append([]string{"vault", "push"}, args...))
	return v.out.String(), err
}

func TestVaultPushRoundTrip(t *testing.T) {
	v := newVaultEnv(t)

	write(t, filepath.Join(v.a, "projects", "x", "decisions", "2026-10-18-one.md"), "one\n")
	out, err := v.push(t, v.a, "-m", "decision: one")
	if err != nil || !strings.Contains(out, "committed 1 file(s): decision: one") || !strings.Contains(out, "vault at ") {
		t.Fatalf("first push: %v\n%s", err, out)
	}

	// The other machine has nothing of its own yet; a push still pulls what arrived.
	out, err = v.push(t, v.b)
	if err != nil || !strings.Contains(out, "nothing new to commit") {
		t.Fatalf("empty push: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(v.b, "projects", "x", "decisions", "2026-10-18-one.md")); err != nil {
		t.Fatalf("b did not receive a's note: %v", err)
	}

	// Both sides add different notes; the second pusher rebases and still lands.
	write(t, filepath.Join(v.a, "reviews", "pr-1.md"), "a\n")
	write(t, filepath.Join(v.b, "engineering", "Note.md"), "b\n")
	if out, err := v.push(t, v.b); err != nil {
		t.Fatalf("b push: %v\n%s", err, out)
	}
	out, err = v.push(t, v.a)
	if err != nil || !strings.Contains(out, "vault: ") { // default message names the host
		t.Fatalf("a push: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(v.a, "engineering", "Note.md")); err != nil {
		t.Fatalf("a did not receive b's note: %v", err)
	}
	gitIn(t, v.b, "pull", "-q", "--rebase", "origin", "main")
	if _, err := os.Stat(filepath.Join(v.b, "reviews", "pr-1.md")); err != nil {
		t.Fatalf("origin lacks a's note: %v", err)
	}
}

func TestVaultPushStopsOnConflict(t *testing.T) {
	v := newVaultEnv(t)
	write(t, filepath.Join(v.a, "Home.md"), "# Home\nfrom a\n")
	write(t, filepath.Join(v.b, "Home.md"), "# Home\nfrom b\n")
	if out, err := v.push(t, v.a); err != nil {
		t.Fatalf("a push: %v\n%s", err, out)
	}
	_, err := v.push(t, v.b)
	if !isUserError(err) || !strings.Contains(err.Error(), "pull stopped on a conflict") || !strings.Contains(err.Error(), "rebase --continue") {
		t.Fatalf("want conflict guidance, got %v", err)
	}
	// Nothing is pushed while the rebase is open, and the next run says so.
	_, err = v.push(t, v.b)
	if !isUserError(err) || !strings.Contains(err.Error(), "rebase is in progress") {
		t.Fatalf("want rebase refusal, got %v", err)
	}
	gitIn(t, v.b, "rebase", "--abort")
	gitIn(t, v.b, "reset", "-q", "--hard", "origin/main")
	if out, err := v.push(t, v.b); err != nil || !strings.Contains(out, "nothing new to commit") {
		t.Fatalf("after abort: %v\n%s", err, out)
	}
}

func TestVaultPushRefusals(t *testing.T) {
	v := newVaultEnv(t)

	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{{"vault"}, {"vault", "pull"}, {"vault", "push", "--now"}, {"vault", "push", "-m"}} {
			if err := v.app.Run(context.Background(), args); !isUserError(err) || !strings.Contains(err.Error(), "usage:") {
				t.Errorf("%v: got %v", args, err)
			}
		}
	})
	t.Run("no KI_VAULT", func(t *testing.T) {
		if _, err := v.push(t, ""); !isUserError(err) || !strings.Contains(err.Error(), "KI_VAULT is not set") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		if _, err := v.push(t, t.TempDir()); !isUserError(err) || !strings.Contains(err.Error(), "not a git repo") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no origin", func(t *testing.T) {
		gitIn(t, v.b, "remote", "remove", "origin")
		if _, err := v.push(t, v.b); !isUserError(err) || !strings.Contains(err.Error(), "no origin remote") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("vault committed as the wrong person", func(t *testing.T) {
		// origin is the GitHub URL projects.yaml knows; the check runs before any network call.
		gitIn(t, v.a, "remote", "set-url", "origin", "https://github.com/me/vault.git")
		gitIn(t, v.a, "config", "user.email", "me@work.example")
		_, err := v.push(t, v.a)
		if !isUserError(err) || !strings.Contains(err.Error(), `in the vault, this repo belongs to identity "personal"`) {
			t.Fatalf("got %v", err)
		}
		gitIn(t, v.a, "config", "user.email", "me@personal.example")
		gitIn(t, v.a, "remote", "set-url", "origin", v.origin)
	})
	t.Run("vault listed under an identity with vault false", func(t *testing.T) {
		gitIn(t, v.a, "remote", "set-url", "origin", "https://github.com/work/vault")
		_, err := v.push(t, v.a)
		if !isUserError(err) || !strings.Contains(err.Error(), "vault: false; fix projects.yaml") {
			t.Fatalf("got %v", err)
		}
		gitIn(t, v.a, "remote", "set-url", "origin", v.origin)
	})
	t.Run("from a repo whose identity never reaches the vault", func(t *testing.T) {
		gitIn(t, v.repo, "remote", "add", "origin", "https://github.com/work/app")
		_, err := v.push(t, v.a)
		if !isUserError(err) || !strings.Contains(err.Error(), `identity "work"`) || !strings.Contains(err.Error(), "vault: false") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestDoctorChecksVault(t *testing.T) {
	v := newVaultEnv(t)
	for _, tc := range []struct{ dir, want string }{
		{v.a, "✓ KI_VAULT"},
		{t.TempDir(), "read-only (not a git clone)"},
		{filepath.Join(t.TempDir(), "missing"), "✗ KI_VAULT"},
	} {
		v.vars["KI_VAULT"] = tc.dir
		v.out.Reset()
		_ = v.app.Run(context.Background(), []string{"doctor"}) // other checks may fail on CI; only the vault line matters here
		if !strings.Contains(v.out.String(), tc.want) {
			t.Errorf("KI_VAULT=%q: doctor output lacks %q:\n%s", tc.dir, tc.want, v.out)
		}
	}
	v.vars["KI_VAULT"] = ""
	v.out.Reset()
	_ = v.app.Run(context.Background(), []string{"doctor"})
	if strings.Contains(v.out.String(), "KI_VAULT") {
		t.Errorf("an unset KI_VAULT is a choice, doctor must not mention it:\n%s", v.out)
	}
}
