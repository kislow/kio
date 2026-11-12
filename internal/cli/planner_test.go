package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const plannerMachineYAML = `identities: [work]
models:
  judgement: {zz-b: policy-model}
roles:
  planner:
    strategy: ordered
    providers: [zz-a:a-model, zz-b, zz-c]
`

// plannerEnv is a session env whose PATH holds the named fake agents, and
// whose machine file defines roles.planner unless machine is "-".
func plannerEnv(t *testing.T, machine string, agents ...string) *env {
	t.Helper()
	e := newEnv(t, "")
	bin := t.TempDir()
	for _, name := range agents {
		write(t, filepath.Join(bin, name), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if machine != "-" {
		write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), machine)
	}
	return e
}

func (e *env) stderr() string { return e.app.Stderr.(*bytes.Buffer).String() }

func (e *env) overQuota(t *testing.T, agent string, until time.Time) {
	t.Helper()
	r := ki.Result{ID: ki.NewJobID(time.Now()), Provider: agent, Role: "execution", Status: ki.JobQuota, Started: time.Now().Add(-time.Minute), QuotaUntil: &until}
	if err := ki.AppendLedger(filepath.Join(e.app.jobsDir(), ki.LedgerFile), r); err != nil {
		t.Fatal(err)
	}
}

func TestBareKiStartsThePlannersFirstEligibleAgent(t *testing.T) {
	tests := []struct {
		name       string
		agents     []string
		quota      string // agent left over quota for an hour
		vars       map[string]string
		args       []string
		wantAgent  string
		wantModel  string
		wantStderr []string
	}{
		{"first provider with its model", []string{"zz-a", "zz-b"}, "", nil, nil, "zz-a", "a-model", []string{"planner=zz-a:a-model", "model=a-model"}},
		{"over quota is skipped and named", []string{"zz-a", "zz-b"}, "zz-a", nil, nil, "zz-b", "policy-model", []string{"ki: role planner: zz-a:a-model skipped, over quota until", "planner=zz-b"}},
		{"missing agent is skipped", []string{"zz-b"}, "", nil, nil, "zz-b", "policy-model", []string{"zz-a:a-model skipped, not on PATH"}},
		{"KI_MODEL beats the provider model", []string{"zz-a"}, "", map[string]string{"KI_MODEL": "env-model"}, nil, "zz-a", "env-model", nil},
		{"--model beats KI_MODEL", []string{"zz-a"}, "", map[string]string{"KI_MODEL": "env-model"}, []string{"--model", "flag-model"}, "zz-a", "flag-model", nil},
		{"KI_AGENT beats the planner", []string{"zz-a", "zz-b"}, "", map[string]string{"KI_AGENT": "zz-b"}, nil, "zz-b", "policy-model", []string{"model=policy-model"}},
		{"--agent beats KI_AGENT", []string{"zz-a", "zz-b"}, "", map[string]string{"KI_AGENT": "zz-b"}, []string{"--agent", "zz-a"}, "zz-a", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := plannerEnv(t, plannerMachineYAML, tt.agents...)
			for k, v := range tt.vars {
				e.vars[k] = v
			}
			if tt.quota != "" {
				e.overQuota(t, tt.quota, time.Now().Add(time.Hour))
			}
			ledgerBefore, _ := os.ReadFile(filepath.Join(e.app.jobsDir(), ki.LedgerFile))
			if err := e.app.Run(t.Context(), append([]string{"--force"}, tt.args...)); err != nil {
				t.Fatal(err)
			}
			if len(e.execd) == 0 || e.execd[0] != tt.wantAgent {
				t.Fatalf("argv %v, want agent %s", e.execd, tt.wantAgent)
			}
			gotModel := ""
			for _, kv := range e.execEnv {
				if v, ok := strings.CutPrefix(kv, "KI_MODEL="); ok {
					gotModel = v
				}
			}
			if gotModel != tt.wantModel {
				t.Fatalf("KI_MODEL %q, want %q", gotModel, tt.wantModel)
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(e.stderr(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, e.stderr())
				}
			}
			// A session is not a job: the pick reserves nothing and records nothing.
			if _, err := os.Stat(filepath.Join(e.app.jobsDir(), ki.ReservationFile)); err == nil {
				t.Fatal("a planner pick wrote a reservation")
			}
			if after, _ := os.ReadFile(filepath.Join(e.app.jobsDir(), ki.LedgerFile)); !bytes.Equal(after, ledgerBefore) {
				t.Fatal("a planner pick wrote the ledger")
			}
		})
	}
}

