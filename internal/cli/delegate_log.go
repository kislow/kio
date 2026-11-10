package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/internal/vault"
	"github.com/kislow/kio/pkg/projects"
)

const delegateLogUsage = "usage: ki delegate log [--date YYYY-MM-DD] [--vault]"

type delegateLogArgs struct {
	date  time.Time
	vault bool
}

func parseDelegateLogArgs(args []string, now time.Time) (delegateLogArgs, error) {
	fs := flag.NewFlagSet("delegate log", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	date := fs.String("date", "", "UTC date")
	vault := fs.Bool("vault", false, "write and push the daily note")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return delegateLogArgs{}, fail("%s", delegateLogUsage)
	}
	day := now.UTC().Format(time.DateOnly)
	if *date != "" {
		if _, err := time.Parse(time.DateOnly, *date); err != nil {
			return delegateLogArgs{}, fail("--date %q: use YYYY-MM-DD", *date)
		}
		day = *date
	}
	parsed, _ := time.Parse(time.DateOnly, day)
	return delegateLogArgs{date: parsed, vault: *vault}, nil
}

type logJob struct {
	ID, Machine, Agent, Model, Repo, Issue, Outcome, PR string
	Started                                             time.Time
	Duration                                            float64
	Tokens                                              ki.Tokens
	Sensitive                                           bool
}

var issueNumberPattern = regexp.MustCompile(`(?:^|[^[:alnum:]_])#([1-9][0-9]{0,8})\b`)

func (a *App) delegateLog(ctx context.Context, args []string) error {
	la, err := parseDelegateLogArgs(args, time.Now())
	if err != nil {
		return err
	}
	ledger, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		return fmt.Errorf("reading the job ledger: %w", err)
	}
	proj, err := a.delegateLogProjects()
	if err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	host = strings.Split(host, ".")[0]
	jobs := make([]logJob, 0, len(ledger))
	for _, r := range ledger {
		if !sameDay(r.Started, la.date) {
			continue
		}
		entry := logJob{ID: r.ID, Machine: host, Agent: r.Provider, Model: r.Model, Outcome: r.Status, Started: r.Started.UTC(), Duration: r.DurationS, Tokens: r.Tokens, Sensitive: true}
		project, ok := projectForRepo(ctx, proj, r.Repo)
		if ok && !sensitiveLogProject(proj, project) {
			entry.Sensitive = false
			entry.Repo = project.Name
			brief, readErr := os.ReadFile(filepath.Join(a.jobsDir(), r.ID, "brief.md"))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return fmt.Errorf("reading brief for %s: %w", r.ID, readErr)
			}
			if readErr == nil {
				entry.Issue = issueNumber(string(brief))
			}
			if r.Status == ki.JobPR {
				entry.PR = r.Detail
			}
		}
		jobs = append(jobs, entry)
	}
	note := renderDelegateLog(la.date, jobs)
	if !la.vault {
		if _, err := io.WriteString(a.Stdout, note); err != nil {
			return fmt.Errorf("printing delegate log: %w", err)
		}
		return nil
	}
	return a.writeDelegateLog(ctx, la.date, note)
}

// delegateLogProjects reads the configured project map without requiring the
// command to run from a project checkout.
func (a *App) delegateLogProjects() (*projects.Config, error) {
	h, err := a.harness()
	if err != nil {
		return nil, err
	}
	p, err := a.configPath(h, ki.ProjectsFile)
	if err != nil {
		return nil, err
	}
	cfg, err := projects.Load(p)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", p, err)
	}
	return cfg, nil
}

func sameDay(t, day time.Time) bool {
	return t.UTC().Format(time.DateOnly) == day.UTC().Format(time.DateOnly)
}

func projectForRepo(ctx context.Context, cfg *projects.Config, repo string) (projects.Project, bool) {
	if repo == "" {
		return projects.Project{}, false
	}
	remote := git(ctx, repo, "remote", "get-url", "origin")
	if remote == "" {
		return projects.Project{}, false
	}
	normalized, err := projects.NormalizeRepo(remote)
	if err != nil {
		return projects.Project{}, false
	}
	for _, p := range cfg.Projects {
		configured, err := projects.NormalizeRepo(p.Repo)
		if err == nil && configured == normalized {
			return p, true
		}
	}
	return projects.Project{}, false
}

func projectSensitive(cfg *projects.Config, p projects.Project) bool {
	for _, b := range p.Brains {
		if cfg.IsSensitive(b) {
			return true
		}
	}
	for _, rule := range p.Ingest {
		if cfg.IsSensitive(rule.Brain) {
			return true
		}
	}
	return false
}

