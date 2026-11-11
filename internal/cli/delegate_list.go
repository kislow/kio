package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// delegateList prints the queued jobs, the ones still running, then the ledger, newest first.
// Questions (ki ask) and skill runs (ki skill run) are left out unless --all asks for them.
func (a *App) delegateList(args []string) error {
	asJSON, all := false, false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "--all":
			all = true
		default:
			return fail("usage: ki delegate ls [--all] [--json]")
		}
	}
	jobs, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		return fmt.Errorf("listing jobs: %w", err)
	}
	for i, j := 0, len(jobs)-1; i < j; i, j = i+1, j-1 {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	}
	running, err := ki.ReadRunning(a.jobsDir())
	if err != nil {
		return fmt.Errorf("listing jobs: %w", err)
	}
	queued, err := a.queuedJobs()
	if err != nil {
		return fmt.Errorf("listing jobs: %w", err)
	}
	jobs = append(append(queued, running...), jobs...)
	if !all {
		jobs = slices.DeleteFunc(jobs, func(r ki.Result) bool { return r.Kind == ki.KindAsk || r.Kind == ki.KindSkill })
	}
	for i := range jobs {
		if jobs[i].Title == "" {
			jobs[i].Title = ki.BriefTitleFile(a.jobsDir(), jobs[i].ID)
		}
	}
	if asJSON {
		if jobs == nil {
			jobs = []ki.Result{}
		}
		if err := json.NewEncoder(a.Stdout).Encode(jobs); err != nil {
			return fmt.Errorf("printing jobs: %w", err)
		}
		return nil
	}
	w := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tTITLE\tAGENT\tMODEL\tSTATUS\tREPO\tBRANCH\tISSUE\tAGE\tDETAIL\tLAST")
	color := a.useColor()
	for _, j := range jobs {
		age, detail, last := ki.Age(time.Since(j.Started)), truncate(j.Detail, 60), ""
		agent := orDefault(j.Provider, "-")
		switch j.Status {
		case ki.JobRunning:
			age, detail, last = ki.Clock(time.Since(j.Started)), fmt.Sprintf("pid %d", j.Pid), lastEvent(j, color)
		case ki.JobQueued:
			agent, detail = "role "+j.Role, truncate(j.Detail, 60)+"; "+expectedStart(j.StartAfter)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, orDefault(truncate(j.Title, 50), "-"), agent, orDefault(j.Model, "-"), j.Status, repoLabel(j.Repo), orDefault(truncate(j.Branch, 50), "-"), orDefault(j.Issue, j.PR), age, detail, last)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("printing jobs: %w", err)
	}
	return nil
}

// lastEvent is the age of a running job's last event, red past its stall
// limit. It is the last column, so the escape codes never shift another one.
func lastEvent(j ki.Result, color bool) string {
	if j.LastEventAt == nil {
		return "-"
	}
	return silenceLabel(time.Since(*j.LastEventAt), j.StallS, color)
}

func silenceLabel(quiet time.Duration, stallS float64, color bool) string {
	text := ki.Clock(quiet)
	if color && stallS > 0 && quiet.Seconds() >= stallS {
		return "\x1b[31m" + text + "\x1b[0m"
	}
	return text
}

// repoLabel is the repo's folder name, "-" for a job that ran in none (ki ask).
func repoLabel(repo string) string {
	if repo == "" {
		return "-"
	}
	return filepath.Base(repo)
}
