package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

const meccaUsage = `ki mecca: one tmux session (ki-mecca) with a window per agent session

  ki mecca [attach [window]]                                      attach to the hub, creating it if needed
  ki mecca new <project|path> [--agent A] [--task T] [--mode ask|auto|edits|skip] [--name N]
               [--brain B]... [--model M] [--force] [--attach] [--json] [-- agent args]
                                                                  open a window running ki in that checkout
  ki mecca ls [--json]                                            windows with project, agent, identity, mode, age, status
  ki mecca kill <window>                                          close a window (name, index or @id)

Names resolve through ~/.config/ki/mecca.yaml (checkouts:, then roots:, default ~/code).
KI_MECCA_SOCKET runs everything on tmux -L <socket>.`

// Tmux runs one tmux command (several when separated by ";") and returns its stdout.
type Tmux func(ctx context.Context, args ...string) (string, error)

func (a *App) mecca(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.meccaAttach(ctx, nil)
	}
	switch args[0] {
	case "attach":
		return a.meccaAttach(ctx, args[1:])
	case "new":
		return a.meccaNew(ctx, args[1:])
	case "ls", "list":
		return a.meccaList(ctx, args[1:])
	case "kill":
		return a.meccaKill(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Fprintln(a.Stdout, meccaUsage)
		return nil
	}
	return fail("unknown ki mecca command %q; see `ki mecca help`", args[0])
}

// tmuxArgs prefixes the socket flag that keeps a test or second hub apart.
func (a *App) tmuxArgs(args ...string) []string {
	if s := a.Getenv("KI_MECCA_SOCKET"); s != "" {
		return append([]string{"-L", s}, args...)
	}
	return args
}

func tmuxBin() (string, error) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return "", fail("tmux not found on PATH; install it with `brew install tmux`")
	}
	return bin, nil
}

func (a *App) tmux() (Tmux, error) {
	if a.Tmux != nil {
		return a.Tmux, nil
	}
	bin, err := tmuxBin()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, a.tmuxArgs(args...)...) // #nosec G204 -- fixed binary, arguments from ki itself
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}, nil
}

const meccaTarget = "=" + ki.MeccaSession

func hasSession(ctx context.Context, tm Tmux) bool {
	_, err := tm(ctx, "has-session", "-t", meccaTarget)
	return err == nil
}

// ensureSession creates the hub with a home window, and (re)sets the hook that
// keeps a window whose ki failed open, so its error stays readable. The hook
// is scoped to this session; global tmux options are never touched.
func (a *App) ensureSession(ctx context.Context, tm Tmux) error {
	if !hasSession(ctx, tm) {
		if _, err := tm(ctx, "new-session", "-d", "-s", ki.MeccaSession, "-n", "home", "-c", a.home()); err != nil {
			return fmt.Errorf("creating the %s session: %w", ki.MeccaSession, err)
		}
	}
	if _, err := tm(ctx, "set-hook", "-t", ki.MeccaSession, "after-new-window", "set-option -w remain-on-exit failed"); err != nil {
		return fmt.Errorf("setting the %s hook: %w", ki.MeccaSession, err)
	}
	return nil
}

func listWindows(ctx context.Context, tm Tmux) ([]ki.Window, error) {
	out, err := tm(ctx, "list-windows", "-t", meccaTarget, "-F", ki.ListFormat)
	if err != nil {
		return nil, fmt.Errorf("listing %s windows: %w", ki.MeccaSession, err)
	}
	ws, err := ki.ParseWindows(out)
	if err != nil {
		return nil, fmt.Errorf("reading %s windows: %w", ki.MeccaSession, err)
	}
	return ws, nil
}

func (a *App) meccaAttach(ctx context.Context, args []string) error {
	var ref string
	switch len(args) {
	case 0:
	case 1:
		ref = args[0]
	default:
		return fail("usage: ki mecca attach [window]")
	}
	tm, err := a.tmux()
	if err != nil {
		return err
	}
	if err := a.ensureSession(ctx, tm); err != nil {
		return err
	}
	if ref != "" {
		ws, err := listWindows(ctx, tm)
		if err != nil {
			return err
		}
		w, err := ki.FindWindow(ws, ref)
		if err != nil {
			return fail("%v", err)
		}
		if _, err := tm(ctx, "select-window", "-t", w.ID); err != nil {
			return fmt.Errorf("selecting %s: %w", w.Name, err)
		}
	}
	return a.attach(ctx, tm)
}

