package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func (a *App) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

func (a *App) serviceCommand(ctx context.Context, bin string, args ...string) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return fail("%s not found on PATH", bin)
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- path is launchctl or systemctl selected by the platform
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("%s: %w: %s", bin, e, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *App) serviceOutput(ctx context.Context, bin string, args ...string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("looking up service manager: %w", err)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput() // #nosec G204 -- path is launchctl or systemctl selected by the platform
	return string(out), err
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