func TestBareKiRefusesWhenNoPlannerProviderIsEligible(t *testing.T) {
	e := plannerEnv(t, "identities: [work]\nroles:\n  planner:\n    providers: [zz-a, zz-missing]\n", "zz-a")
	until := time.Now().Add(90 * time.Minute)
	e.overQuota(t, "zz-a", until)
	err := e.app.Run(t.Context(), []string{"--force"})
	if !isUserError(err) || e.execd != nil {
		t.Fatalf("want a refusal, got %v (exec %v)", err, e.execd)
	}
	for _, want := range []string{"no provider available", "zz-a over quota until", "zz-missing not on PATH", "earliest back at " + until.Format("2006-01-02T15:04")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
	// --agent still starts a session.
	if err := e.app.Run(t.Context(), []string{"--force", "--agent", "zz-a"}); err != nil || e.execd[0] != "zz-a" {
		t.Fatalf("--agent must bypass the planner: %v %v", err, e.execd)
	}
}

func TestBareKiWithoutPlannerRoleIsUnchanged(t *testing.T) {
	tests := []struct {
		name, machine string
		kiAgent       string
		want          string
	}{
		{"no machine file", "-", "", "claude"},
		{"roles without a planner", "identities: [work]\nroles:\n  execution:\n    providers: [zz-a]\n", "", "claude"},
		{"KI_AGENT", "-", "zz-a", "zz-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := plannerEnv(t, tt.machine, "claude", "zz-a")
			e.vars["KI_AGENT"] = tt.kiAgent
			if err := e.app.Run(t.Context(), []string{"--force"}); err != nil {
				t.Fatal(err)
			}
			if e.execd[0] != tt.want || strings.Contains(e.stderr(), "planner") {
				t.Fatalf("argv %v, stderr %s", e.execd, e.stderr())
			}
		})
	}
}

func TestPositionalAgentShorthand(t *testing.T) {
	t.Run("equals --agent and passes args through", func(t *testing.T) {
		e := plannerEnv(t, plannerMachineYAML, "codex", "zz-a")
		if err := e.app.Run(t.Context(), []string{"codex", "--force", "--mode", "auto", "--", "--agent", "x"}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"codex", "--approve-for-me", "-c", "sandbox_workspace_write.network_access=true", "--agent", "x"}; !slices.Equal(e.execd, want) {
			t.Fatalf("argv %v, want %v", e.execd, want)
		}
		if strings.Contains(e.stderr(), "planner") {
			t.Fatalf("a named agent bypasses the planner: %s", e.stderr())
		}
	})
	t.Run("beats KI_AGENT", func(t *testing.T) {
		e := plannerEnv(t, "-", "codex", "zz-a")
		e.vars["KI_AGENT"] = "zz-a"
		if err := e.app.Run(t.Context(), []string{"codex", "--force"}); err != nil || e.execd[0] != "codex" {
			t.Fatalf("%v %v", err, e.execd)
		}
	})
	t.Run("with --agent is refused", func(t *testing.T) {
		e := plannerEnv(t, "-", "codex")
		err := e.app.Run(t.Context(), []string{"codex", "--agent", "codex", "--force"})
		if !isUserError(err) || !strings.Contains(err.Error(), "both name the agent") || e.execd != nil {
			t.Fatalf("got %v (exec %v)", err, e.execd)
		}
	})
	t.Run("only as the first argument", func(t *testing.T) {
		e := plannerEnv(t, "-", "claude")
		if err := e.app.Run(t.Context(), []string{"--force", "--", "codex"}); err != nil || e.execd[0] != "claude" || e.execd[len(e.execd)-1] != "codex" {
			t.Fatalf("%v %v", err, e.execd)
		}
	})
}

func TestUnknownLeadingWord(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"agents", []string{"agents"}, []string{`unknown command or agent "agents"`, "agents: claude, codex, agy, cursor-agent, agent", "did you mean `ki agent`?", "after `--`"}},
		{"command typo", []string{"delegat"}, []string{"did you mean `ki delegate`?"}},
		{"nothing close", []string{"zzzzzzzz"}, []string{`unknown command or agent "zzzzzzzz"`, "after `--`"}},
		{"empty word", []string{""}, []string{`unknown command or agent ""`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := plannerEnv(t, "-", "claude")
			err := e.app.Run(t.Context(), tt.args)
			if !isUserError(err) || e.execd != nil {
				t.Fatalf("want a refusal, got %v (exec %v)", err, e.execd)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal lacks %q: %v", want, err)
				}
			}
			if tt.name == "nothing close" && strings.Contains(err.Error(), "did you mean") {
				t.Errorf("suggested something for a distant word: %v", err)
			}
		})
	}
}

