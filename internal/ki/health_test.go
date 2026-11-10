package ki

import (
	"strings"
	"testing"
	"time"
)

var healthNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// ranJob is a job of the agent that ended `ago` before healthNow after running for ten minutes.
func ranJob(agent, model, status string, ago time.Duration) Result {
	d := 10 * time.Minute
	return Result{Provider: agent, ModelRequested: model, Status: status, Started: healthNow.Add(-ago - d), DurationS: d.Seconds()}
}

func timedOut(agent, model string, ago time.Duration) Result {
	r := ranJob(agent, model, JobFailed, ago)
	r.TimedOut = true
	return r
}

func pickWith(ledger ...Result) PickInput {
	return PickInput{Role: "execution", R: Role{Strategy: StrategyOrdered, Providers: []Provider{
		{Agent: "agent", Weight: 1}, {Agent: "claude", Model: "sonnet", Weight: 1},
	}}, Ledger: ledger, Now: healthNow, Installed: func(string) bool { return true }}
}

func names(ps []Provider) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return strings.Join(out, ",")
}

func TestAStalledJobParksItsProviderForTheCooldown(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		ledger  []Result
		try     string
		skipped string
	}{
		"stalled a moment ago": {[]Result{ranJob("agent", "", JobStalled, 3*time.Minute)}, "claude:sonnet", "stalled 3m ago, cooling down for 30m"},
		"timed out":            {[]Result{timedOut("agent", "", 5*time.Minute)}, "claude:sonnet", "timed out 5m ago, cooling down for 30m"},
		"cooldown over":        {[]Result{ranJob("agent", "", JobStalled, 31*time.Minute)}, "agent,claude:sonnet", ""},
		"a later success":      {[]Result{ranJob("agent", "", JobStalled, 20*time.Minute), ranJob("agent", "", JobDone, 2*time.Minute)}, "agent,claude:sonnet", ""},
		"a later failure":      {[]Result{ranJob("agent", "", JobStalled, 20*time.Minute), ranJob("agent", "", JobFailed, 2*time.Minute)}, "agent,claude:sonnet", ""},
		"a launch failure in between does not clear it": {[]Result{ranJob("agent", "", JobStalled, 20*time.Minute), ranJob("agent", "", JobLaunchFailed, 20*time.Minute)}, "claude:sonnet", "stalled"},
		"another provider's stall":                      {[]Result{ranJob("claude", "haiku", JobStalled, time.Minute)}, "agent,claude:sonnet", ""},
		"a plain failure is not a hang":                 {[]Result{ranJob("agent", "", JobFailed, time.Minute)}, "agent,claude:sonnet", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			try, skipped := Candidates(pickWith(tc.ledger...))
			if got := names(try); got != tc.try {
				t.Fatalf("try %q, want %q (skipped %+v)", got, tc.try, skipped)
			}
			if tc.skipped == "" {
				if len(skipped) != 0 {
					t.Fatalf("skipped %+v", skipped)
				}
				return
			}
			if len(skipped) != 1 || skipped[0].Provider.Agent != "agent" || !strings.Contains(skipped[0].Reason, tc.skipped) || skipped[0].Demoted {
				t.Fatalf("skipped %+v, want a reason with %q", skipped, tc.skipped)
			}
		})
	}
}

func TestTwoHungJobsInADayDemoteUntilSuccess(t *testing.T) {
	t.Parallel()
	hungTwice := []Result{ranJob("agent", "", JobStalled, 5*time.Hour), timedOut("agent", "", 2*time.Hour)}
	for name, tc := range map[string]struct {
		ledger   []Result
		try      string
		demoted  bool
		reasonIn string
	}{
		"two hung jobs":                    {hungTwice, "claude:sonnet,agent", true, "2 jobs hung in the last 24h"},
		"one hung job":                     {hungTwice[:1], "agent,claude:sonnet", false, ""},
		"a success after both clears it":   {append(append([]Result{}, hungTwice...), ranJob("agent", "", JobPR, time.Hour)), "agent,claude:sonnet", false, ""},
		"a success before both keeps it":   {append([]Result{ranJob("agent", "", JobDone, 9*time.Hour)}, hungTwice...), "claude:sonnet,agent", true, ""},
		"a failure in between keeps it":    {append(append([]Result{}, hungTwice...), ranJob("agent", "", JobFailed, time.Hour)), "claude:sonnet,agent", true, ""},
		"older than a day does not count":  {[]Result{ranJob("agent", "", JobStalled, 30*time.Hour), ranJob("agent", "", JobStalled, 25*time.Hour)}, "agent,claude:sonnet", false, ""},
		"another model of the agent":       {[]Result{ranJob("agent", "x", JobStalled, 5*time.Hour), ranJob("agent", "x", JobStalled, 3*time.Hour)}, "claude:sonnet,agent", true, ""},
		"claude hung on a different model": {[]Result{ranJob("claude", "haiku", JobStalled, 5*time.Hour), ranJob("claude", "haiku", JobStalled, 3*time.Hour)}, "agent,claude:sonnet", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			try, skipped := Candidates(pickWith(tc.ledger...))
			if got := names(try); got != tc.try {
				t.Fatalf("try %q, want %q (skipped %+v)", got, tc.try, skipped)
			}
			if got := len(skipped) == 1 && skipped[0].Demoted; got != tc.demoted {
				t.Fatalf("demoted %v, skipped %+v", got, skipped)
			}
			if tc.reasonIn != "" && !strings.Contains(skipped[0].Reason, tc.reasonIn) {
				t.Fatalf("reason %q lacks %q", skipped[0].Reason, tc.reasonIn)
			}
		})
	}
}