func sensitiveLogProject(cfg *projects.Config, p projects.Project) bool {
	return !p.Identity.Vault || projectSensitive(cfg, p)
}

func issueNumber(text string) string {
	m := issueNumberPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func renderDelegateLog(day time.Time, jobs []logJob) string {
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Started.Equal(jobs[j].Started) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].Started.Before(jobs[j].Started)
	})
	var b strings.Builder
	date := day.UTC().Format(time.DateOnly)
	// The vault's conventions (AGENTS.md): a dated, unique title, area and
	// created in the frontmatter, and no H1 in the body (the file name is the title).
	fmt.Fprintf(&b, "---\ntitle: %s Agent Log\narea: agents\ncreated: %s\ntags: [agent-log]\n---\n", date, date)
	if len(jobs) == 0 {
		b.WriteString("\nNo delegated jobs.\n")
		return b.String()
	}
	b.WriteString("\n| Time (UTC) | Machine | Agent | Model | Repo | Issue | Outcome | PR | Duration | Tokens (in/out/cache) |\n| --- | --- | --- | --- | --- | ---: | --- | --- | ---: | ---: |\n")
	for _, j := range jobs {
		if j.Sensitive {
			fmt.Fprintf(&b, "| %s |  |  |  |  |  | %s |  |  |  |\n", escapeTable(j.ID), escapeTable(j.Outcome))
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %.1fs | %d/%d/%d |\n", j.Started.UTC().Format("15:04:05"), escapeTable(j.Machine), escapeTable(j.Agent), escapeTable(j.Model), escapeTable(j.Repo), escapeTable(j.Issue), escapeTable(j.Outcome), escapeTable(j.PR), j.Duration, j.Tokens.Input, j.Tokens.Output, j.Tokens.CacheRead)
	}
	return b.String()
}

func escapeTable(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func (a *App) writeDelegateLog(ctx context.Context, day time.Time, note string) error {
	if line := vault.SecretLine(note); line > 0 {
		return fail("generated agent log line %d looks like a credential; refusing to write it to the vault", line)
	}
	v, err := a.vaultPreflight(ctx)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(v.dir, "AGENTS.md")); err != nil {
		return fail("KI_VAULT must contain AGENTS.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(v.dir, "agents"), 0o750); err != nil {
		return fmt.Errorf("creating agents folder: %w", err)
	}
	name := day.UTC().Format(time.DateOnly) + " Agent Log.md"
	path := filepath.Join(v.dir, "agents", name)
	old, err := os.ReadFile(path) // #nosec G304 -- fixed name beneath the preflighted vault directory
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading existing agent log: %w", err)
	}
	if string(old) != note {
		if err := os.WriteFile(path, []byte(note), 0o644); err != nil { // #nosec G306 -- vault notes are shared with Obsidian
			return fmt.Errorf("writing agent log: %w", err)
		}
	}
	if err := updateAgentsFolderNote(filepath.Join(v.dir, "agents", agentsFolderNote), strings.TrimSuffix(name, ".md")); err != nil {
		return err
	}
	lines, err := a.vaultSync(ctx, v, "agents: "+day.UTC().Format(time.DateOnly)+" Agent Log")
	for _, line := range lines {
		fmt.Fprintln(a.Stdout, line)
	}
	if err != nil {
		return fmt.Errorf("pushing agent log: %w", err)
	}
	return nil
}

const agentsFolderNote = "Agents.md"

// updateAgentsFolderNote keeps the agents/ folder note listing every daily log,
// newest first, as the vault's folder-note convention asks. It creates the note
// with its frontmatter and overview when missing and only adds a link that is
// not listed yet.
func updateAgentsFolderNote(path, logName string) error {
	link := "- [[" + logName + "]]"
	old, err := os.ReadFile(path) // #nosec G304 -- fixed name beneath the preflighted vault directory
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading agents folder note: %w", err)
	}
	text := string(old)
	if text == "" {
		text = "---\ntitle: Agents\narea: agents\ncreated: " + strings.Fields(logName)[0] +
			"\n---\nWhat delegated agents did each day, across machines, generated by `ki delegate log --vault`. " +
			"Do not edit the logs by hand. Not ingested into any brain.\n\n## Logs\n\n"
	}
	if strings.Contains(text, link+"\n") || strings.HasSuffix(text, link) {
		return nil
	}
	if i := strings.Index(text, "## Logs\n\n"); i >= 0 {
		at := i + len("## Logs\n\n")
		text = text[:at] + link + "\n" + text[at:]
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n## Logs\n\n" + link + "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil { // #nosec G306 G703 -- fixed name beneath the preflighted vault directory; vault notes are shared with Obsidian
		return fmt.Errorf("writing agents folder note: %w", err)
	}
	return nil
}
