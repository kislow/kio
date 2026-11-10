package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

func TestParseDelegateLogArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		args  []string
		want  string
		vault bool
		bad   bool
	}{
		{name: "defaults", want: "2026-10-26"},
		{name: "date and flags", args: []string{"--date", "2026-10-25", "--vault"}, want: "2026-10-25", vault: true},
		{name: "invalid date", args: []string{"--date", "yesterday"}, bad: true},
		{name: "unknown argument", args: []string{"extra"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDelegateLogArgs(tc.args, time.Date(2026, 10, 26, 23, 0, 0, 0, time.FixedZone("local", 4*60*60)))
			if tc.bad {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil || got.date.Format(time.DateOnly) != tc.want || got.vault != tc.vault {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestRenderDelegateLog(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		jobs []logJob
		want []string
		omit []string
	}{
		{
			name: "job metadata",
			jobs: []logJob{{ID: "j-20261025-161805-4fdf", Machine: "mbp", Agent: "codex", Model: "gpt-5-mini", Repo: "kio", Issue: "219", Outcome: "PR", PR: "https://github.com/acme/r/pull/8", Started: day.Add(16 * time.Hour), Duration: 12.5, Tokens: ki.Tokens{Input: 3, Output: 4, CacheRead: 5}}},
			want: []string{"2026-10-25", "16:00:00", "mbp", "codex", "gpt-5-mini", "kio", "219", "PR", "https://github.com/acme/r/pull/8", "12.5s", "3/4/5"},
		},
		{
			name: "sensitive project hides all metadata",
			jobs: []logJob{{ID: "j-secret", Machine: "private-host", Agent: "claude", Model: "secret-model", Repo: "private-project", Issue: "77", Outcome: "DONE", PR: "https://private.invalid", Started: day, Duration: 99, Tokens: ki.Tokens{Input: 999}, Sensitive: true}},
			want: []string{"j-secret", "DONE"},
			omit: []string{"private-host", "claude", "secret-model", "private-project", "77", "private.invalid", "99.0s", "999"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := renderDelegateLog(day, tc.jobs)
			second := renderDelegateLog(day, tc.jobs)
			if first != second {
				t.Fatal("same day's note changed between renders")
			}
			for _, want := range tc.want {
				if !strings.Contains(first, want) {
					t.Errorf("note misses %q: %s", want, first)
				}
			}
			for _, unwanted := range tc.omit {
				if strings.Contains(first, unwanted) {
					t.Errorf("note contains %q: %s", unwanted, first)
				}
			}
		})
	}
}

func TestSensitiveProjectJobRendersOnlyIDAndOutcome(t *testing.T) {
	t.Parallel()
	cfg := &projects.Config{SensitiveBrains: []string{"client"}}
	project := projects.Project{Name: "client-work", Identity: projects.Identity{Vault: true}, Brains: []string{"client"}}
	if !sensitiveLogProject(cfg, project) {
		t.Fatal("project with a sensitive brain must be treated as sensitive")
	}
	entry := logJob{ID: "j-20261025-010101-abcd", Machine: "machine-secret", Agent: "codex", Model: "private-model", Repo: project.Name, Issue: "19", Outcome: "DONE", PR: "private-pr", Started: time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), Duration: 99, Tokens: ki.Tokens{Input: 42}, Sensitive: sensitiveLogProject(cfg, project)}
	note := renderDelegateLog(entry.Started, []logJob{entry})
	for _, want := range []string{entry.ID, entry.Outcome} {
		if !strings.Contains(note, want) {
			t.Errorf("note misses %q", want)
		}
	}
	for _, secret := range []string{entry.Machine, entry.Agent, entry.Model, entry.Repo, entry.Issue, entry.PR, "99.0s", "42"} {
		if strings.Contains(note, secret) {
			t.Errorf("sensitive note contains %q", secret)
		}
	}
}

func TestDelegateLogVaultRewriteIsIdempotent(t *testing.T) {
	v := newVaultEnv(t)
	write(t, filepath.Join(v.a, "AGENTS.md"), "Generated agent logs belong in agents/. Use YAML frontmatter.\n")
	for i := 0; i < 2; i++ {
		v.out.Reset()
		if err := v.app.Run(context.Background(), []string{"delegate", "log", "--date", "2026-10-25", "--vault"}); err != nil {
			t.Fatalf("run %d: %v\n%s", i+1, err, v.out.String())
		}
	}
	path := filepath.Join(v.a, "agents", "2026-10-25 Agent Log.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "created: 2026-10-25") || !strings.Contains(string(got), "No delegated jobs.") {
		t.Fatalf("unexpected note: %s", got)
	}
	if out := v.out.String(); !strings.Contains(out, "nothing new to commit") {
		t.Fatalf("second write was not idempotent: %s", out)
	}
}

func TestWriteDelegateLogRejectsCredentialShapedOutput(t *testing.T) {
	t.Parallel()
	a := &App{}
	err := a.writeDelegateLog(context.Background(), time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), "# Agent log\napi_token=abc123456789\n")
	if !isUserError(err) || !strings.Contains(err.Error(), "refusing to write") {
		t.Fatalf("expected credential refusal, got %v", err)
	}
}