func TestADemotedProviderIsStillTriedWhenItIsTheOnlyOne(t *testing.T) {
	t.Parallel()
	in := pickWith(ranJob("agent", "", JobStalled, 5*time.Hour), timedOut("agent", "", 2*time.Hour))
	in.R.Providers = in.R.Providers[:1]
	if try, skipped := Candidates(in); names(try) != "agent" || len(skipped) != 1 || !skipped[0].Demoted {
		t.Fatalf("try %s skipped %+v", names(try), skipped)
	}
}

func TestRoundRobinKeepsADemotedProviderLast(t *testing.T) {
	t.Parallel()
	in := pickWith(ranJob("agent", "", JobStalled, 5*time.Hour), timedOut("agent", "", 2*time.Hour))
	in.R.Strategy = StrategyRoundRobin
	in.Ledger = append(in.Ledger, Result{Role: "execution", Provider: "claude", ModelRequested: "sonnet", Status: JobDone, Started: healthNow.Add(-time.Hour)})
	// one dispatched job puts the turn on claude; the next lands on the demoted agent, which still goes last
	if try, _ := Candidates(in); names(try) != "claude:sonnet,agent" {
		t.Fatalf("try %s", names(try))
	}
}

func TestHealthReportsOutcomesMedianAndNextPick(t *testing.T) {
	t.Parallel()
	slow := ranJob("agent", "", JobDone, 3*time.Hour)
	slow.DurationS = 1800
	ledger := []Result{
		slow,
		ranJob("agent", "", JobStalled, 2*time.Hour),
		timedOut("agent", "", 20*time.Minute),
		ranJob("agent", "", JobLaunchFailed, 15*time.Minute),
		ranJob("claude", "sonnet", JobPR, 4*time.Hour),
		ranJob("claude", "sonnet", JobPR, time.Hour),
		ranJob("codex", "gpt-5-mini", JobFailed, 8*24*time.Hour),
		ranJob("codex", "gpt-5-mini", JobDone, time.Hour),
	}
	m := &Machine{Roles: map[string]Role{"execution": pickWith().R}}
	providers := HealthProviders(m, ledger, healthNow.Add(-HealthSince))
	if got := names(providers); got != "agent,claude:sonnet,codex:gpt-5-mini" {
		t.Fatalf("providers %s", got)
	}
	got := map[string]ProviderHealth{}
	for _, h := range Health(pickWith(ledger...), providers) {
		got[h.Provider] = h
	}
	a := got["agent"]
	if a.Jobs != 4 || a.Outcomes[JobDone] != 1 || a.Outcomes[JobStalled] != 1 || a.Outcomes[JobFailed] != 1 || a.Outcomes[JobLaunchFailed] != 1 {
		t.Fatalf("agent %+v", a)
	}
	if a.MedianS != 600 {
		t.Fatalf("median of 1800, 600, 600 is 600, got %v", a.MedianS)
	}
	if !strings.Contains(a.Skip, "timed out") && !strings.Contains(a.Skip, "stalled") {
		t.Fatalf("agent is cooling down or launch-tripped: %+v", a)
	}
	if !strings.Contains(a.Demoted, "2 jobs hung") {
		t.Fatalf("agent hung twice: %+v", a)
	}
	if c := got["claude:sonnet"]; c.Jobs != 2 || c.Outcomes[JobPR] != 2 || c.Skip != "" || c.Demoted != "" {
		t.Fatalf("claude %+v", c)
	}
	if x := got["codex:gpt-5-mini"]; x.Jobs != 2 || x.MedianS != 600 {
		t.Fatalf("codex has no role entry, so it is listed from the ledger: %+v", x)
	}
}

func TestHealthListsAProviderNoRoleCoversOnlyWhenRecent(t *testing.T) {
	t.Parallel()
	ledger := []Result{ranJob("agy", "", JobDone, 9*24*time.Hour), ranJob("agy", "", JobDone, time.Hour)}
	if got := names(HealthProviders(nil, ledger, healthNow.Add(-HealthSince))); got != "agy" {
		t.Fatalf("recent job lists the agent: %q", got)
	}
	if got := HealthProviders(nil, ledger[:1], healthNow.Add(-HealthSince)); len(got) != 0 {
		t.Fatalf("old job does not: %v", got)
	}
}

func TestMedianOfNothingIsZero(t *testing.T) {
	t.Parallel()
	if median(nil) != 0 || median([]float64{3, 1}) != 2 || median([]float64{9, 1, 5}) != 5 {
		t.Fatal("median")
	}
}
