package ki

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func plannerMachine(strategy string, providers ...Provider) *Machine {
	for i := range providers {
		if providers[i].Weight == 0 {
			providers[i].Weight = 1
		}
	}
	return &Machine{
		Roles:  map[string]Role{RolePlanner: {Strategy: strategy, Providers: providers}},
		Models: map[string]map[string]string{ClassJudgement: {"codex": "gpt-5", "claude": "opus"}},
	}
}

func TestResolveSessionAgent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	claude, codex, agy := Provider{Agent: "claude", Model: "sonnet"}, Provider{Agent: "codex"}, Provider{Agent: "agy"}
	quotaClaude := []Result{{ID: "j1", Provider: "claude", Status: JobQuota, Started: now.Add(-time.Hour), QuotaUntil: &reset}}
	launchFailedCodex := []Result{{ID: "j2", Provider: "codex", Status: JobLaunchFailed, Started: now.Add(-time.Minute)}}
	all := func(string) bool { return true }
	tests := []struct {
		name         string
		flag, env    string
		m            *Machine
		ledger       []Result
		installed    func(string) bool
		wantAgent    string
		wantSource   string
		wantProvider string
		wantSkipped  int
		wantErr      string
	}{
		{"no planner role is claude", "", "", &Machine{}, nil, all, "claude", SourceDefault, "", 0, ""},
		{"nil machine is claude", "", "", nil, nil, all, "claude", SourceDefault, "", 0, ""},
		{"flag beats env and role", "agy", "codex", plannerMachine(StrategyOrdered, claude), nil, all, "agy", SourceFlag, "", 0, ""},
		{"env beats role", "", "codex", plannerMachine(StrategyOrdered, claude), nil, all, "codex", SourceEnv, "", 0, ""},
		{"first provider", "", "", plannerMachine(StrategyOrdered, claude, codex), nil, all, "claude", SourcePlanner, "claude:sonnet", 0, ""},
		{"over quota provider is skipped", "", "", plannerMachine(StrategyOrdered, claude, codex), quotaClaude, all, "codex", SourcePlanner, "codex", 1, ""},
		{"missing agent is skipped", "", "", plannerMachine(StrategyOrdered, claude, codex), nil, func(a string) bool { return a != "claude" }, "codex", SourcePlanner, "codex", 1, ""},
		{"breaker skips a failed launch", "", "", plannerMachine(StrategyOrdered, codex, agy), launchFailedCodex, all, "agy", SourcePlanner, "agy", 1, ""},
		{"round-robin starts at its first turn", "", "", plannerMachine(StrategyRoundRobin, claude, codex), nil, all, "claude", SourcePlanner, "claude:sonnet", 0, ""},
		{"none eligible refuses with reasons and reset", "", "", plannerMachine(StrategyOrdered, claude, agy), quotaClaude, func(a string) bool { return a != "agy" }, "", "", "", 2, "claude:sonnet over quota until 2026-11-08T12:00:00Z; agy not on PATH); earliest back at 2026-11-08T12:00:00Z"},
		{"none installed has no reset", "", "", plannerMachine(StrategyOrdered, claude), nil, func(string) bool { return false }, "", "", "", 1, "roles.planner: no provider available (claude:sonnet not on PATH); name an agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := PickInput{Ledger: tt.ledger, Now: now, Installed: tt.installed}
			got, err := ResolveSessionAgent(tt.flag, tt.env, tt.m, in)
			if tt.wantErr != "" {
				var np NoPlannerError
				if !errors.As(err, &np) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err %v, want it to contain %q", err, tt.wantErr)
				}
				if len(got.Skipped) != tt.wantSkipped {
					t.Fatalf("skipped %d, want %d", len(got.Skipped), tt.wantSkipped)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Agent != tt.wantAgent || got.Source != tt.wantSource || len(got.Skipped) != tt.wantSkipped {
				t.Fatalf("got %+v, want agent %s source %s skipped %d", got, tt.wantAgent, tt.wantSource, tt.wantSkipped)
			}
			if tt.wantProvider != "" && got.Provider.Name() != tt.wantProvider {
				t.Fatalf("provider %s, want %s", got.Provider.Name(), tt.wantProvider)
			}
		})
	}
}

