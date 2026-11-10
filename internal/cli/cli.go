// Package cli implements the ki commands. cmd/ki only wires it to the process:
// standard streams, environment, signal context and exit code.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/kislow/kio"
	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// coreUsage is the command list of `ki help` before the registered extensions.
const coreUsage = `ki: agent sessions scoped by the central projects file

  ki [claude|codex|agy|cursor-agent|agent] [--brain B]... [--agent A] [--model M] [--mode ask|auto|edits|skip] [--identity NAME] [--force] [-- agent args]
                                                                  start an agent session: the agent is --agent or the positional name, else KI_AGENT,
                                                                  else the first eligible provider of roles.planner, else claude (KI_MODEL and KI_MODE set the defaults);
                                                                  outside a git repo it runs as the one identity of the repos under the folder, or --identity
  ki status [--json]                                              show what ki resolves here
  ki recap [--identity NAME] [--since YYYY-MM-DD] [--json]        one identity's work since the previous working day
  ki update                                                       checkout: pull it and a linked config, reinstall ki, relink skills, run doctor;
                                                                  release binary: pull a linked config, relink skills (upgrade the binary yourself)
  ki doctor                                                       check this machine's setup
  ki cursor                                                       write Cursor's local MCP config and rule
  ki setup claude                                                 turn off Claude Code's commit and PR attribution
  ki setup codex                                                  add the KB and instructions to Codex
  ki setup skills                                                 link the /ki-* skills for Claude Code and the other agents
  ki setup machine <identity>...                                  record which identities this machine carries (machine.yaml)
  ki setup config <dir>                                           use a configuration directory of your own (projects.yaml and friends)
  ki vault push [-m message]                                      commit, pull with rebase and push $KI_VAULT
  ki vault mcp                                                    serve $KI_VAULT to an agent over stdio (started by the agent)
  ki mcp ls [--json] [--agent claude|cursor|codex]                every MCP server each agent can reach, with source, transport and status
  ki mecca [attach|new|ls|kill]                                   tmux hub with one window per agent session (ki mecca help)
  ki delegate (--task "..." | --issue N) [--role R | --agent A] [--json]  one headless task in a worktree; --issue claims it first
  ki delegate ls [--all] [--json]                                 running jobs first, then the ones this machine ran, newest first (--all adds asks and skill runs)
  ki ask "<question>" [--role R | --agent A] [--source kb,vault,gh] [--json]  one read-only answer with its sources
  ki skill run <skill> [args...] [--role R | --agent A] [--checkout PR|BRANCH] [--allow TOOL]... [--json]
                                                                  one skill, headless, held to the skill's own tool list (ki skill help)
  ki kb brains|search|recent|activity|graph|context [--brain B]... [--all] [--json]  read-only views of the knowledge base for UIs (ki kb help)
  ki delegate watch <job>                                         follow a job's progress lines until it ends
  ki delegate resume <job> [--role R | --agent A] [--when-available]  continue a QUOTA job in its worktree on another provider
  ki delegate cancel <job>                                        drop a QUEUED job
  ki queue run|install|uninstall|status                           start queued jobs whose provider is back; install runs it every minute (launchd)
  ki delegate log [--date YYYY-MM-DD] [--vault]          render or publish a daily agent log
  ki approve <pr> [--review] [--merge] [--dry-run] [--json]       approve a pull request, or review it read-only first and approve if clean
  ki diagram build|check|render <file>                            tldraw drawings for the vault: build from a spec, keep hand edits, PDF or SVG
  ki validate [file]                                              validate a projects file
`

// commandTimeout bounds every git and gh call ki makes.
const commandTimeout = 15 * time.Second

// Repo-local Cursor files written by `ki cursor`. Both must be git-ignored.
const (
	cursorMCP  = ".cursor/mcp.json"
	cursorRule = ".cursor/rules/ki.mdc"
)

func cursorFiles() []string { return []string{cursorMCP, cursorRule} }

// UserError is a problem the user has to fix; printed without noise.
type UserError struct{ Msg string }

func (e UserError) Error() string { return e.Msg }

func fail(format string, args ...any) error { return UserError{fmt.Sprintf(format, args...)} }

