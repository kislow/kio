package ki

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadMachine(t *testing.T) {
	t.Parallel()
	known := map[string]struct{}{"personal": {}, "work": {}}
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name, content, wantErr string
		carries                map[string]bool
	}{
		{"one identity", "identities: [personal]\n", "", map[string]bool{"personal": true, "work": false}},
		{"two identities", "identities:\n  - work\n  - personal\n", "", map[string]bool{"personal": true, "work": true}},
		{"unknown identity", "identities: [client]\n", `identity "client" is not in projects.yaml`, nil},
		{"duplicate", "identities: [personal, personal]\n", "listed twice", nil},
		{"empty", "identities: []\n", "at least one", nil},
		{"typo key", "identity: [personal]\n", "identity", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := LoadMachine(write(strings.ReplaceAll(tc.name, " ", "-")+".yaml", tc.content), known)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for id, want := range tc.carries {
				if m.Carries(id) != want {
					t.Errorf("Carries(%q) = %v, want %v", id, !want, want)
				}
			}
		})
	}
}

func TestMachineAbsentCarriesEverything(t *testing.T) {
	t.Parallel()
	m, err := LoadMachine(filepath.Join(t.TempDir(), "machine.yaml"), map[string]struct{}{"personal": {}})
	if err != nil || m != nil {
		t.Fatalf("missing file must mean no restriction: %v %v", m, err)
	}
	if !m.Carries("anything") {
		t.Fatal("a nil machine carries every identity")
	}
}

func TestRenderMachineRoundTrips(t *testing.T) {
	t.Parallel()
	content := RenderMachine(Machine{Identities: []string{"work", "personal"}})
	p := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMachine(p, map[string]struct{}{"personal": {}, "work": {}})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Carries("personal") || !m.Carries("work") || !strings.HasPrefix(content, "# Identities") {
		t.Fatalf("got %q", content)
	}
}