func TestLeadingWordPassThrough(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantAgent string
		wantTail  []string
	}{
		{"bare ki", []string{"--force"}, "claude", nil},
		{"claude", []string{"claude", "--force"}, "claude", nil},
		{"codex with a prompt after the separator", []string{"codex", "--force", "--", "prompt"}, "codex", []string{"prompt"}},
		{"agent is Cursor", []string{"agent", "--force"}, "agent", nil},
		{"agent with args", []string{"agent", "--force", "--", "--resume"}, "agent", []string{"--resume"}},
		{"a flag value is not a command", []string{"--force", "--mode", "ask", "--brain", "agents", "--", "fix the tests"}, "claude", []string{"fix the tests"}},
		{"a prompt after flags", []string{"--force", "fix the tests"}, "claude", []string{"fix the tests"}},
		{"codex resume flag", []string{"codex", "--force", "--resume"}, "codex", []string{"--resume"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := plannerEnv(t, "-", "claude", "codex", "agent")
			if err := e.app.Run(t.Context(), tt.args); err != nil {
				t.Fatal(err)
			}
			if len(e.execd) == 0 || e.execd[0] != tt.wantAgent {
				t.Fatalf("argv %v, want agent %s", e.execd, tt.wantAgent)
			}
			if len(tt.wantTail) > 0 && !slices.Equal(e.execd[len(e.execd)-len(tt.wantTail):], tt.wantTail) {
				t.Fatalf("argv %v, want tail %v", e.execd, tt.wantTail)
			}
		})
	}
}

func TestEditDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{{"", "", 0}, {"agents", "agent", 1}, {"kitten", "sitting", 3}, {"abc", "", 3}} {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestStatusAndDoctorShowThePlanner(t *testing.T) {
	e := plannerEnv(t, plannerMachineYAML, "zz-b")
	if err := e.app.Run(t.Context(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Planner struct {
			Agent   string        `json:"agent"`
			Model   string        `json:"model"`
			Source  string        `json:"source"`
			Skipped []ki.SkipNote `json:"skipped"`
			Error   string        `json:"error"`
		} `json:"planner"`
		Roles map[string]json.RawMessage `json:"roles"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, e.out)
	}
	p := got.Planner
	if p.Agent != "zz-b" || p.Model != "policy-model" || p.Source != ki.SourcePlanner || len(p.Skipped) != 2 || p.Skipped[0].Provider != "zz-a:a-model" || p.Skipped[0].Reason != "not on PATH" {
		t.Fatalf("planner %+v", p)
	}
	if _, ok := got.Roles[ki.RolePlanner]; !ok {
		t.Fatalf("roles lack planner: %s", e.out)
	}

	e.out.Reset()
	_ = e.app.Run(t.Context(), []string{"doctor"}) // other checks may fail on CI
	if !strings.Contains(e.out.String(), "planner") || !strings.Contains(e.out.String(), "zz-b:policy-model via roles.planner; zz-a:a-model not on PATH; zz-c not on PATH") {
		t.Fatalf("doctor lacks the planner:\n%s", e.out)
	}
}

func TestStatusPlannerReportsARefusalAndTheDefault(t *testing.T) {
	e := plannerEnv(t, "identities: [work]\nroles:\n  planner:\n    providers: [zz-missing]\n")
	if err := e.app.Run(t.Context(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), `"error": "roles.planner: no provider available (zz-missing not on PATH)`) {
		t.Fatalf("status:\n%s", e.out)
	}

	e = plannerEnv(t, "-")
	if err := e.app.Run(t.Context(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), `"source": "default"`) || !strings.Contains(e.out.String(), `"agent": "claude"`) {
		t.Fatalf("status:\n%s", e.out)
	}
}

func TestPlannerRoleSurvivesSetupMachine(t *testing.T) {
	e := plannerEnv(t, plannerMachineYAML)
	if err := e.app.Run(t.Context(), []string{"setup", "machine", "work"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"))
	if err != nil || !strings.Contains(string(data), "  planner:\n    strategy: ordered\n    providers:\n      - zz-a:a-model\n") {
		t.Fatalf("%v\n%s", err, data)
	}
}

func TestDelegateRefusesThePlannerRole(t *testing.T) {
	e := plannerEnv(t, plannerMachineYAML, "zz-a")
	m, err := ki.LoadPolicy(e.app.machinePath())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.app.providers(t.Context(), delegateArgs{Role: ki.RolePlanner}, m, "j-test")
	if !isUserError(err) || !strings.Contains(err.Error(), "not a headless worker") {
		t.Fatalf("got %v", err)
	}
}
