package ki

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func loadRoles(t *testing.T, content string) (*Machine, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(p, []byte("identities: [personal]\n"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadMachine(p, map[string]struct{}{"personal": {}})
}

func TestLoadMachineRoles(t *testing.T) {
	t.Parallel()
	m, err := loadRoles(t, `roles:
  execution:
    strategy: round-robin
    providers:
      - codex:gpt-5-mini
      - {agent: claude, model: sonnet, weight: 2, daily_jobs: 20}
      - agy
  search:
    providers: [claude:haiku]
  writing:
    providers: [claude:haiku, agent]
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Role{
		"execution": {Strategy: StrategyRoundRobin, Providers: []Provider{
			{Agent: "codex", Model: "gpt-5-mini", Weight: 1},
			{Agent: "claude", Model: "sonnet", Weight: 2, DailyJobs: 20},
			{Agent: "agy", Weight: 1},
		}},
		"search":  {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "claude", Model: "haiku", Weight: 1}}},
		"writing": {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "claude", Model: "haiku", Weight: 1}, {Agent: "agent", Weight: 1}}},
	}
	if !reflect.DeepEqual(m.Roles, want) {
		t.Fatalf("got %+v", m.Roles)
	}
	if names := m.RoleNames(); !reflect.DeepEqual(names, []string{"execution", "search", "writing"}) {
		t.Fatalf("names %v", names)
	}
	if r, ok := m.RoleFor("execution"); !ok || r.Providers[1].Name() != "claude:sonnet" || m.Roles["execution"].Providers[2].Name() != "agy" {
		t.Fatalf("RoleFor: %v %+v", ok, r)
	}
	var none *Machine
	if _, ok := none.RoleFor("execution"); ok || none.RoleNames() != nil {
		t.Fatal("no file means no roles")
	}
	for name, content := range map[string]string{
		"unknown role":    "roles:\n  thinking: {providers: [claude]}\n",
		"bad strategy":    "roles:\n  execution: {strategy: cheapest, providers: [claude]}\n",
		"no providers":    "roles:\n  execution: {strategy: ordered}\n",
		"empty providers": "roles:\n  execution: {providers: []}\n",
		"bad agent":       "roles:\n  execution: {providers: ['bad agent:x']}\n",
		"bad model":       "roles:\n  execution: {providers: [{agent: claude, model: 'has space'}]}\n",
		"negative":        "roles:\n  execution: {providers: [{agent: claude, weight: -1}]}\n",
		"duplicate":       "roles:\n  execution: {providers: [claude:sonnet, claude:sonnet]}\n",
		"unknown key":     "roles:\n  execution: {providers: [{agent: claude, cap: 3}]}\n",
	} {
		if _, err := loadRoles(t, content); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestRenderMachineRolesRoundTrip(t *testing.T) {
	t.Parallel()
	m := Machine{Identities: []string{"personal"}, Roles: map[string]Role{
		"execution": {Strategy: StrategyRoundRobin, Providers: []Provider{
			{Agent: "codex", Model: "gpt-5-mini", Weight: 1},
			{Agent: "claude", Model: "sonnet", Weight: 3, DailyJobs: 20},
			{Agent: "agy", Weight: 1, DailyJobs: 5},
		}},
		"writing": {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "claude", Model: "haiku", Weight: 1}}},
	}}
	content := RenderMachine(m)
	p := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMachine(p, map[string]struct{}{"personal": {}})
	if err != nil {
		t.Fatalf("%v\n%s", err, content)
	}
	if !reflect.DeepEqual(got.Roles, m.Roles) {
		t.Fatalf("got %+v\n%s", got.Roles, content)
	}
	if !strings.Contains(content, "      - codex:gpt-5-mini\n") || !strings.Contains(content, "{agent: claude, model: sonnet, weight: 3, daily_jobs: 20}") {
		t.Fatalf("render:\n%s", content)
	}
	example := RenderMachine(Machine{Identities: []string{"personal"}})
	if !strings.Contains(example, "# roles:") || !strings.Contains(example, "#   writing:") {
		t.Fatalf("without roles the block is a commented example naming every class:\n%s", example)
	}
	if !strings.Contains(content, "  writing:\n    strategy: ordered\n    providers:\n      - claude:haiku\n") {
		t.Fatalf("writing role not rendered:\n%s", content)
	}
}

func TestCandidates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	codex := Provider{Agent: "codex", Model: "gpt-5-mini", Weight: 1}
	claude := Provider{Agent: "claude", Model: "sonnet", Weight: 1}
	agy := Provider{Agent: "agy", Weight: 1}
	quotaUntil := now.Add(147*time.Hour + 25*time.Minute + 10*time.Second)
	expiredQuota := now.Add(-time.Second)
	all := func(string) bool { return true }
	job := func(agent, model, status string, ago time.Duration, role string) Result {
		return Result{Provider: agent, Model: "reported-" + model, ModelRequested: model, Status: status, Started: now.Add(-ago), Role: role}
	}
	names := func(ps []Provider) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.Name())
		}
		return out
	}
	for name, tc := range map[string]struct {
		role      Role
		ledger    []Result
		installed func(string) bool
		want      []string
		skipped   []string
	}{
		"ordered picks the first":  {Role{StrategyOrdered, []Provider{codex, claude, agy}}, nil, all, []string{"codex:gpt-5-mini", "claude:sonnet", "agy"}, nil},
		"not installed is skipped": {Role{StrategyOrdered, []Provider{codex, claude}}, nil, func(a string) bool { return a != "codex" }, []string{"claude:sonnet"}, []string{"codex:gpt-5-mini: not on PATH"}},
		"breaker trips on a fresh launch failure": {
			Role{StrategyOrdered, []Provider{codex, claude}},
			[]Result{job("codex", "gpt-5-mini", JobLaunchFailed, 3*time.Minute, "")},
			all,
			[]string{"claude:sonnet"},
			[]string{"codex:gpt-5-mini: launch failed 3m ago"},
		},
		"breaker resets after the cooldown": {
			Role{StrategyOrdered, []Provider{codex, claude}},
			[]Result{job("codex", "gpt-5-mini", JobLaunchFailed, 11*time.Minute, "")},
			all,
			[]string{"codex:gpt-5-mini", "claude:sonnet"},
			nil,
		},
		"quota skips until its reset time": {
			Role{StrategyOrdered, []Provider{agy, claude}},
			[]Result{{Provider: "agy", Status: JobFailed, Started: now, QuotaUntil: &quotaUntil}},
			all,
			[]string{"claude:sonnet"},
			[]string{"agy: over quota until " + quotaUntil.Format(time.RFC3339)},
		},
		"quota skip expires at reset time": {
			Role{StrategyOrdered, []Provider{agy, claude}},
			[]Result{{Provider: "agy", Status: JobFailed, Started: now, QuotaUntil: &expiredQuota}},
			all,
			[]string{"agy", "claude:sonnet"},
			nil,
		},
		"breaker resets on a later run": {
			Role{StrategyOrdered, []Provider{codex, claude}},
			[]Result{job("codex", "gpt-5-mini", JobLaunchFailed, 3*time.Minute, ""), job("codex", "other", JobFailed, time.Minute, "")},
			all,
			[]string{"codex:gpt-5-mini", "claude:sonnet"},
			nil,
		},
		"daily cap counts runs, not launch failures": {
			Role{StrategyOrdered, []Provider{{Agent: "codex", Model: "gpt-5-mini", Weight: 1, DailyJobs: 2}, claude}},
			[]Result{job("codex", "gpt-5-mini", JobDone, 2*time.Hour, ""), job("codex", "gpt-5-mini", JobLaunchFailed, 90*time.Minute, ""), job("codex", "gpt-5-mini", JobBlocked, time.Hour, ""), job("codex", "gpt-5-mini", JobDone, 30*time.Hour, "")},
			all,
			[]string{"claude:sonnet"},
			[]string{"codex:gpt-5-mini: daily cap of 2 reached"},
		},
		"daily cap is per model": {
			Role{StrategyOrdered, []Provider{{Agent: "codex", Model: "gpt-5-mini", Weight: 1, DailyJobs: 1}, claude}},
			[]Result{job("codex", "gpt-5", JobDone, time.Hour, "")},
			all,
			[]string{"codex:gpt-5-mini", "claude:sonnet"},
			nil,
		},
		"round-robin takes turns": {
			Role{StrategyRoundRobin, []Provider{codex, claude, agy}},
			[]Result{job("codex", "gpt-5-mini", JobDone, time.Hour, "execution")},
			all,
			[]string{"claude:sonnet", "agy", "codex:gpt-5-mini"},
			nil,
		},
		"round-robin ignores other roles' jobs": {
			Role{StrategyRoundRobin, []Provider{codex, claude}},
			[]Result{job("codex", "gpt-5-mini", JobDone, time.Hour, "search"), job("codex", "gpt-5-mini", JobDone, time.Hour, "")},
			all,
			[]string{"codex:gpt-5-mini", "claude:sonnet"},
			nil,
		},
		"round-robin weights": {
			Role{StrategyRoundRobin, []Provider{{Agent: "codex", Model: "gpt-5-mini", Weight: 2}, claude}},
			[]Result{job("codex", "gpt-5-mini", JobDone, 3*time.Hour, "execution")},
			all,
			[]string{"codex:gpt-5-mini", "claude:sonnet"},
			nil,
		},
		"round-robin weights, third turn": {
			Role{StrategyRoundRobin, []Provider{{Agent: "codex", Model: "gpt-5-mini", Weight: 2}, claude}},
			[]Result{job("codex", "gpt-5-mini", JobDone, 3*time.Hour, "execution"), job("codex", "gpt-5-mini", JobDone, 2*time.Hour, "execution")},
			all,
			[]string{"claude:sonnet", "codex:gpt-5-mini"},
			nil,
		},
		"round-robin skips the tripped one": {
			Role{StrategyRoundRobin, []Provider{codex, claude}},
			[]Result{job("codex", "gpt-5-mini", JobLaunchFailed, time.Minute, "execution")},
			all,
			[]string{"claude:sonnet"},
			[]string{"codex:gpt-5-mini: launch failed 1m ago"},
		},
		"nothing left": {Role{StrategyOrdered, []Provider{codex}}, nil, func(string) bool { return false }, []string{}, []string{"codex:gpt-5-mini: not on PATH"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			try, skipped := Candidates(PickInput{Role: "execution", R: tc.role, Ledger: tc.ledger, Now: now, Installed: tc.installed})
			gotSkipped := []string{}
			for _, s := range skipped {
				gotSkipped = append(gotSkipped, s.Provider.Name()+": "+s.Reason)
			}
			if !reflect.DeepEqual(names(try), tc.want) {
				t.Errorf("try %v, want %v", names(try), tc.want)
			}
			if tc.skipped == nil {
				tc.skipped = []string{}
			}
			if !reflect.DeepEqual(gotSkipped, tc.skipped) {
				t.Errorf("skipped %v, want %v", gotSkipped, tc.skipped)
			}
		})
	}
}
