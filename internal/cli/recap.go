package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kislow/kio/internal/standup"
)

const recapUsage = "usage: ki recap [--identity NAME] [--since YYYY-MM-DD] [--json]"

type recapArgs struct {
	identity, since string
	json            bool
}

func parseRecapArgs(args []string) (recapArgs, error) {
	var sa recapArgs
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--json":
			sa.json = true
		case (a == "--identity" || a == "--since") && i+1 < len(args):
			if a == "--identity" {
				sa.identity = args[i+1]
			} else {
				sa.since = args[i+1]
			}
			i++
		default:
			return sa, fail("%s", recapUsage)
		}
	}
	return sa, nil
}

// recap prints one identity's activity since the previous working day, read
// with that identity's own gh token. It never writes anywhere.
func (a *App) recap(ctx context.Context, args []string) error {
	sa, err := parseRecapArgs(args)
	if err != nil {
		return err
	}
	h, err := a.current(ctx, nil)
	if err != nil {
		return err
	}
	id, err := standup.SelectIdentity(h.cfg, sa.identity, h.session.Identity)
	if err != nil {
		return fail("%v", err)
	}
	now := time.Now()
	since, err := standup.Since(now, sa.since)
	if err != nil {
		return fail("%v", err)
	}
	token := ghToken(ctx, id.GitHubUser)
	if token == "" {
		return fail("gh is not logged in as %s; run `gh auth login`", id.GitHubUser)
	}
	client := standup.NewClient(a.Getenv("KI_GITHUB_GRAPHQL_URL"), token)
	rep, err := standup.Collect(ctx, client, standup.NewScope(h.cfg, id), a.Getenv("KI_VAULT"), since, now)
	if errors.Is(err, standup.ErrIdentity) {
		return fail("%v", err)
	}
	if err != nil {
		return fmt.Errorf("collecting recap: %w", err)
	}
	if !sa.json {
		if err := rep.WriteText(a.Stdout); err != nil {
			return fmt.Errorf("printing recap: %w", err)
		}
		return nil
	}
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return fmt.Errorf("writing recap: %w", err)
	}
	return nil
}
