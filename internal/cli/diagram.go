package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// diagramTimeout bounds one `ki diagram` run; rendering starts a browser.
const diagramTimeout = 10 * time.Minute

// diagramCheckTimeout bounds the requirements check `ki doctor` runs.
const diagramCheckTimeout = 15 * time.Second

// diagramTool is the node program behind `ki diagram`, in the harness
// checkout. Building and merging tldraw drawings needs tldraw's own schema
// and store, and rendering needs tldraw in a browser, so this part is not Go.
type diagramTool struct {
	harness, dir, script, node string
}

func (a *App) findDiagramTool() (diagramTool, error) {
	h, err := a.harness()
	if err != nil {
		return diagramTool{}, err
	}
	if h == "" {
		return diagramTool{}, fail("ki diagram needs a checkout of the repository (its node tool is not in the binary): clone it and run `make install-cli`")
	}
	dir := filepath.Join(h, "diagram")
	t := diagramTool{harness: h, dir: dir, script: filepath.Join(dir, "bin", "ki-diagram.mjs")}
	if _, err := os.Stat(t.script); err != nil {
		return t, fail("%s has no diagram tool; run ki update", a.short(h))
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return t, fail("ki diagram needs node 22 or newer on PATH; then run: make -C %s diagram", h)
	}
	t.node = node
	return t, nil
}

// diagram hands the arguments to the node tool, which prints its own results
// and problems; paths are taken relative to where ki was started.
func (a *App) diagram(ctx context.Context, args []string) error {
	t, err := a.findDiagramTool()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, diagramTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.node, append([]string{t.script}, args...)...) // #nosec G204 G702 -- our own script; arguments are the user's
	cmd.Dir = a.Dir
	cmd.Stdout, cmd.Stderr = a.Stdout, a.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("ki diagram stopped after %s: %w", diagramTimeout, ctx.Err())
	case errors.As(err, &exit):
		return fail("ki diagram failed (exit %d)", exit.ExitCode())
	default:
		return fmt.Errorf("running %s: %w", t.script, err)
	}
}

// requirement is one line of `ki-diagram requirements --json`.
type requirement struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// checkDiagrams tells whether `ki diagram` can build and render here. Not
// setting it up is a choice, not a problem; a half-done setup is.
func (a *App) checkDiagrams(ctx context.Context, c *checks) {
	t, err := a.findDiagramTool()
	if t.script == "" {
		return // no harness checkout: already reported
	}
	if err != nil {
		c.note("ki diagram", "node is not installed; needed only for tldraw drawings (docs/diagrams.md)")
		return
	}
	setup := "make -C " + t.harness + " diagram"
	if _, err := os.Stat(filepath.Join(t.dir, "node_modules")); err != nil {
		c.note("ki diagram", "not set up; for tldraw drawings run: "+setup)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, diagramCheckTimeout)
	defer cancel()
	// The tool exits 1 when a check fails, and still prints them all.
	out, runErr := exec.CommandContext(ctx, t.node, t.script, "requirements", "--json").Output() // #nosec G204 -- our own script, fixed arguments
	var reqs []requirement
	if err := json.Unmarshal(out, &reqs); err != nil || len(reqs) == 0 {
		detail := "requirements check gave no answer"
		if runErr != nil {
			detail += ": " + runErr.Error()
		}
		c.report(false, "ki diagram", detail, setup)
		return
	}
	for _, r := range reqs {
		c.report(r.OK, "diagram "+r.Name, r.Detail, r.Fix)
	}
}
