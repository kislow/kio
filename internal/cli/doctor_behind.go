package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kislow/kio/internal/ki"
)

// checkBehind warns when the harness or a linked configuration checkout is
// behind its upstream as last fetched; doctor makes no network call, so
// `ki update` is what refreshes the remote.
func (a *App) checkBehind(ctx context.Context, c *checks, h *here) {
	if h.harness != "" {
		a.warnIfBehind(ctx, c, "harness", h.harness)
	}
	if h.configSource == ki.ConfigFromHarness {
		return
	}
	if top := git(ctx, h.config, "rev-parse", "--show-toplevel"); top != "" {
		a.warnIfBehind(ctx, c, "config", top)
	}
}

func (a *App) warnIfBehind(ctx context.Context, c *checks, label, dir string) {
	upstream := git(ctx, dir, "rev-parse", "--abbrev-ref", "@{upstream}")
	if upstream == "" {
		return
	}
	behind, err := strconv.Atoi(git(ctx, dir, "rev-list", "--count", "HEAD..@{upstream}"))
	if err != nil || behind == 0 {
		return
	}
	noun := "commits"
	if behind == 1 {
		noun = "commit"
	}
	c.warn(label+" behind", fmt.Sprintf("%s is %d %s behind %s; run ki update", a.short(dir), behind, noun, upstream))
}