func TestLoadMachineModelPolicy(t *testing.T) {
	t.Parallel()
	known := map[string]struct{}{"personal": {}}
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	m, err := LoadMachine(write("ok.yaml", "identities: [personal]\nmodels:\n  judgement: {claude: opus, codex: gpt-5}\n  execution: {claude: sonnet}\n  writing: {claude: haiku}\neffort:\n  execution: medium\n  writing: low\n"), known)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ class, agent, want string }{
		{"judgement", "claude", "opus"},
		{"judgement", "codex", "gpt-5"},
		{"execution", "claude", "sonnet"},
		{"execution", "codex", ""},
		{"search", "claude", ""},
		{"writing", "claude", "haiku"},
		{"judgement", "cursor", ""},
	} {
		if got := m.ModelFor(tc.class, tc.agent); got != tc.want {
			t.Errorf("ModelFor(%s, %s) = %q, want %q", tc.class, tc.agent, got, tc.want)
		}
	}
	if m.EffortFor("execution") != "medium" || m.EffortFor("writing") != "low" || m.EffortFor("judgement") != "" {
		t.Fatalf("effort: %+v", m.Effort)
	}
	var nilMachine *Machine
	if nilMachine.ModelFor("judgement", "claude") != "" || nilMachine.EffortFor("execution") != "" {
		t.Fatal("no file means no policy")
	}
	for name, content := range map[string]string{
		"bad-class":  "identities: [personal]\nmodels:\n  thinking: {claude: opus}\n",
		"bad-effort": "identities: [personal]\neffort:\n  execution: extreme\n",
		"bad-model":  "identities: [personal]\nmodels:\n  search: {claude: 'has space'}\n",
	} {
		if _, err := LoadMachine(write(name+".yaml", content), known); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestLoadPolicyIgnoresUnknownIdentities(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	known := map[string]struct{}{"personal": {}}
	stale := write("stale.yaml", "identities: [retired]\nmodels:\n  execution: {claude: haiku}\neffort:\n  execution: medium\nroles:\n  execution:\n    strategy: ordered\n    providers: [codex:gpt-5-mini]\n")
	if _, err := LoadMachine(stale, known); err == nil || !strings.Contains(err.Error(), `"retired"`) {
		t.Fatalf("a removed identity must still fail LoadMachine: %v", err)
	}
	m, err := LoadPolicy(stale)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModelFor("execution", "claude") != "haiku" || m.EffortFor("execution") != "medium" {
		t.Fatalf("policy: %+v", m)
	}
	role, ok := m.RoleFor("execution")
	if !ok || len(role.Providers) != 1 || role.Providers[0].Name() != "codex:gpt-5-mini" {
		t.Fatalf("roles: %+v", m.Roles)
	}

	if _, err := LoadPolicy(write("bad-model.yaml", "identities: [retired]\nmodels:\n  thinking: {claude: opus}\n")); err == nil || !strings.Contains(err.Error(), "unknown class") {
		t.Fatalf("a models block that does not parse must fail: %v", err)
	}
	if _, err := LoadPolicy(write("bad-effort.yaml", "identities: [retired]\neffort:\n  execution: extreme\n")); err == nil || !strings.Contains(err.Error(), "extreme") {
		t.Fatalf("an effort block that does not parse must fail: %v", err)
	}
	missing, err := LoadPolicy(filepath.Join(dir, "absent.yaml"))
	if err != nil || missing != nil {
		t.Fatalf("missing file: %v %v", missing, err)
	}
}

func TestRenderMachineWithPolicyRoundTrips(t *testing.T) {
	t.Parallel()
	m := Machine{Identities: []string{"personal"}, Models: map[string]map[string]string{"execution": {"codex": "gpt-5-mini", "claude": "sonnet"}}, Effort: map[string]string{"execution": "medium"}}
	p := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(p, []byte(RenderMachine(m)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMachine(p, map[string]struct{}{"personal": {}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelFor("execution", "claude") != "sonnet" || got.ModelFor("execution", "codex") != "gpt-5-mini" || got.EffortFor("execution") != "medium" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(RenderMachine(Machine{Identities: []string{"personal"}}), "# models:") {
		t.Fatal("without a policy the models block is a commented example")
	}
}

func TestLoadMachineDelegateAllow(t *testing.T) {
	t.Parallel()
	known := map[string]struct{}{"personal": {}}
	tests := []struct {
		name, content string
		want          []string
		wantErr       string
	}{
		{"absent", "", nil, ""},
		{"flow list", "delegate:\n  allow: [git, gh, go, uv, make]\n", []string{"git", "gh", "go", "uv", "make"}, ""},
		{"block list keeps order", "delegate:\n  allow:\n    - make\n    - git status\n    - go\n", []string{"make", "git status", "go"}, ""},
		{"dots, dashes, underscores", "delegate:\n  allow: [golangci-lint, python3.13, my_tool, gh pr-merge]\n", []string{"golangci-lint", "python3.13", "my_tool", "gh pr-merge"}, ""},
		{"null block", "delegate:\n", nil, ""},
		{"wildcard", "delegate:\n  allow: ['*']\n", nil, `delegate.allow[0]: "*": wildcards are refused`},
		{"wildcard subcommand", "delegate:\n  allow: [git, 'git *']\n", nil, `delegate.allow[1]: "git *": wildcards are refused`},
		{"empty string", "delegate:\n  allow: ['']\n", nil, `delegate.allow[0]: "" is not a command`},
		{"two spaces", "delegate:\n  allow: ['git remote add']\n", nil, `"git remote add" is not a command`},
		{"path", "delegate:\n  allow: [/bin/rm]\n", nil, `"/bin/rm" is not a command`},
		{"shell chars", "delegate:\n  allow: ['git; rm']\n", nil, `"git; rm" is not a command`},
		{"leading dash", "delegate:\n  allow: [-rf]\n", nil, `"-rf" is not a command`},
		{"double space", "delegate:\n  allow: ['git  status']\n", nil, `is not a command`},
		{"trailing space", "delegate:\n  allow: ['git ']\n", nil, `is not a command`},
		{"duplicate", "delegate:\n  allow: [git, go, git]\n", nil, `delegate.allow: "git" is listed twice`},
		{"empty list", "delegate:\n  allow: []\n", nil, "delegate.allow must list at least one command"},
		{"no allow key", "delegate: {}\n", nil, "delegate.allow must list at least one command"},
		{"stall alone", "delegate:\n  stall: 20m\n", nil, ""},
		{"notify alone", "delegate:\n  notify_stall: true\n", nil, ""},
		{"queue alone", "delegate:\n  queue: true\n", nil, ""},
		{"queue parallel alone", "delegate:\n  queue_parallel: 3\n", nil, ""},
		{"negative queue parallel", "delegate:\n  queue_parallel: -1\n", nil, "delegate.queue_parallel: -1 cannot be negative"},
		{"bad stall", "delegate:\n  stall: soon\n", nil, `delegate.stall: "soon" is not a positive duration`},
		{"zero stall", "delegate:\n  stall: 0s\n", nil, `delegate.stall: "0s" is not a positive duration`},
		{"unknown key", "delegate:\n  allow: [git]\n  deny: [rm]\n", nil, "deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "machine.yaml")
			if err := os.WriteFile(p, []byte("identities: [personal]\n"+tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			m, err := LoadMachine(p, known)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), p) {
					t.Fatalf("want an error naming %s and containing %q, got %v", p, tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := m.DelegateAllow(); !slices.Equal(got, tt.want) {
				t.Fatalf("allow %q, want %q", got, tt.want)
			}
		})
	}
	var none *Machine
	if none.DelegateAllow() != nil {
		t.Fatal("no file means no allow-list")
	}
}

func TestRenderMachineDelegateRoundTrips(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    Machine
	}{
		{"alone", Machine{Identities: []string{"personal"}, Delegate: Delegate{Allow: []string{"git", "gh", "go", "uv", "make"}}}},
		{"with subcommands", Machine{Identities: []string{"personal"}, Delegate: Delegate{Allow: []string{"make", "git status", "go"}}}},
		{"yaml words stay strings", Machine{Identities: []string{"personal"}, Delegate: Delegate{Allow: []string{"true", "null", "1.5", "no", "0x1f", "git"}}}},
		{"with the rest of the policy", Machine{
			Identities: []string{"personal"},
			Models:     map[string]map[string]string{"execution": {"claude": "sonnet"}},
			Effort:     map[string]string{"execution": "low"},
			Roles: map[string]Role{
				"execution": {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "agy", Weight: 1}}},
				RolePlanner: {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "claude", Model: "opus", Weight: 1}, {Agent: "codex", Weight: 1, DailyJobs: 3}}},
			},
			Delegate: Delegate{Allow: []string{"git"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content := RenderMachine(tt.m)
			p := filepath.Join(t.TempDir(), "machine.yaml")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadMachine(p, map[string]struct{}{"personal": {}})
			if err != nil {
				t.Fatalf("%v\n%s", err, content)
			}
			if !slices.Equal(got.DelegateAllow(), tt.m.Delegate.Allow) || !reflect.DeepEqual(got.Roles, tt.m.Roles) || !reflect.DeepEqual(got.Models, tt.m.Models) {
				t.Fatalf("got %+v\n%s", got, content)
			}
			if !strings.Contains(content, "\ndelegate:\n  allow: [") || strings.Contains(content, "# delegate:") {
				t.Fatalf("render:\n%s", content)
			}
		})
	}
	example := RenderMachine(Machine{Identities: []string{"personal"}})
	if !strings.Contains(example, "# delegate:\n#   allow: [git, gh, go, make]\n") {
		t.Fatalf("without an allow-list the block is a commented example:\n%s", example)
	}
}

func TestMachineStallSettings(t *testing.T) {
	t.Parallel()
	known := map[string]struct{}{"personal": {}}
	load := func(content string) *Machine {
		p := filepath.Join(t.TempDir(), "machine.yaml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := LoadMachine(p, known)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	var none *Machine
	if none.StallLimit() != DefaultStall || none.NotifyStall() || none.QueueByDefault() || none.QueueParallel() != DefaultQueueParallel {
		t.Fatal("no file means the default limit, no notification and no queue by default")
	}
	if m := load("identities: [personal]\ndelegate:\n  queue: true\n  queue_parallel: 4\n"); !m.QueueByDefault() || m.QueueParallel() != 4 {
		t.Fatalf("queue %v parallel %d", m.QueueByDefault(), m.QueueParallel())
	}
	if m := load("identities: [personal]\ndelegate:\n  allow: [git]\n"); m.StallLimit() != 15*time.Minute || m.NotifyStall() {
		t.Fatalf("defaults: %v %v", m.StallLimit(), m.NotifyStall())
	}
	m := load("identities: [personal]\ndelegate:\n  allow: [git]\n  stall: 20m\n  notify_stall: true\n")
	if m.StallLimit() != 20*time.Minute || !m.NotifyStall() {
		t.Fatalf("stall %v notify %v", m.StallLimit(), m.NotifyStall())
	}
	for name, d := range map[string]Delegate{
		"with the allow-list": {Allow: []string{"git"}, Stall: 20 * time.Minute, NotifyStall: true},
		"without it":          {Stall: 45 * time.Second},
		"notify only":         {NotifyStall: true},
		"queue":               {Queue: true, QueueParallel: 3},
	} {
		got := load(RenderMachine(Machine{Identities: []string{"personal"}, Delegate: d})).Delegate
		if got.Stall != d.Stall || got.NotifyStall != d.NotifyStall || !slices.Equal(got.Allow, d.Allow) || got.Queue != d.Queue || got.QueueParallel != d.QueueParallel {
			t.Fatalf("%s: rendered and read back %+v, want %+v", name, got, d)
		}
	}
}

func TestLoadMachineRolesAcceptPlannerOnly(t *testing.T) {
	t.Parallel()
	load := func(roles string) (*Machine, error) {
		p := filepath.Join(t.TempDir(), "machine.yaml")
		if err := os.WriteFile(p, []byte("identities: [personal]\nroles:\n"+roles), 0o600); err != nil {
			t.Fatal(err)
		}
		return LoadMachine(p, map[string]struct{}{"personal": {}})
	}
	m, err := load("  planner:\n    providers: [claude:opus, agent]\n  execution:\n    providers: [codex]\n")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.RoleFor(RolePlanner)
	if !ok || r.Strategy != StrategyOrdered || len(r.Providers) != 2 || r.Providers[0].Name() != "claude:opus" {
		t.Fatalf("planner: %+v", m.Roles)
	}
	if _, err := load("  orchestrator:\n    providers: [claude]\n"); err == nil || !strings.Contains(err.Error(), "unknown role") || !strings.Contains(err.Error(), "planner") {
		t.Fatalf("an unknown role must name the valid ones: %v", err)
	}
}

func TestRoleStatesIncludePlannerWithJudgementModel(t *testing.T) {
	t.Parallel()
	m := &Machine{
		Models: map[string]map[string]string{ClassJudgement: {"codex": "gpt-5"}},
		Roles:  map[string]Role{RolePlanner: {Strategy: StrategyOrdered, Providers: []Provider{{Agent: "codex", Weight: 1}}}},
	}
	got := RoleStates(m, PickInput{Now: time.Now(), Installed: func(string) bool { return true }})
	st, ok := got[RolePlanner]
	if !ok || len(st.Providers) != 1 || st.Providers[0].Model != "gpt-5" || st.Providers[0].Health != HealthOK {
		t.Fatalf("%+v", got)
	}
}