func TestResolveSessionAgentReadsOnly(t *testing.T) {
	t.Parallel()
	in := PickInput{Now: time.Now(), Installed: func(string) bool { return true }, Ledger: []Result{{ID: "j", Provider: "claude", Status: JobDone, Started: time.Now()}}}
	m := plannerMachine(StrategyOrdered, Provider{Agent: "claude", DailyJobs: 1}, Provider{Agent: "codex"})
	for i := 0; i < 3; i++ {
		got, err := ResolveSessionAgent("", "", m, in)
		if err != nil || got.Agent != "codex" {
			t.Fatalf("pick %d: %+v %v", i, got, err)
		}
	}
	if len(in.Ledger) != 1 || len(in.Reserved) != 0 {
		t.Fatalf("a planner pick must not write: %+v", in)
	}
}

func TestResolveSessionAgentCountsReservationsAgainstTheCap(t *testing.T) {
	t.Parallel()
	now := time.Now()
	in := PickInput{Now: now, Installed: func(string) bool { return true }, Reserved: []Reservation{{ID: "r", Provider: "claude", At: now}}}
	got, err := ResolveSessionAgent("", "", plannerMachine(StrategyOrdered, Provider{Agent: "claude", DailyJobs: 1}, Provider{Agent: "codex"}), in)
	if err != nil || got.Agent != "codex" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSessionModel(t *testing.T) {
	t.Parallel()
	m := plannerMachine(StrategyOrdered)
	picked := SessionAgent{Agent: "claude", Source: SourcePlanner, Provider: Provider{Agent: "claude", Model: "sonnet"}}
	noModel := SessionAgent{Agent: "codex", Source: SourcePlanner, Provider: Provider{Agent: "codex"}}
	other := SessionAgent{Agent: "/usr/bin/agy", Source: SourceFlag}
	tests := []struct {
		name      string
		s         SessionAgent
		flag, env string
		m         *Machine
		want      string
	}{
		{"flag first", picked, "haiku", "gpt-5-mini", m, "haiku"},
		{"env next", picked, "", "gpt-5-mini", m, "gpt-5-mini"},
		{"provider model next", picked, "", "", m, "sonnet"},
		{"judgement policy next", noModel, "", "", m, "gpt-5"},
		{"agent default last", other, "", "", m, ""},
		{"no machine", noModel, "", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.s.Model(tt.flag, tt.env, tt.m); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitShorthand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		wantAgent string
		wantRest  []string
	}{
		{"no args", nil, "", nil},
		{"each agent", []string{"codex", "--mode", "auto"}, "codex", []string{"--mode", "auto"}},
		{"cursor-agent", []string{"cursor-agent"}, "cursor-agent", []string{}},
		{"agent is Cursor's binary", []string{"agent", "--mode", "auto"}, "agent", []string{"--mode", "auto"}},
		{"agents is a typo", []string{"agents"}, "", []string{"agents"}},
		{"agy with passthrough", []string{"agy", "--", "--flag"}, "agy", []string{"--", "--flag"}},
		{"cursor stays a subcommand", []string{"cursor"}, "", []string{"cursor"}},
		{"only as the first argument", []string{"--force", "claude"}, "", []string{"--force", "claude"}},
		{"after the separator it is an agent arg", []string{"--", "claude"}, "", []string{"--", "claude"}},
		{"unknown name", []string{"gemini"}, "", []string{"gemini"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agent, rest := SplitShorthand(tt.args)
			if agent != tt.wantAgent || !slices.Equal(rest, tt.wantRest) {
				t.Fatalf("got %q %v, want %q %v", agent, rest, tt.wantAgent, tt.wantRest)
			}
		})
	}
}

func TestParseRunArgsAgentGiven(t *testing.T) {
	t.Parallel()
	if r := ParseRunArgs([]string{"--agent", "codex"}, ""); !r.AgentGiven || r.Agent != "codex" {
		t.Fatalf("%+v", r)
	}
	if r := ParseRunArgs([]string{"--", "--agent", "codex"}, "claude"); r.AgentGiven || r.Agent != "claude" {
		t.Fatalf("agent args after -- are not ki's: %+v", r)
	}
}