// attach switches an existing tmux client to the hub, or replaces this process
// with a tmux client attached to it.
func (a *App) attach(ctx context.Context, tm Tmux) error {
	if a.Getenv("TMUX") != "" && a.Getenv("KI_MECCA_SOCKET") == "" {
		if _, err := tm(ctx, "switch-client", "-t", meccaTarget); err != nil {
			return fmt.Errorf("switching to %s: %w", ki.MeccaSession, err)
		}
		return nil
	}
	bin, err := tmuxBin()
	if err != nil {
		return err
	}
	// Without TMUX in the environment, tmux attaches even from inside another
	// server's pane (only possible with KI_MECCA_SOCKET set).
	env := slices.DeleteFunc(a.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "TMUX=") })
	argv := append([]string{"tmux"}, a.tmuxArgs("attach-session", "-t", meccaTarget)...)
	if err := a.Exec(bin, argv, env); err != nil {
		return fmt.Errorf("attaching to %s: %w", ki.MeccaSession, err)
	}
	return nil
}

func (a *App) meccaNew(ctx context.Context, args []string) error {
	na, err := ki.ParseMeccaNew(args, orDefault(a.Getenv("KI_AGENT"), "claude"))
	if err != nil {
		return fail("%v", err)
	}
	if ki.ValidJobID(na.Target) {
		if err := a.meccaJob(&na); err != nil {
			return err
		}
	}
	if err := ki.ValidateBrains(na.Brains); err != nil {
		return fail("%v", err)
	}
	tm, err := a.tmux()
	if err != nil {
		return err
	}
	h, err := a.harness()
	if err != nil {
		return err
	}
	pf, err := a.configPath(h, ki.ProjectsFile)
	if err != nil {
		return err
	}
	cfg, err := projects.Load(pf)
	if err != nil {
		return fmt.Errorf("loading %s: %w", pf, err)
	}
	dir, err := a.meccaCheckout(ctx, na.Target, cfg)
	if err != nil {
		return err
	}
	s, err := a.meccaSession(ctx, cfg, dir, na)
	if err != nil {
		return err
	}
	if s.Unmapped && !s.Folder && !na.Force {
		return fail("%s is not in projects.yaml, so ki would refuse it; add it there, or pass --force for a session without an identity", dir)
	}
	w := ki.Window{
		Project: meccaProject(s, na.Target, dir), Agent: na.Agent, Identity: "unmapped",
		Task: ki.CleanOption(na.Task), Mode: na.Mode, Dir: dir, Created: time.Now(), Status: ki.StatusWorking,
	}
	if s.Identity != nil {
		w.Identity = s.Identity.Name
	}

	if err := a.ensureSession(ctx, tm); err != nil {
		return err
	}
	if w, err = a.openWindow(ctx, tm, na, w); err != nil {
		return err
	}
	if na.JSON {
		if err := json.NewEncoder(a.Stdout).Encode(w); err != nil {
			return fmt.Errorf("encoding window: %w", err)
		}
	} else {
		fmt.Fprintf(a.Stdout, "opened %s (%s): %s as %s in %s\n", w.Name, w.ID, w.Agent, w.Identity, w.Dir)
	}
	if na.Attach {
		if _, err := tm(ctx, "select-window", "-t", w.ID); err != nil {
			return fmt.Errorf("selecting %s: %w", w.Name, err)
		}
		return a.attach(ctx, tm)
	}
	return nil
}

// meccaSession is the scope of the window's directory: a repo's, or for a
// folder outside any repo the one its repos give, which has to be one
// identity or none (the window's ki is not told which of several to pick).
func (a *App) meccaSession(ctx context.Context, cfg *projects.Config, dir string, na ki.MeccaNewArgs) (ki.Session, error) {
	if git(ctx, dir, "rev-parse", "--show-toplevel") != "" {
		return ki.Resolve(cfg, git(ctx, dir, "remote", "get-url", "origin"), na.Brains), nil
	}
	s, mixed := folderSession(ctx, cfg, dir, na.Brains)
	if len(mixed) > 0 {
		return s, fail("%s holds repos of several identities (%s); start ki there with --identity NAME instead", dir, strings.Join(mixed, ", "))
	}
	return s, nil
}

