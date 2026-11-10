package ki

import (
	"fmt"
	"sort"
	"time"
)

// A job that hangs (STALLED, or killed at --timeout) says the provider is
// stuck rather than down. It parks the provider for a cool-down, and two in a
// day send it to the end of the order until a job on it ends PR or DONE.
const (
	// HangCooldown is how long a provider is skipped after a job that hung.
	HangCooldown = 30 * time.Minute
	// DemoteAfter hung jobs within DemoteWindow demote the provider.
	DemoteAfter  = 2
	DemoteWindow = 24 * time.Hour
	// HealthJobs is how many of a provider's latest jobs `ki delegate health` reads.
	HealthJobs = 20
	// HealthSince is how far back a provider seen only in the ledger is listed.
	HealthSince = 7 * 24 * time.Hour
)

func (r Result) hung() bool { return r.Status == JobStalled || r.TimedOut }

func (r Result) ended() time.Time {
	return r.Started.Add(time.Duration(r.DurationS * float64(time.Second)))
}

// ranOn reports whether the job ran on the provider: its agent, and its model
// unless the provider names none, which covers whatever the agent chose.
func ranOn(r Result, p Provider) bool {
	return r.Provider == p.Agent && (p.Model == "" || r.ModelRequested == p.Model)
}

// cooldown is set when the provider's latest job that ran hung inside
// HangCooldown; any later job that ran, whatever it returned, resets it.
func (in PickInput) cooldown(p Provider) (reason string, until time.Time, hung bool) {
	for i := len(in.Ledger) - 1; i >= 0; i-- {
		r := in.Ledger[i]
		if !ranOn(r, p) || r.Status == JobLaunchFailed {
			continue
		}
		since := in.Now.Sub(r.ended())
		if !r.hung() || since >= HangCooldown {
			return "", time.Time{}, false
		}
		what := "stalled"
		if r.Status != JobStalled {
			what = "timed out"
		}
		return fmt.Sprintf("%s %s ago, cooling down for %s", what, Age(since), Age(HangCooldown)), r.ended().Add(HangCooldown), true
	}
	return "", time.Time{}, false
}

// demoted is set when the provider's jobs that ran include DemoteAfter hung
// ones inside DemoteWindow and none ended PR or DONE since the latest of them.
func (in PickInput) demoted(p Provider) (string, bool) {
	hung := 0
	for i := len(in.Ledger) - 1; i >= 0; i-- {
		r := in.Ledger[i]
		if !ranOn(r, p) || r.Status == JobLaunchFailed {
			continue
		}
		if in.Now.Sub(r.ended()) > DemoteWindow || r.Status == JobPR || r.Status == JobDone {
			break
		}
		if r.hung() {
			hung++
		}
	}
	if hung < DemoteAfter {
		return "", false
	}
	return fmt.Sprintf("%d jobs hung in the last %s; last in the order until a job ends PR or DONE", hung, Age(DemoteWindow)), true
}

// ProviderHealth is one provider's recent outcomes and what the pick does with it now.
type ProviderHealth struct {
	Provider string `json:"provider"`
	// Jobs is how many of the provider's latest jobs the counts cover.
	Jobs     int            `json:"jobs"`
	Outcomes map[string]int `json:"outcomes"`
	// MedianS is the median duration, in seconds, of those that ran (not LAUNCH_FAILED).
	MedianS float64 `json:"median_s"`
	Skip    string  `json:"skip,omitempty"`
	Demoted string  `json:"demoted,omitempty"`
}

// HealthProviders lists the providers worth reporting on: those the roles
// name, then agents the ledger saw since the given time that no role covers.
func HealthProviders(m *Machine, ledger []Result, since time.Time) []Provider {
	var out []Provider
	seen := map[string]bool{}
	add := func(p Provider) {
		if !seen[p.Name()] {
			seen[p.Name()] = true
			out = append(out, p)
		}
	}
	for _, class := range roleNames {
		if r, ok := m.RoleFor(class); ok {
			for _, p := range r.Providers {
				add(p)
			}
		}
	}
	for _, r := range ledger {
		if r.Provider == "" || r.Started.Before(since) {
			continue
		}
		covered := false
		for _, p := range out {
			covered = covered || ranOn(r, p)
		}
		if !covered {
			add(Provider{Agent: r.Provider, Model: r.ModelRequested})
		}
	}
	return out
}

// Health reads the ledger the way a pick does and reports each provider.
func Health(in PickInput, providers []Provider) []ProviderHealth {
	out := make([]ProviderHealth, 0, len(providers))
	for _, p := range providers {
		h := ProviderHealth{Provider: p.Name(), Outcomes: map[string]int{}}
		var ran []float64
		for i := len(in.Ledger) - 1; i >= 0 && h.Jobs < HealthJobs; i-- {
			r := in.Ledger[i]
			if !ranOn(r, p) {
				continue
			}
			h.Jobs++
			h.Outcomes[r.Status]++
			if r.Status != JobLaunchFailed {
				ran = append(ran, r.DurationS)
			}
		}
		h.MedianS = median(ran)
		h.Skip = in.skip(p)
		h.Demoted, _ = in.demoted(p)
		out = append(out, h)
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	if n := len(v); n%2 == 0 {
		return (v[n/2-1] + v[n/2]) / 2
	}
	return v[len(v)/2]
}
