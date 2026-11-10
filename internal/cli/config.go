package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// configLink is the link ki setup config writes.
func (a *App) configLink() string { return filepath.Join(a.confDir(), ki.ConfigLink) }

// configDir finds the configuration directory: KI_CONFIG, else the link, else
// the harness checkout's deploy/config. The harness path is passed in so the
// fallback never resolves the checkout twice; without a checkout there is no
// fallback and a missing configuration is an error.
func (a *App) configDir(harness string) (dir, source string, err error) {
	if d := a.Getenv("KI_CONFIG"); d != "" {
		resolved, err := filepath.EvalSymlinks(filepath.Clean(d))
		if err != nil {
			return "", "", fail("KI_CONFIG=%s: %v", d, err)
		}
		return resolved, ki.ConfigFromEnv, nil
	}
	if _, err := os.Lstat(a.configLink()); err == nil {
		resolved, err := filepath.EvalSymlinks(a.configLink())
		if err != nil {
			return "", "", fail("%s points nowhere: %v; run `ki setup config <dir>` again", a.short(a.configLink()), err)
		}
		return resolved, ki.ConfigFromLink, nil
	}
	if harness == "" {
		return "", "", fail("no configuration directory: run `ki setup config <dir>` or set KI_CONFIG (INSTALL.md, step 3)")
	}
	return filepath.Join(harness, "deploy", "config"), ki.ConfigFromHarness, nil
}

// configPath is a file inside the configuration directory.
func (a *App) configPath(harness, name string) (string, error) {
	dir, _, err := a.configDir(harness)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// mcpURL is the knowledge base: KI_MCP_URL, else knowledge_base.url in the
// configuration's config.yaml. There is no built-in server: a configuration
// without a URL yields "" and ki runs in local mode, without the knowledge base.
func (a *App) mcpURL(h *here) string {
	if u := a.Getenv("KI_MCP_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if h != nil {
		return h.settings.KnowledgeBase.URL
	}
	return ""
}

// noURL is the refusal of a command that cannot work without a knowledge base.
func noURL(h *here, command string) error {
	return fail("%s needs a knowledge base: set knowledge_base.url in %s or export KI_MCP_URL (see docs/self-hosting.md)", command, filepath.Join(h.config, ki.ConfigFile))
}

// setupConfig validates a configuration directory and links it.
func (a *App) setupConfig(args []string) error {
	if len(args) != 1 {
		return fail("usage: ki setup config <dir>   (a directory with %s)", strings.Join(ki.ConfigFiles, ", "))
	}
	dir := args[0]
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(a.home(), dir[2:])
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.Dir, dir)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err == nil {
		resolved, err = filepath.Abs(resolved) // a.Dir is empty outside tests, so a relative path stays relative
	}
	if err != nil {
		return fail("%s: %v", dir, err)
	}
	if missing := ki.MissingConfigFiles(resolved); len(missing) > 0 {
		return fail("%s is not a configuration directory: missing %s (start from examples/config-minimal in the kio release that matches `ki version`, see INSTALL.md)", a.short(resolved), strings.Join(missing, ", "))
	}
	if _, err := projects.Load(filepath.Join(resolved, ki.ProjectsFile)); err != nil {
		return fail("%s: %v", filepath.Join(a.short(resolved), ki.ProjectsFile), err)
	}
	s, err := ki.LoadSettings(filepath.Join(resolved, ki.ConfigFile))
	if err != nil {
		return fail("%v", err)
	}
	link := a.configLink()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(link), err)
	}
	_ = os.Remove(link) // replace an older link; a real directory there is left alone by Symlink's error below
	if err := os.Symlink(resolved, link); err != nil {
		return fmt.Errorf("linking %s: %w", link, err)
	}
	detail := "knowledge base " + s.KnowledgeBase.URL
	if s.KnowledgeBase.URL == "" {
		detail = "no knowledge base: sessions start without ki-kb"
	}
	fmt.Fprintf(a.Stdout, "linked %s -> %s (%s)\n", a.short(link), a.short(resolved), detail)
	return nil
}
