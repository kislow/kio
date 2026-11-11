package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// identityRestored is added to a job's detail when ki put back git config a worker changed.
const identityRestored = "restored git identity in .git/config"

// commitIdentity is who a worker's commits are authored and committed by.
type commitIdentity struct{ name, email string }

// resolveCommitIdentity reads the identity git would commit as in the repo
// root, outside any sandbox, so includeIf and the user's own setup apply. It
// refuses an unset email and one that is not the session's identity: no
// commit is made under the wrong author.
func resolveCommitIdentity(ctx context.Context, p *prepared) (commitIdentity, error) {
	id := commitIdentity{name: git(ctx, p.here.root, "config", "user.name"), email: git(ctx, p.here.root, "config", "user.email")}
	if id.email == "" {
		return id, errors.New("git user.email is not set in this repo, so a worker would commit under the wrong author; set it, or fix your includeIf config")
	}
	if problem := ki.IdentityProblem(p.session, id.email); problem != "" {
		return id, errors.New(problem)
	}
	return id, nil
}

// env is the identity as git reads it from the environment; an unset name is left to git.
func (c commitIdentity) env() map[string]string {
	env := map[string]string{"GIT_AUTHOR_EMAIL": c.email, "GIT_COMMITTER_EMAIL": c.email}
	if c.name != "" {
		env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"] = c.name, c.name
	}
	return env
}

// localUser is the user.name and user.email of the shared repository config.
type localUser struct{ name, email configValue }

// configValue is a git config value that may be unset.
type configValue struct {
	value string
	set   bool
}

func readLocalUser(ctx context.Context, root string) localUser {
	return localUser{name: readLocalConfig(ctx, root, "user.name"), email: readLocalConfig(ctx, root, "user.email")}
}

func readLocalConfig(ctx context.Context, root, key string) configValue {
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "--get", key) // #nosec G204 -- fixed binary, key from ki itself
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return configValue{}
	}
	return configValue{value: strings.TrimSuffix(string(out), "\n"), set: true}
}

// restoreLocalUser puts back the user.* values of the shared repository config
// a worker changed and reports whether it had to.
func restoreLocalUser(ctx context.Context, root string, before localUser) bool {
	restored := restoreLocalConfig(ctx, root, "user.name", before.name)
	return restoreLocalConfig(ctx, root, "user.email", before.email) || restored
}

func restoreLocalConfig(ctx context.Context, root, key string, want configValue) bool {
	if readLocalConfig(ctx, root, key) == want {
		return false
	}
	args := []string{"config", "--local", "--unset", key}
	if want.set {
		args = []string{"config", "--local", key, want.value}
	}
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary, arguments from ki itself
	cmd.Dir = root
	return cmd.Run() == nil
}
