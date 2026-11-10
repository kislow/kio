package ki

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// A role is a class of work with a list of providers that may run it, so a
// planner names the work (`ki delegate --role execution`) and the machine
// decides which vendor gets it. Only two strategies exist: ordered keeps
// the first provider busy and the rest as failover; round-robin takes turns,
// weighted. Failover is not a strategy: every pick skips a provider that is
// missing, over quota, recently failed to launch, or over its daily cap.
const (
	StrategyOrdered    = "ordered"
	StrategyRoundRobin = "round-robin"
)

// BreakerCooldown is how long a provider is skipped after a LAUNCH_FAILED.
const BreakerCooldown = 10 * time.Minute

// Provider is one entry of a role: an agent, optionally its model, its share
// of round-robin turns and a cap on jobs per day (0 is none).
type Provider struct {
	Agent     string
	Model     string
	Weight    int
	DailyJobs int
}

// Name is the agent and model as written in machine.yaml: codex:gpt-5-mini.
func (p Provider) Name() string {
	if p.Model == "" {
		return p.Agent
	}
	return p.Agent + ":" + p.Model
}

// Role is a strategy over providers.
type Role struct {
	Strategy  string
	Providers []Provider
}

type rawRole struct {
	Strategy  string        `yaml:"strategy"`
	Providers []rawProvider `yaml:"providers"`
}

// rawProvider accepts the short form `agent:model` and the long form
// {agent, model, weight, daily_jobs}.
type rawProvider struct {
	Agent     string `yaml:"agent"`
	Model     string `yaml:"model"`
	Weight    int    `yaml:"weight"`
	DailyJobs int    `yaml:"daily_jobs"`
}

func (p *rawProvider) UnmarshalYAML(data []byte) error {
	var short string
	if err := yaml.Unmarshal(data, &short); err == nil {
		p.Agent, p.Model, _ = strings.Cut(strings.TrimSpace(short), ":")
		return nil
	}
	type long rawProvider // no UnmarshalYAML, so no recursion
	var l long
	if err := yaml.UnmarshalWithOptions(data, &l, yaml.Strict()); err != nil {
		return fmt.Errorf("a provider is `agent:model` or {agent, model, weight, daily_jobs}: %w", err)
	}
	*p = rawProvider(l)
	return nil
}

// parseRoles validates the roles block of the machine file.
func parseRoles(path string, raw map[string]rawRole) (map[string]Role, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	roles := make(map[string]Role, len(raw))
	for name, rr := range raw {
		if !isRole(name) {
			return nil, fmt.Errorf("%s: roles.%s: unknown role (%s)", path, name, roleList)
		}
		r := Role{Strategy: rr.Strategy}
		switch r.Strategy {
		case "":
			r.Strategy = StrategyOrdered
		case StrategyOrdered, StrategyRoundRobin:
		default:
			return nil, fmt.Errorf("%s: roles.%s.strategy: %q is not ordered or round-robin", path, name, rr.Strategy)
		}
		if len(rr.Providers) == 0 {
			return nil, fmt.Errorf("%s: roles.%s: providers must list at least one agent", path, name)
		}
		seen := map[string]bool{}
		for i, rp := range rr.Providers {
			p := Provider(rp)
			if p.Agent == "" || strings.ContainsAny(p.Agent, " /:") {
				return nil, fmt.Errorf("%s: roles.%s.providers[%d]: %q is not an agent name", path, name, i, p.Agent)
			}
			if p.Model != "" && !modelName.MatchString(p.Model) {
				return nil, fmt.Errorf("%s: roles.%s.providers[%d]: %q is not a model name", path, name, i, p.Model)
			}
			if p.Weight < 0 || p.DailyJobs < 0 {
				return nil, fmt.Errorf("%s: roles.%s.providers[%d]: weight and daily_jobs cannot be negative", path, name, i)
			}
			if p.Weight == 0 {
				p.Weight = 1
			}
			if seen[p.Name()] {
				return nil, fmt.Errorf("%s: roles.%s: %s is listed twice", path, name, p.Name())
			}
			seen[p.Name()] = true
			r.Providers = append(r.Providers, p)
		}
		roles[name] = r
	}
	return roles, nil
}

