package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// vaultTimeout bounds the pull and the push: they cross the network, unlike
// the local git calls behind commandTimeout.
const vaultTimeout = 2 * time.Minute

const vaultUsage = "usage: ki vault push [-m message] | ki vault mcp [--read-only [--path folder]...]"

func (a *App) vault(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(vaultUsage)
	}
	switch args[0] {
	case "mcp":
		readOnly, paths, ok := parseVaultMCPArgs(args[1:])
		if !ok {
			return fail(vaultUsage)
		}
		return a.vaultMCP(ctx, readOnly, paths)
	case "push":
	default:
		return fail(vaultUsage)
	}
	msg := ""
	for rest := args[1:]; len(rest) > 0; rest = rest[2:] {
		if rest[0] != "-m" || len(rest) < 2 {
			return fail(vaultUsage)
		}
		msg = rest[1]
	}
	return a.vaultPush(ctx, msg)
}

// parseVaultMCPArgs reads the flags of `ki vault mcp`: --read-only, and with it
// --path <folder> any number of times. --path alone is refused later by the
// server, with a message; here only malformed flags are not ok.
func parseVaultMCPArgs(args []string) (readOnly bool, paths []string, ok bool) {
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		switch arg {
		case "--read-only":
			readOnly = true
		case "--path":
			if len(args) == 0 {
				return false, nil, false
			}
			paths = append(paths, args[0])
			args = args[1:]
		default:
			return false, nil, false
		}
	}
	return readOnly, paths, true
}

// vaultIsClone is whether $KI_VAULT is a git work tree, which the writing
// server and `ki vault push` need. A folder that only receives the vault
// through a sync tool is not one.
func (a *App) vaultIsClone(ctx context.Context) bool {
	dir := a.Getenv("KI_VAULT")
	return dir != "" && git(ctx, dir, "rev-parse", "--is-inside-work-tree") == "true"
}

// sessionVaultServer is the vault server a session or an agent config gets:
// the writing one for a clone, the read-only one (limited to readPaths) for a
// folder that is not, and none without a vault or without the ki binary.
func (a *App) sessionVaultServer(ctx context.Context, readPaths []string) ki.MCPServer {
	dir := a.Getenv("KI_VAULT")
	if dir == "" || a.binary() == "" {
		return ki.MCPServer{}
	}
	if a.vaultIsClone(ctx) {
		return ki.VaultServer(a.binary(), dir)
	}
	return ki.ReadOnlyVaultServer(a.binary(), dir, readPaths...)
}

// addVaultServer adds the session's vault server to cfg, when there is one.
func (a *App) addVaultServer(ctx context.Context, cfg ki.MCPConfig, readPaths []string) ki.MCPConfig {
	if srv := a.sessionVaultServer(ctx, readPaths); srv.Command != "" {
		cfg.MCPServers[ki.VaultServerName] = srv
	}
	return cfg
}

// vaultPush commits everything that changed in $KI_VAULT, pulls with rebase
// and pushes, so notes written on one machine reach the cluster and the other
// machines. It is the one git path for the vault: the skills call it after
// writing a note, and the user runs it by hand. It refuses from a repo whose
// identity must never reach the vault, and when the vault would be committed
// as the wrong person.
func (a *App) vaultPush(ctx context.Context, msg string) error {
	v, err := a.vaultPreflight(ctx)
	if err != nil {
		return err
	}
	lines, err := a.vaultSync(ctx, v, msg)
	for _, l := range lines {
		fmt.Fprintln(a.Stdout, l)
	}
	return err
}

// vaultClone is a vault that passed every check and may be written and pushed.
type vaultClone struct {
	dir, branch string
}