// meccaJob turns a job id into a window target: the job's worktree, its
// agent, and that agent's flag to resume the session it ran, so a BLOCKED
// job is answered where it stopped.
func (a *App) meccaJob(na *ki.MeccaNewArgs) error {
	jobs, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		return fmt.Errorf("reading the job ledger: %w", err)
	}
	var job *ki.Result
	for i := range jobs {
		if jobs[i].ID == na.Target {
			job = &jobs[i]
		}
	}
	switch {
	case job == nil:
		return fail("job %s is not in the ledger (ki delegate ls)", na.Target)
	case job.Worktree == "":
		return fail("job %s ran with --here in %s; open that checkout instead", na.Target, job.Repo)
	}
	if _, err := os.Stat(job.Worktree); err != nil {
		return fail("job %s's worktree is gone: %s", na.Target, job.Worktree)
	}
	na.Target, na.Agent = job.Worktree, job.Provider
	na.Task = orDefault(na.Task, job.ID)
	na.AgentArgs = append(ki.ResumeArgs(job.Provider, job.Session), na.AgentArgs...)
	if na.Mode != "" {
		m, err := ki.ParseMode(na.Mode)
		if err == nil {
			_, _, err = ki.ModeArgs(na.Agent, m, na.AgentArgs)
		}
		if err != nil {
			return fail("%v", err)
		}
	}
	return nil
}

// openWindow starts ki in a new detached window and records w on it.
func (a *App) openWindow(ctx context.Context, tm Tmux, na ki.MeccaNewArgs, w ki.Window) (ki.Window, error) {
	ws, err := listWindows(ctx, tm)
	if err != nil {
		return w, err
	}
	taken := make([]string, len(ws))
	for i, x := range ws {
		taken[i] = x.Name
	}
	w.Name = ki.UniqueName(orDefault(na.Name, ki.WindowName(w.Project, na.Task)), taken)

	cmd := []string{"new-window", "-d", "-P", "-F", "#{window_id} #{window_index}", "-t", meccaTarget + ":", "-n", w.Name, "-c", w.Dir}
	env := ki.MeccaEnv(a.Getenv)
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd = append(cmd, "-e", k+"="+env[k])
	}
	cmd = append(append(cmd, "--"), ki.MeccaKiArgv(orDefault(a.binary(), "ki"), na)...)
	out, err := tm(ctx, cmd...)
	if err != nil {
		return w, fmt.Errorf("opening window %s: %w", w.Name, err)
	}
	id, idx, _ := strings.Cut(strings.TrimSpace(out), " ")
	w.ID = id
	if w.Index, err = strconv.Atoi(idx); err != nil {
		return w, fmt.Errorf("reading the new window's index from %q: %w", out, err)
	}

	opts := map[string]string{
		ki.OptProject: w.Project, ki.OptAgent: w.Agent, ki.OptIdentity: w.Identity, ki.OptTask: w.Task,
		ki.OptMode: w.Mode, ki.OptDir: w.Dir, ki.OptCreated: strconv.FormatInt(w.Created.Unix(), 10),
	}
	var set []string
	for _, k := range slices.Sorted(maps.Keys(opts)) {
		if opts[k] == "" {
			continue
		}
		if len(set) > 0 {
			set = append(set, ";")
		}
		set = append(set, "set-option", "-w", "-t", w.ID, k, opts[k])
	}
	if _, err := tm(ctx, set...); err != nil {
		return w, fmt.Errorf("labelling window %s: %w", w.Name, err)
	}
	return w, nil
}

// meccaProject is the label a window shows: the project name the user typed if
// it maps there, else the first mapped name, else the folder name.
func meccaProject(s ki.Session, target, dir string) string {
	switch {
	case slices.Contains(s.Projects, target):
		return target
	case len(s.Projects) > 0:
		return s.Projects[0]
	}
	return filepath.Base(dir)
}

func looksLikePath(t string) bool {
	return strings.ContainsRune(t, filepath.Separator) || strings.HasPrefix(t, "~") || t == "." || t == ".."
}