// App holds everything a command touches outside its arguments, so tests can
// run commands against temporary directories and fake environments.
type App struct {
	// Stdin is read by the hidden `ki skill guard` and `ki session guard`; nil means os.Stdin.
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	Version string
	// GOOS selects the platform-specific service integration; empty uses runtime.GOOS.
	GOOS string
	// Exec replaces the process with the agent; tests substitute a recorder.
	Exec func(bin string, argv, env []string) error
	// Environ is the environment the agent inherits.
	Environ func() []string
	// Data replaces the agent instructions and skills embedded in the binary
	// (agent/, skills/); nil means the embedded copy.
	Data fs.FS
	// Dir is where ki looks for the current repo; empty means the process's directory.
	Dir string
	// Executable is this ki binary, handed to agents so they can start
	// `ki vault mcp`; nil means os.Executable.
	Executable func() (string, error)
	// Tmux runs tmux for `ki mecca`; nil means the tmux on PATH.
	Tmux Tmux
	// GitHubToken returns an account's token; nil means gh's stored one.
	GitHubToken func(ctx context.Context, user string) string
	// GitHubAPI is the REST and GraphQL base URL; empty means api.github.com.
	GitHubAPI string
	// GitHubHTTP optionally replaces the bounded HTTP client for GitHub requests.
	GitHubHTTP *http.Client
	// StallAfter is how long a streaming delegate worker may be silent before ki
	// warns; zero means five minutes.
	StallAfter time.Duration
	// ResultGrace is how long a streaming worker may run on after its final
	// result before ki ends it; zero means a minute.
	ResultGrace time.Duration
	// EventClock is the clock the stall and linger checks of a streaming worker
	// read; nil means time.Now.
	EventClock func() time.Time
	// Notify shows a desktop notification when a job stalls and the machine asks
	// for one; nil means osascript on macOS and nothing elsewhere.
	Notify func(title, body string)
	// ClaudeMCPList returns `claude mcp list`'s output for `ki mcp ls`; nil means the claude on PATH.
	ClaudeMCPList func(ctx context.Context) (string, error)
	// GH runs gh for `ki approve` as one account (its token in GH_TOKEN); nil means the gh on PATH.
	GH ghRun
	// Now is the clock provider picks and the queue read; nil means time.Now.
	Now func() time.Time
	// StartQueued hands a queued job to a ki process; nil means a detached `ki queue start <job>`.
	StartQueued func(ctx context.Context, e ki.QueueEntry) error

	// hostname and pollDelay let tests name the host and skip the wait for GitHub's mergeable state.
	hostname  func() string
	pollDelay time.Duration
}

// binary is the path agents use to start ki as the vault server.
func (a *App) binary() string {
	exe := a.Executable
	if exe == nil {
		exe = os.Executable
	}
	p, err := exe()
	if err != nil {
		return ""
	}
	return p
}

// New returns an App bound to this process.
func New(stdout, stderr io.Writer, version string) *App {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version // go install module@version stamps no -ldflags
	}
	return &App{
		Stdout: stdout, Stderr: stderr, Getenv: os.Getenv, Version: version,
		Exec: syscall.Exec, Environ: os.Environ,
	}
}

// Run dispatches one ki command.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprintln(a.Stdout, usage())
			return nil
		case "version", "--version":
			fmt.Fprintln(a.Stdout, a.Version)
			return nil
		case "status":
			return a.status(ctx, args[1:])
		case "recap":
			return a.recap(ctx, args[1:])
		case "standup":
			fmt.Fprintln(a.Stderr, "ki: `ki standup` is now `ki recap`; the old name will be removed in a later release")
			return a.recap(ctx, args[1:])
		case "update":
			return a.update(ctx)
		case "doctor":
			return a.doctor(ctx)
		case "cursor":
			return a.cursor(ctx)
		case "setup":
			return a.setup(ctx, args[1:])
		case "vault":
			return a.vault(ctx, args[1:])
		case "diagram":
			return a.diagram(ctx, args[1:])
		case "validate":
			return a.validate(args[1:])
		case "mecca":
			return a.mecca(ctx, args[1:])
		case "delegate":
			return a.delegate(ctx, args[1:])
		case "approve":
			return a.approve(ctx, args[1:])
		case "ask":
			return a.ask(ctx, args[1:])
		case "skill":
			return a.skill(ctx, args[1:])
		case "session":
			return a.session(args[1:])
		case "mcp":
			return a.mcp(ctx, args[1:])
		case "kb":
			return a.kb(ctx, args[1:])
		case "queue":
			return a.queue(ctx, args[1:])
		case "run":
			args = args[1:]
		default:
			if e, ok := findExtension(args[0]); ok {
				return e.run(a, ctx, args[1:])
			}
			if err := checkLeadingWord(args[0]); err != nil {
				return err
			}
		}
	}
	return a.run(ctx, args)
}

// --- environment --------------------------------------------------------------