// vaultPreflight runs every refusal before anything is written or sent: the
// vault must be set, a clone on a branch with an origin, owned by an identity
// that may hold it, committed as that identity, and the current repo must not
// belong to an identity whose content never goes to the vault.
func (a *App) vaultPreflight(ctx context.Context) (vaultClone, error) {
	dir := a.Getenv("KI_VAULT")
	if dir == "" {
		return vaultClone{}, fail("KI_VAULT is not set; see docs/laptop-setup.md")
	}
	h, err := a.current(ctx, nil)
	if err != nil {
		return vaultClone{}, err
	}
	if id := h.session.Identity; id != nil && !id.Vault {
		return vaultClone{}, fail("this repo belongs to identity %q, whose content never goes to the vault (vault: false)", id.Name)
	}
	if git(ctx, dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return vaultClone{}, fail("KI_VAULT=%s is not a git repo; clone the vault repo there (docs/laptop-setup.md)", dir)
	}
	remote := git(ctx, dir, "remote", "get-url", "origin")
	if remote == "" {
		return vaultClone{}, fail("KI_VAULT=%s has no origin remote", dir)
	}
	if vs := ki.Resolve(h.cfg, remote, nil); vs.Identity != nil {
		if !vs.Identity.Vault {
			return vaultClone{}, fail("the vault repo is listed under identity %q with vault: false; fix projects.yaml", vs.Identity.Name)
		}
		if p := ki.IdentityProblem(vs, git(ctx, dir, "config", "user.email")); p != "" {
			return vaultClone{}, fail("in the vault, %s", p)
		}
	}
	if rebaseInProgress(ctx, dir) {
		return vaultClone{}, fail("a rebase is in progress in %s; finish it first:\n  git -C %q status  # then add the resolved files, git rebase --continue, and run ki vault push again", dir, dir)
	}
	branch := git(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if branch == "" {
		return vaultClone{}, fail("KI_VAULT=%s is not on a branch", dir)
	}
	return vaultClone{dir: dir, branch: branch}, nil
}

// vaultSync commits whatever changed, pulls with rebase and pushes. It returns
// what happened as lines for the caller to print or hand to an agent; the MCP
// server must not write to stdout itself.
func (a *App) vaultSync(ctx context.Context, v vaultClone, msg string) ([]string, error) {
	var lines []string
	if out, err := gitRun(ctx, v.dir, commandTimeout, "add", "-A"); err != nil {
		return lines, fmt.Errorf("%w: %s", err, out)
	}
	staged, err := gitRun(ctx, v.dir, commandTimeout, "diff", "--cached", "--name-only")
	if err != nil {
		return lines, fmt.Errorf("%w: %s", err, staged)
	}
	if staged == "" {
		lines = append(lines, "nothing new to commit")
	} else {
		if msg == "" {
			msg = defaultVaultMessage(time.Now())
		}
		if out, err := gitRun(ctx, v.dir, commandTimeout, "commit", "-q", "-m", msg); err != nil {
			return lines, fmt.Errorf("%w: %s", err, out)
		}
		lines = append(lines, fmt.Sprintf("committed %d file(s): %s", len(strings.Split(staged, "\n")), msg))
	}

	if out, err := gitRun(ctx, v.dir, vaultTimeout, "pull", "-q", "--rebase", "origin", v.branch); err != nil {
		if rebaseInProgress(ctx, v.dir) {
			return lines, fail("pull stopped on a conflict:\n%s\nopen the files git lists, keep the right version, then:\n  git -C %q add -A && git -C %q rebase --continue && ki vault push", out, v.dir, v.dir)
		}
		return lines, fail("pull failed: %s", out)
	}
	if out, err := gitRun(ctx, v.dir, vaultTimeout, "push", "-q", "origin", v.branch); err != nil {
		return lines, fail("push failed: %s", out)
	}
	lines = append(lines, fmt.Sprintf("vault at %s: %s", git(ctx, v.dir, "rev-parse", "--short", "HEAD"), git(ctx, v.dir, "log", "-1", "--format=%s")))
	return lines, nil
}

// defaultVaultMessage names the machine and the time, which is all a
// hand-written batch of notes has in common.
func defaultVaultMessage(now time.Time) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	host, _, _ = strings.Cut(host, ".")
	return "vault: " + host + " " + now.Format("2006-01-02 15:04")
}

// rebaseInProgress reports whether git left a rebase half done in dir; the
// git dir may live outside the work tree, so the paths come from git itself.
func rebaseInProgress(ctx context.Context, dir string) bool {
	for _, state := range []string{"rebase-merge", "rebase-apply"} {
		p := git(ctx, dir, "rev-parse", "--git-path", state)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// gitRun runs git in dir and returns its combined output. Unlike git(), a
// failure is an error, because a vault push must stop on the first one. Git
// never prompts for credentials here: a missing login fails instead of hanging
// a skill.
func gitRun(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary, arguments from ki itself
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s in %s: %w", args[0], dir, err)
	}
	return text, nil
}