// meccaCheckout turns a project name or path into the checkout to run ki in.
func (a *App) meccaCheckout(ctx context.Context, target string, cfg *projects.Config) (string, error) {
	if looksLikePath(target) {
		p := ki.ExpandHome(target, a.home())
		if !filepath.IsAbs(p) {
			base := a.Dir
			if base == "" {
				wd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("finding the current directory: %w", err)
				}
				base = wd
			}
			p = filepath.Join(base, p)
		}
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			return "", fail("%s is not a directory", p)
		}
		if root := git(ctx, p, "rev-parse", "--show-toplevel"); root != "" {
			return root, nil
		}
		return p, nil
	}
	repo := ""
	for _, p := range cfg.Projects {
		if p.Name == target {
			repo = p.Repo
		}
	}
	co, err := ki.LoadCheckouts(filepath.Join(a.confDir(), ki.MeccaConfigFile), a.home())
	if err != nil {
		return "", fail("%v", err)
	}
	dir, err := co.Find(target, repo, func(d string) string { return git(ctx, d, "remote", "get-url", "origin") })
	if err != nil {
		return "", fail("%v", err)
	}
	return dir, nil
}

func (a *App) meccaList(ctx context.Context, args []string) error {
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			return fail("usage: ki mecca ls [--json]")
		}
		asJSON = true
	}
	tm, err := a.tmux()
	if err != nil {
		return err
	}
	ws := []ki.Window{}
	if hasSession(ctx, tm) {
		if ws, err = listWindows(ctx, tm); err != nil {
			return err
		}
	}
	now := time.Now()
	for i, w := range ws {
		tail := ""
		if ki.NeedsTail(w, now) {
			// A window can close between list and capture; it then reads as idle.
			if out, err := tm(ctx, "capture-pane", "-p", "-J", "-t", w.ID); err == nil {
				tail = out
			}
		}
		ws[i] = ki.Classify(w, tail, now)
	}
	if asJSON {
		if err := json.NewEncoder(a.Stdout).Encode(ws); err != nil {
			return fmt.Errorf("encoding windows: %w", err)
		}
		return nil
	}
	if len(ws) == 0 {
		fmt.Fprintln(a.Stdout, "no ki-mecca windows; open one with `ki mecca new <project>`")
		return nil
	}
	return writeWindows(a.Stdout, ws, now)
}

func writeWindows(out io.Writer, ws []ki.Window, now time.Time) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW\tPROJECT\tAGENT\tIDENTITY\tMODE\tAGE\tSTATUS")
	for _, w := range ws {
		age := "-"
		if !w.Created.IsZero() {
			age = ki.Age(now.Sub(w.Created))
		}
		status := w.Status
		if w.Note != "" {
			status += ": " + truncate(w.Note, 60)
		}
		fmt.Fprintf(tw, "%d:%s\t%s\t%s\t%s\t%s\t%s\t%s\n", w.Index, w.Name,
			dash(w.Project), dash(w.Agent), dash(w.Identity), dash(w.Mode), age, status)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("writing windows: %w", err)
	}
	return nil
}

func dash(s string) string { return orDefault(s, "-") }

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (a *App) meccaKill(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fail("usage: ki mecca kill <window>")
	}
	tm, err := a.tmux()
	if err != nil {
		return err
	}
	if !hasSession(ctx, tm) {
		return fail("no %s session", ki.MeccaSession)
	}
	ws, err := listWindows(ctx, tm)
	if err != nil {
		return err
	}
	w, err := ki.FindWindow(ws, args[0])
	if err != nil {
		return fail("%v", err)
	}
	if _, err := tm(ctx, "kill-window", "-t", w.ID); err != nil {
		return fmt.Errorf("closing %s: %w", w.Name, err)
	}
	fmt.Fprintf(a.Stdout, "closed %s (%s)\n", w.Name, w.ID)
	return nil
}

// checkTmux is a doctor note, never a failure: only ki mecca needs tmux.
func (a *App) checkTmux(c *checks) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		c.note("tmux", "not installed: ki mecca needs it (brew install tmux)")
		return
	}
	c.report(true, "tmux", a.short(path)+", for ki mecca", "")
}