// Skipped says why a provider was passed over in one pick. A Demoted one is
// not passed over: it stays in the list to try, moved behind the rest.
type Skipped struct {
	Provider Provider
	Reason   string
	Demoted  bool
	// Until is when the reason lapses, zero when no time is known (not on PATH).
	Until time.Time
}

// PickInput is what a pick looks at: the role, the machine's job history,
// the time, and whether an agent is on this machine. Nothing here touches
// the network: availability is what the ledger last saw.
type PickInput struct {
	Role   string
	R      Role
	Ledger []Result
	// Reserved are picks whose job has not reached the ledger yet; they take
	// their turn and count against daily caps like a recorded job.
	Reserved  []Reservation
	Now       time.Time
	Installed func(agent string) bool
}

// Candidates returns the providers to try, in order, and the ones skipped or
// demoted with the reason. The caller runs the first; on LAUNCH_FAILED it records the
// attempt and moves to the next, which is the only retry: a worker that ran
// is never re-run elsewhere.
func Candidates(in PickInput) (try []Provider, skipped []Skipped) {
	order := in.R.Providers
	if in.R.Strategy == StrategyRoundRobin {
		order = rotate(in.R.Providers, turns(in.Ledger, in.pending(), in.Role))
	}
	var late []Provider
	for _, p := range order {
		if reason, until := in.skipUntil(p); reason != "" {
			skipped = append(skipped, Skipped{Provider: p, Reason: reason, Until: until})
			continue
		}
		if reason, ok := in.demoted(p); ok {
			late = append(late, p)
			skipped = append(skipped, Skipped{Provider: p, Reason: reason, Demoted: true})
			continue
		}
		try = append(try, p)
	}
	return append(try, late...), skipped
}

func (in PickInput) skip(p Provider) string {
	reason, _ := in.skipUntil(p)
	return reason
}

// skipUntil is why the provider is skipped now, "" when it is not, and when
// that reason lapses: the quota reset, the end of the breaker or cool-down,
// the next midnight for a daily cap; zero for a missing agent.
func (in PickInput) skipUntil(p Provider) (string, time.Time) {
	if in.Installed != nil && !in.Installed(p.Agent) {
		return "not on PATH", time.Time{}
	}
	if until, over := in.overQuota(p.Agent); over {
		return "over quota until " + until.Format(time.RFC3339), until
	}
	if since, tripped := in.breaker(p.Agent); tripped {
		return "launch failed " + Age(since) + " ago", in.Now.Add(BreakerCooldown - since)
	}
	if reason, until, hung := in.cooldown(p); hung {
		return reason, until
	}
	if p.DailyJobs > 0 {
		if n := in.today(p); n >= p.DailyJobs {
			y, m, d := in.Now.Date()
			return fmt.Sprintf("daily cap of %d reached", p.DailyJobs), time.Date(y, m, d+1, 0, 0, 0, 0, in.Now.Location())
		}
	}
	return "", time.Time{}
}

// BackAt is the earliest time a skipped provider is expected back, zero when
// none of the reasons has a time (every provider missing from PATH).
func BackAt(skipped []Skipped) time.Time {
	var at time.Time
	for _, s := range skipped {
		if s.Demoted || s.Until.IsZero() {
			continue
		}
		if at.IsZero() || s.Until.Before(at) {
			at = s.Until
		}
	}
	return at
}

func (in PickInput) overQuota(agent string) (time.Time, bool) {
	var until time.Time
	for _, r := range in.Ledger {
		if r.Provider == agent && r.QuotaUntil != nil && r.QuotaUntil.After(until) {
			until = *r.QuotaUntil
		}
	}
	return until, until.After(in.Now)
}

// breaker trips when the agent's latest job is a LAUNCH_FAILED inside the
// cooldown; any later run of the agent, whatever its outcome, resets it.
func (in PickInput) breaker(agent string) (time.Duration, bool) {
	for i := len(in.Ledger) - 1; i >= 0; i-- {
		r := in.Ledger[i]
		if r.Provider != agent {
			continue
		}
		if r.Status != JobLaunchFailed {
			return 0, false
		}
		since := in.Now.Sub(r.Started)
		return since, since < BreakerCooldown
	}
	return 0, false
}

