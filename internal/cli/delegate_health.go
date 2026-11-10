package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// healthOutcomes are the statuses `ki delegate health` counts, in column order.
var healthOutcomes = []string{ki.JobPR, ki.JobDone, ki.JobBlocked, ki.JobFailed, ki.JobStalled, ki.JobQuota, ki.JobLaunchFailed}

// providerHealth reads the ledger and the machine's roles the way a pick does.
func (a *App) providerHealth() ([]ki.ProviderHealth, error) {
	ledger, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		return nil, fmt.Errorf("reading the job ledger: %w", err)
	}
	m, err := ki.LoadPolicy(a.machinePath())
	if err != nil {
		return nil, fail("%v", err)
	}
	now := time.Now()
	in := ki.PickInput{Ledger: ledger, Now: now, Installed: func(agent string) bool {
		_, err := exec.LookPath(agent)
		return err == nil
	}}
	return ki.Health(in, ki.HealthProviders(m, ledger, now.Add(-ki.HealthSince))), nil
}

// delegateHealth prints each provider's recent outcomes, median duration and
// whether the next pick skips or demotes it.
func (a *App) delegateHealth(args []string) error {
	asJSON := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--json":
		asJSON = true
	default:
		return fail("usage: ki delegate health [--json]")
	}
	health, err := a.providerHealth()
	if err != nil {
		return err
	}
	if asJSON {
		if err := json.NewEncoder(a.Stdout).Encode(health); err != nil {
			return fmt.Errorf("printing health: %w", err)
		}
		return nil
	}
	w := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tJOBS\t"+strings.Join(healthOutcomes, "\t")+"\tMEDIAN\tNEXT PICK")
	for _, h := range health {
		cells := make([]string, len(healthOutcomes))
		for i, s := range healthOutcomes {
			cells[i] = fmt.Sprint(h.Outcomes[s])
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", h.Provider, h.Jobs, strings.Join(cells, "\t"), medianLabel(h), nextPick(h))
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("printing health: %w", err)
	}
	return nil
}

func medianLabel(h ki.ProviderHealth) string {
	if h.Jobs == 0 || h.MedianS == 0 {
		return "-"
	}
	return ki.Clock(time.Duration(h.MedianS * float64(time.Second)))
}

func nextPick(h ki.ProviderHealth) string {
	switch {
	case h.Skip != "":
		return "skipped: " + h.Skip
	case h.Demoted != "":
		return "demoted: " + h.Demoted
	}
	return "ok"
}

// healthSummary is one line for ki doctor: the providers the next pick would
// not use as usual, or that all are fine.
func healthSummary(health []ki.ProviderHealth) string {
	var parts []string
	for _, h := range health {
		if h.Skip != "" || h.Demoted != "" {
			parts = append(parts, h.Provider+" "+nextPick(h))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d providers, none skipped or demoted", len(health))
	}
	return strings.Join(parts, "; ")
}

// notifyStalled shows a notification for a stalled job when the machine asks for one.
func (a *App) notifyStalled(ctx context.Context, m *ki.Machine, r ki.Result) {
	if !m.NotifyStall() {
		return
	}
	title, body := "ki: job stalled", fmt.Sprintf("%s on %s printed nothing; ended as STALLED, worktree kept", r.ID, r.Provider)
	if a.Notify != nil {
		a.Notify(title, body)
		return
	}
	if runtime.GOOS != "darwin" {
		return
	}
	script := fmt.Sprintf("display notification %q with title %q", body, title)
	// A missing notification must not change the job's recorded outcome.
	if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err != nil { // #nosec G204 -- fixed binary, text built by ki
		fmt.Fprintf(a.Stderr, "ki: could not show the stall notification: %v\n", err)
	}
}

// useColor is whether stdout is a terminal that may take colour.
func (a *App) useColor() bool {
	f, ok := a.Stdout.(*os.File)
	if !ok || a.Getenv("NO_COLOR") != "" {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