// git runs git with a timeout and returns trimmed stdout, or "" on any failure:
// callers treat a missing value (no repo, no remote, no email) as unknown.
func git(ctx context.Context, dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary, arguments from ki itself
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (a *App) home() string {
	if h := a.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// harness finds the kio checkout: $KI_HOME, else the link `make install-cli` creates.
// Neither depends on the directory's name. "" without an error means there is none:
// ki then runs from the copy embedded in the binary and from a linked configuration.
func (a *App) harness() (string, error) {
	for _, c := range []string{a.Getenv("KI_HOME"), filepath.Join(a.home(), ".config", "ki", "harness")} {
		if c == "" {
			continue
		}
		c = filepath.Clean(c)
		if _, err := os.Stat(filepath.Join(c, "deploy", "config", "projects.yaml")); err == nil {
			resolved, err := filepath.EvalSymlinks(c)
			if err != nil {
				return "", fmt.Errorf("resolving %s: %w", c, err)
			}
			return resolved, nil
		}
	}
	return "", nil
}

// dataFS is where the agent instructions and skills are read from: the checkout
// when there is one, so edits show at once, else the copy embedded in the binary.
func (a *App) dataFS(harness string) fs.FS {
	switch {
	case harness != "":
		return os.DirFS(harness)
	case a.Data != nil:
		return a.Data
	default:
		return kio.Data
	}
}

// readData reads a file of the data layout (agent/..., skills/...), "" when it is absent.
func readData(fsys fs.FS, name string) (string, error) {
	raw, err := fs.ReadFile(fsys, filepath.ToSlash(name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	return string(raw), nil
}

func (a *App) confDir() string {
	if d := a.Getenv("KI_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(a.home(), ".config", "ki")
}

func ghToken(ctx context.Context, user string) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--user", user).Output() // #nosec G204 -- user from projects file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tokenFile is ki.TokenFile plus a deprecation notice when the session only
// has the legacy sensitive token.
func (a *App) tokenFile(s ki.Session) string {
	path := ki.TokenFile(a.confDir(), s)
	if filepath.Base(path) == ki.LegacySensitiveTokenName {
		fmt.Fprintf(a.Stderr, "ki: %s is deprecated; rename it to token-sensitive\n", a.short(path))
	}
	return path
}

func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed location under the user's config dir
	if errors.Is(err, os.ErrNotExist) {
		return "", fail("missing %s; see docs/laptop-setup.md", path)
	}
	if err != nil {
		return "", fmt.Errorf("reading token: %w", err)
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", fail("%s is empty", path)
	}
	return t, nil
}

type here struct {
	harness      string // the checkout, "" when ki runs from the embedded copy
	data         fs.FS  // the agent instructions and skills: the checkout's or the embedded ones
	config       string // the configuration directory
	configSource string // env, link or harness (ki.ConfigFrom*)
	settings     ki.Settings
	url          string // the knowledge base, "" in local mode (see mcpURL)
	root         string
	// dir is the current directory when it is not inside a git repo, "" in a repo.
	dir string
	// mixed names the identities of the repos under dir when there are several.
	mixed   []string
	session ki.Session
	cfg     *projects.Config
}

func (a *App) current(ctx context.Context, extra []string) (*here, error) {
	h, err := a.harness()
	if err != nil {
		return nil, err
	}
	dir, source, err := a.configDir(h)
	if err != nil {
		return nil, err
	}
	cfg, err := projects.Load(filepath.Join(dir, ki.ProjectsFile))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", filepath.Join(dir, ki.ProjectsFile), err)
	}
	settings, err := ki.LoadSettings(filepath.Join(dir, ki.ConfigFile))
	if err != nil {
		return nil, fail("%v", err)
	}
	cur := &here{harness: h, data: a.dataFS(h), config: dir, configSource: source, settings: settings, cfg: cfg}
	cur.root = git(ctx, a.Dir, "rev-parse", "--show-toplevel")
	if cur.root != "" {
		cur.session = ki.Resolve(cfg, git(ctx, cur.root, "remote", "get-url", "origin"), extra)
	} else {
		if cur.dir, err = a.workDir(); err != nil {
			return nil, err
		}
		cur.session, cur.mixed = folderSession(ctx, cfg, cur.dir, extra)
	}
	cur.url = a.mcpURL(cur)
	return cur, nil
}

func readOptional(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the user's own config files
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(raw), nil
}

// writeFile writes content, keeping an existing file's permissions.
func writeFile(path, content string, mode os.FileMode) error {
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