// today counts the provider's jobs that ran (launch failures are free) on
// the calendar day of Now, in Now's zone, plus its reservations still waiting
// for their job to be recorded.
func (in PickInput) today(p Provider) int {
	y, m, d := in.Now.Date()
	onToday := func(t time.Time) bool {
		ry, rm, rd := t.In(in.Now.Location()).Date()
		return ry == y && rm == m && rd == d
	}
	n := 0
	for _, r := range in.Ledger {
		if r.Provider == p.Agent && r.ModelRequested == p.Model && r.Status != JobLaunchFailed && onToday(r.Started) {
			n++
		}
	}
	for _, r := range in.pending() {
		if r.Provider == p.Agent && r.Model == p.Model && onToday(r.At) {
			n++
		}
	}
	return n
}

// pending is the reservations whose job the ledger does not hold yet. Once
// the job is recorded the ledger counts it, so it is never counted twice.
func (in PickInput) pending() []Reservation {
	recorded := make(map[string]bool, len(in.Ledger))
	for _, r := range in.Ledger {
		recorded[r.ID] = true
	}
	var out []Reservation
	for _, r := range in.Reserved {
		if !recorded[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// turns is how many jobs the role has dispatched so far, launch failures and
// reservations not yet recorded included: either one consumed a turn.
func turns(ledger []Result, reserved []Reservation, role string) int {
	n := 0
	for _, r := range ledger {
		if r.Role == role {
			n++
		}
	}
	for _, r := range reserved {
		if r.Role == role {
			n++
		}
	}
	return n
}

// rotate expands the providers into their weighted cycle and starts it at
// the given turn, so each call to Candidates begins with the next provider
// due. A provider appears once in the result, in cycle order.
func rotate(providers []Provider, turn int) []Provider {
	var cycle []Provider
	for _, p := range providers {
		for i := 0; i < p.Weight; i++ {
			cycle = append(cycle, p)
		}
	}
	if len(cycle) == 0 {
		return nil
	}
	start := turn % len(cycle)
	seen := map[string]bool{}
	out := make([]Provider, 0, len(providers))
	for i := range cycle {
		p := cycle[(start+i)%len(cycle)]
		if !seen[p.Name()] {
			seen[p.Name()] = true
			out = append(out, p)
		}
	}
	return out
}

// RoleNames lists the roles a machine defines, sorted, for messages.
func (m *Machine) RoleNames() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.Roles))
	for n := range m.Roles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RoleFor is the role by name, and whether the machine defines it.
func (m *Machine) RoleFor(name string) (Role, bool) {
	if m == nil {
		return Role{}, false
	}
	r, ok := m.Roles[name]
	return r, ok
}

// renderRoles writes the roles block the way LoadMachine reads it: the
// short form where weight and cap are defaults, the long form otherwise.
func renderRoles(b *strings.Builder, roles map[string]Role) {
	if len(roles) == 0 {
		return
	}
	b.WriteString("roles:\n")
	for _, class := range roleNames {
		r, ok := roles[class]
		if !ok {
			continue
		}
		fmt.Fprintf(b, "  %s:\n    strategy: %s\n    providers:\n", class, r.Strategy)
		for _, p := range r.Providers {
			if p.Weight == 1 && p.DailyJobs == 0 {
				fmt.Fprintf(b, "      - %s\n", p.Name())
				continue
			}
			fmt.Fprintf(b, "      - {agent: %s", p.Agent)
			if p.Model != "" {
				fmt.Fprintf(b, ", model: %s", p.Model)
			}
			if p.Weight != 1 {
				fmt.Fprintf(b, ", weight: %d", p.Weight)
			}
			if p.DailyJobs != 0 {
				fmt.Fprintf(b, ", daily_jobs: %d", p.DailyJobs)
			}
			b.WriteString("}\n")
		}
	}
}
