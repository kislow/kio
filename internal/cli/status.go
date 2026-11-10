package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
)

type statusIdentity struct {
	Name       string `json:"name"`
	GitHubUser string `json:"github_user"`
	Email      string `json:"email"`
	Vault      bool   `json:"vault"`
	Ingest     bool   `json:"ingest"`
}

func (a *App) status(ctx context.Context, args []string) error {
	h, err := a.current(ctx, nil)
	if err != nil {
		return err
	}
	s := h.session
	// A bad KI_MODE is reported, not fatal, so skills can still read status.
	mode, modeErr := ki.ParseMode(a.Getenv("KI_MODE"))
	email := ""
	if h.root != "" {
		email = git(ctx, h.root, "config", "user.email")
	}

	if len(args) > 0 && args[0] == "--json" {
		out := map[string]any{
			"repo": nilIfEmpty(h.root), "harness": h.harness, "config": h.config, "config_source": h.configSource,
			"config_instructions": nilIfEmpty(h.configInstructions()),
			"knowledge_base":      nilIfEmpty(a.mcpURL(h)), "projects": s.Projects,
			"project": nil, "brains": s.Brains, "sensitive": s.Sensitive, "board": nilIfEmpty(s.Board),
			"identity": nil, "identity_ok": s.Folder || ki.IdentityProblem(s, email) == "", "vault_dir": nilIfEmpty(a.Getenv("KI_VAULT")),
			"unmapped": s.Unmapped && !s.Folder, "mode": nilIfEmpty(string(mode)),
			"folder": s.Folder, "folder_identities": nil,
		}
		if len(h.mixed) > 0 {
			out["folder_identities"] = h.mixed
		}
		if len(s.Projects) > 0 {
			out["project"] = s.Projects[0]
		}
		if s.Identity != nil {
			id := s.Identity
			out["identity"] = statusIdentity{id.Name, id.GitHubUser, id.Email, id.Vault, id.Ingest}
		}
		a.statusPolicy(out)
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("writing status: %w", err)
		}
		return nil
	}

	fmt.Fprintf(a.Stdout, "repo:      %s\n", orDefault(h.root, "<not in a git repo>"))
	fmt.Fprintf(a.Stdout, "projects:  %s\n", orDefault(strings.Join(s.Projects, ", "), "<not in projects.yaml>"))
	brains := strings.Join(s.Brains, ", ")
	if s.Unmapped && !s.Folder {
		brains += " (unmapped repo: ki needs --force here)"
	}
	fmt.Fprintf(a.Stdout, "brains:    %s\n", brains)
	fmt.Fprintf(a.Stdout, "sensitive: %v\n", s.Sensitive)
	if f := h.configInstructions(); f != "" {
		fmt.Fprintf(a.Stdout, "instructions: %s (after the harness's)\n", f)
	}
	if modeErr != nil {
		fmt.Fprintf(a.Stdout, "mode:      KI_MODE: %v\n", modeErr)
	} else {
		fmt.Fprintf(a.Stdout, "mode:      %s\n", mode)
	}
	if len(h.mixed) > 0 {
		fmt.Fprintf(a.Stdout, "folder:    %s\n", h.mixedProblem())
	}
	fmt.Fprintf(a.Stdout, "subagents: %s\n", a.subagentPolicy())
	if s.Identity != nil && s.Folder {
		fmt.Fprintf(a.Stdout, "identity:  %s (%s, %s) from the repos in this folder\n", s.Identity.Name, s.Identity.GitHubUser, s.Identity.Email)
	} else if s.Identity != nil {
		state := "OK"
		if ki.IdentityProblem(s, email) != "" {
			state = "MISMATCH"
		}
		fmt.Fprintf(a.Stdout, "identity:  %s (%s, %s)\n", s.Identity.Name, s.Identity.GitHubUser, s.Identity.Email)
		fmt.Fprintf(a.Stdout, "git email: %s  %s\n", orDefault(email, "<unset>"), state)
	}
	return nil
}

// statusPolicy adds the machine's roles, models and effort to the status, so
// a UI can offer what this machine is set up for. A broken machine file or
// ledger is reported in policy_error, not fatal: status must still print.
func (a *App) statusPolicy(out map[string]any) {
	out["roles"], out["models"], out["effort"] = map[string]ki.RoleState{}, map[string]map[string]string{}, map[string]string{}
	m, err := ki.LoadPolicy(a.machinePath())
	if err != nil {
		out["policy_error"] = err.Error()
		return
	}
	ledger, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		out["policy_error"] = err.Error()
	}
	out["subagents"] = m.SubagentAllow()
	in := ki.PickInput{Ledger: ledger, Now: time.Now(), Installed: func(agent string) bool {
		_, err := exec.LookPath(agent)
		return err == nil
	}}
	out["roles"] = ki.RoleStates(m, in)
	out["planner"] = a.statusPlanner(m)
	if m != nil {
		if m.Models != nil {
			out["models"] = m.Models
		}
		if m.Effort != nil {
			out["effort"] = m.Effort
		}
	}
}

// subagentPolicy is the machine's subagent policy in one line; an unreadable
// machine file is reported there, not fatal.
func (a *App) subagentPolicy() string {
	m, err := ki.LoadPolicy(a.machinePath())
	if err != nil {
		return err.Error()
	}
	return m.SubagentPolicy()
}

// statusPlanner is the agent and model a bare `ki` would start here and why:
// the source (KI_AGENT, roles.planner or the default) and the providers the
// planner role passed over. An unreadable ledger or a role with nothing
// eligible is reported as error, not fatal.
func (a *App) statusPlanner(m *ki.Machine) map[string]any {
	sa, err := a.resolveSession("", m)
	out := map[string]any{"skipped": sa.Notes()}
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["agent"], out["source"] = sa.Agent, sa.Source
	out["model"] = nilIfEmpty(sa.Model("", a.Getenv("KI_MODEL"), m))
	return out
}
