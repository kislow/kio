package ki

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// The publish scan keeps attribution out of the public tree, so the fixtures
	// below are assembled at run time.
	coAuthoredBy     = "Co-authored" + "-by: "
	coAuthoredByCaps = "Co-Authored" + "-By: "
	anthropic        = "anthropic" + ".com"
)

func TestLeakMatch(t *testing.T) {
	t.Parallel()
	const host = "acme-laptop.example.com"
	tests := []struct {
		name, text, host, want string
	}{
		{name: "ordinary text", text: "fix(build): repair the cache key\n\nThe key ignored the lockfile.", host: host},
		{name: "empty", text: "", host: host},
		{name: "ki branch prefix", text: "ki/j-abc", host: host, want: "ki/j-"},
		{name: "job id", text: "see job j-20261111-111145-5a25 for details", host: host, want: "j-20261111-111145-5a25"},
		{name: "job id prefix", text: "chore/j-20261111-111145-5a25-cleanup", host: host, want: "j-20261111-111145-5a25"},
		{name: "similar id is fine", text: "j-2026-1111", host: host},
		{name: "worktrees path", text: "built in /home/alice/acme-worktrees/x", host: host, want: "-worktrees/"},
		{name: "full hostname", text: "ran on ACME-LAPTOP.example.com", host: host, want: "acme-laptop.example.com"},
		{name: "short hostname", text: "ran on acme-laptop", host: host, want: "acme-laptop"},
		{name: "short hostname alone is skipped", text: "ran on mac", host: "mac.example.com"},
		{name: "localhost is generic", text: "bind to localhost on ubuntu", host: "localhost"},
		{name: "ubuntu is generic", text: "bind to localhost on ubuntu", host: "ubuntu"},
		{name: "generic domain name", text: "ran on localhost.localdomain", host: "localhost.localdomain"},
		{name: "short letters only", text: "ran on build", host: "build"},
		{name: "whole hostname token", text: "ran on acme-build-01", host: "acme-build-01", want: "acme-build-01"},
		{name: "hostname inside a longer token", text: "ran on acme-build-012", host: "acme-build-01"},
		{name: "hostname after a hyphen", text: "ran on x-acme-build-01", host: "acme-build-01"},
		{name: "short form before a period", text: "ran on acme-build-01.", host: "acme-build-01.example.com", want: "acme-build-01"},
		{name: "no hostname", text: "ran on anything", host: ""},
		{name: "cursor trailer", text: "fix: x\n\n" + coAuthoredBy + "Cursor <cursoragent@cursor.com>", host: host, want: coAuthoredBy + "Cursor <cursoragent@cursor.com>"},
		{name: "claude trailer", text: "fix: x\n\n" + coAuthoredByCaps + "Claude <noreply@" + anthropic + ">", host: host, want: coAuthoredByCaps + "Claude <noreply@" + anthropic + ">"},
		{name: "codex trailer", text: "fix: x\n\n" + coAuthoredBy + "Codex <codex@openai.com>", host: host, want: coAuthoredBy + "Codex <codex@openai.com>"},
		{name: "vendor address only", text: coAuthoredBy + "Bot <bot@eng.anthropic.com>", host: host, want: coAuthoredBy + "Bot <bot@eng.anthropic.com>"},
		{name: "gemini trailer", text: coAuthoredBy + "Gemini <g@example.com>", host: host, want: coAuthoredBy + "Gemini <g@example.com>"},
		{name: "copilot trailer", text: coAuthoredBy + "Copilot <c@example.com>", host: host, want: coAuthoredBy + "Copilot <c@example.com>"},
		{name: "agy trailer", text: coAuthoredBy + "agy <a@example.com>", host: host, want: coAuthoredBy + "agy <a@example.com>"},
		{name: "human trailer", text: "fix: x\n\n" + coAuthoredBy + "Alice <alice@example.com>", host: host},
		{name: "human with lookalike domain", text: coAuthoredBy + "Bob <bob@notcursor.com>", host: host},
		{name: "generated with", text: "fix: x\n\nGenerated with Claude Code", host: host, want: "Generated with Claude Code"},
		{name: "robot emoji", text: "fix: x\n\n🤖 Generated with [Claude Code](https://example.com)", host: host, want: "🤖 Generated with [Claude Code](https://example.com)"},
		{name: "made with", text: "Made with Cursor", host: host, want: "Made with Cursor"},
		{name: "generated with a tool", text: "fix: x\n\nGenerated with mockery", host: host},
		{name: "made with a command", text: "Made with `make manifests`", host: host},
		{name: "robot emoji and a tool", text: "🤖 Generated with mockery", host: host},
		{name: "claude code link", text: "🤖 Generated with [Claude Code](https://claude.com/claude-code)", host: host, want: "🤖 Generated with [Claude Code](https://claude.com/claude-code)"},
		{name: "generated with cursor", text: "Generated with Cursor", host: host, want: "Generated with Cursor"},
		{name: "made with a vendor address", text: "Made with https://cursor.com", host: host, want: "Made with https://cursor.com"},
		{name: "generated mid-sentence is fine", text: "the file is generated with protoc", host: host},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := leakMatch(tc.text, tc.host); got != tc.want {
				t.Fatalf("leakMatch(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestLeakMatchUsesTheMachineHostname(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || len(host) < minHostLen {
		t.Skip("no usable hostname")
	}
	if got := LeakMatch("ran on " + host); got == "" {
		t.Fatalf("the machine's own hostname %q passed", host)
	}
	if got := LeakMatch("fix: ordinary"); got != "" {
		t.Fatalf("ordinary text matched %q", got)
	}
}

func TestStripAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, msg, want string }{
		{name: "cursor", msg: "fix: x\n\n" + coAuthoredBy + "Cursor <cursoragent@cursor.com>\n", want: "fix: x\n"},
		{name: "claude", msg: "fix: x\n\n🤖 Generated with [Claude Code](https://example.com)\n\n" + coAuthoredByCaps + "Claude <noreply@" + anthropic + ">\n", want: "fix: x\n"},
		{name: "human stays", msg: "fix: x\n\n" + coAuthoredBy + "Alice <alice@example.com>\n" + coAuthoredBy + "Codex <codex@openai.com>\n", want: "fix: x\n\n" + coAuthoredBy + "Alice <alice@example.com>\n"},
		{name: "body stays", msg: "fix: x\n\nThe cache key\nignored the lockfile.\n\n" + coAuthoredBy + "Cursor <cursoragent@cursor.com>", want: "fix: x\n\nThe cache key\nignored the lockfile.\n"},
		{name: "tool credit stays", msg: "fix: x\n\nGenerated with mockery\nMade with `make manifests`\n", want: "fix: x\n\nGenerated with mockery\nMade with `make manifests`\n"},
		{name: "agent credit goes", msg: "fix: x\n\nGenerated with Cursor\n", want: "fix: x\n"},
		{name: "nothing to strip", msg: "fix: x\n", want: "fix: x\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StripAttribution(tc.msg); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHooksEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base []string
		want map[string]string
	}{
		{name: "none yet", base: []string{"PATH=/bin"}, want: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/h"}},
		{name: "appended", base: []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c"}, want: map[string]string{"GIT_CONFIG_COUNT": "3", "GIT_CONFIG_KEY_2": "core.hooksPath", "GIT_CONFIG_VALUE_2": "/h"}},
		{name: "unreadable count", base: []string{"GIT_CONFIG_COUNT=x"}, want: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/h"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := HooksEnv(tc.base, "/h")
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestWriteHooks(t *testing.T) {
	t.Parallel()
	repo, dir := filepath.Join(t.TempDir(), "hooks"), filepath.Join(t.TempDir(), "job", "hooks")
	for name, mode := range map[string]os.FileMode{"pre-commit": 0o755, "commit-msg": 0o755, "post-merge": 0o644, "pre-rebase.sample": 0o755} {
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte("#!/bin/sh\n"), mode); err != nil { // #nosec G306 -- a hook fixture
			t.Fatal(err)
		}
	}
	if err := WriteHooks(dir, "/opt/ki's/ki", repo); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{"commit-msg", "pre-commit", "pre-push"}; !slices.Equal(names, want) {
		t.Fatalf("hooks %v, want %v (a non-executable and a sample hook are not passed through)", names, want)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("commit-msg"); !strings.Contains(got, `KI='/opt/ki'\''s/ki'`) || !strings.Contains(got, "REPO='"+filepath.Join(repo, "commit-msg")+"'") || !strings.Contains(got, "delegate guard --strip") {
		t.Errorf("commit-msg:\n%s", got)
	}
	if got := read("pre-push"); !strings.Contains(got, "delegate guard --branch") || !strings.Contains(got, "delegate guard --message-file") || !strings.Contains(got, "%B") {
		t.Errorf("pre-push:\n%s", got)
	}
	if got := read("pre-commit"); !strings.Contains(got, "exec '"+filepath.Join(repo, "pre-commit")+"' \"$@\"") {
		t.Errorf("pre-commit:\n%s", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "pre-push")); err != nil || info.Mode()&0o100 == 0 {
		t.Errorf("pre-push must be executable: %v", err)
	}
}

func TestWriteHooksNeedsTheKiBinary(t *testing.T) {
	t.Parallel()
	if err := WriteHooks(t.TempDir(), "", t.TempDir()); err == nil {
		t.Fatal("hooks that cannot call ki must not be written")
	}
}

func TestWriteHooksWithoutARepoHooksFolder(t *testing.T) {
	t.Parallel()
	if err := WriteHooks(t.TempDir(), "/opt/ki", filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}
