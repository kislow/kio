package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configDirFrom copies the harness's deploy/config into dir and adds a
// config.yaml with the URL, the shape `ki setup config` expects.
func configDirFrom(t *testing.T, e *env, dir, url string) string {
	t.Helper()
	src := filepath.Join(e.harness, "deploy", "config")
	for _, f := range []string{"projects.yaml", "models.env", "aliases.yaml", "mcp-config.json"} {
		data, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			data = []byte("# example\n")
		}
		write(t, filepath.Join(dir, f), string(data))
	}
	if url != "" {
		write(t, filepath.Join(dir, "config.yaml"), "knowledge_base:\n  url: "+url+"\n")
	}
	return dir
}

func TestConfigFallsBackToTheHarness(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(e.harness)
	if out["config"] != filepath.Join(want, "deploy", "config") || out["config_source"] != "harness" {
		t.Fatalf("got %v / %v", out["config"], out["config_source"])
	}
	if out["knowledge_base"] != "https://kb.example" {
		t.Fatalf("the fallback layout reads deploy/config/config.yaml: %v", out["knowledge_base"])
	}

	if err := os.Remove(filepath.Join(e.harness, "deploy", "config", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["knowledge_base"] != nil {
		t.Fatalf("no config.yaml means no knowledge base, got %v", out["knowledge_base"])
	}
}

// There is no built-in knowledge base: the harness's own deploy/config without
// a URL runs in local mode, the same as a linked configuration without one.
func TestFallbackLayoutWithoutURLRunsLocally(t *testing.T) {
	e := newEnv(t, "https://github.com/someone/else")
	if err := os.Remove(filepath.Join(e.harness, "deploy", "config", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil || e.execd == nil {
		t.Fatalf("want a session, got %v (exec %v)", err, e.execd)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"doctor"}); err != nil && strings.Contains(e.out.String(), "✗ knowledge base") {
		t.Fatalf("doctor must not fail on the missing URL: %v\n%s", err, e.out)
	}
	if out := e.out.String(); !strings.Contains(out, "knowledge base") || !strings.Contains(out, "not configured (optional)") {
		t.Fatalf("doctor:\n%s", out)
	}
}

func TestSetupConfigLinksAndWins(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	dir := configDirFrom(t, e, filepath.Join(t.TempDir(), "my-config"), "https://kb.example")
	if err := e.app.Run(context.Background(), []string{"setup", "config", dir}); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(e.vars["KI_CONFIG_DIR"], "config")); err != nil || !strings.HasSuffix(target, "my-config") {
		t.Fatalf("link: %q %v", target, err)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["config_source"] != "link" || out["knowledge_base"] != "https://kb.example" {
		t.Fatalf("got %v / %v", out["config_source"], out["knowledge_base"])
	}

	// KI_CONFIG beats the link; KI_MCP_URL beats config.yaml.
	other := configDirFrom(t, e, filepath.Join(t.TempDir(), "other"), "https://other.example")
	e.vars["KI_CONFIG"] = other
	e.vars["KI_MCP_URL"] = "https://env.example/"
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["config_source"] != "env" || out["knowledge_base"] != "https://env.example" {
		t.Fatalf("got %v / %v", out["config_source"], out["knowledge_base"])
	}
}

func TestSetupConfigRefusals(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	ctx := context.Background()
	for name, tc := range map[string]struct {
		prepare func(dir string)
		want    string
	}{
		"no args":       {func(string) {}, "usage"},
		"missing files": {func(dir string) { write(t, filepath.Join(dir, "projects.yaml"), projectsYAML) }, "missing"},
		"bad projects": {func(dir string) {
			configDirFrom(t, e, dir, "https://kb.example")
			write(t, filepath.Join(dir, "projects.yaml"), "identities: {x: {github_user: a, email: b, credential: c}}\n")
		}, "projects.yaml"},
		"bad config": {func(dir string) {
			configDirFrom(t, e, dir, "")
			write(t, filepath.Join(dir, "config.yaml"), "knowledge_bse: {url: https://x}\n")
		}, "config.yaml"},
	} {
		dir := filepath.Join(t.TempDir(), "cfg")
		tc.prepare(dir)
		args := []string{"setup", "config", dir}
		if name == "no args" {
			args = []string{"setup", "config"}
		}
		err := e.app.Run(ctx, args)
		if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a user error containing %q, got %v", name, tc.want, err)
		}
		if _, err := os.Lstat(filepath.Join(e.vars["KI_CONFIG_DIR"], "config")); err == nil {
			t.Fatalf("%s: a refused directory must not be linked", name)
		}
	}
}

func TestLinkedConfigWithoutURLRunsLocally(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	dir := configDirFrom(t, e, filepath.Join(t.TempDir(), "my-config"), "https://kb.example")
	if err := e.app.Run(context.Background(), []string{"setup", "config", dir}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "config.yaml"), "knowledge_base: {url: ''}\n")
	gitIn(t, e.repo, "config", "user.email", "alice@work.example")
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil || e.execd == nil {
		t.Fatalf("a linked configuration without a URL starts a session: %v", err)
	}
	e.out.Reset()
	_ = e.app.Run(context.Background(), []string{"doctor"})
	if !strings.Contains(e.out.String(), "not configured (optional)") || !strings.Contains(e.out.String(), "✓ config") {
		t.Fatalf("doctor must say the knowledge base is optional and show the linked config:\n%s", e.out)
	}
}

func TestDoctorNotesTheFallbackConfig(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	kb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(kb.Close)
	e.vars["KI_MCP_URL"] = kb.URL
	_ = e.app.Run(context.Background(), []string{"doctor"})
	if !strings.Contains(e.out.String(), "· config") || !strings.Contains(e.out.String(), "ki setup config") {
		t.Fatalf("the harness's own config is a note with the way out:\n%s", e.out)
	}
}

func TestUpdatePullsTheLinkedConfigToo(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	gitHarness(t, e)
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }
	ctx := context.Background()

	cfg := configDirFrom(t, e, filepath.Join(filepath.Dir(e.harness), "my-config"), "https://kb.example")
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := gitRun(ctx, dir, commandTimeout, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	origin := filepath.Join(filepath.Dir(e.harness), "config-origin.git")
	run("", "init", "-q", "--bare", "-b", "main", origin)
	run(cfg, "init", "-q", "-b", "main")
	run(cfg, "-c", "user.name=t", "-c", "user.email=t@x", "add", "-A")
	run(cfg, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "seed")
	run(cfg, "remote", "add", "origin", origin)
	run(cfg, "push", "-q", "-u", "origin", "main")
	run(cfg, "remote", "set-head", "origin", "main")
	if err := e.app.Run(ctx, []string{"setup", "config", cfg}); err != nil {
		t.Fatal(err)
	}

	e.out.Reset()
	if err := e.app.Run(ctx, []string{"update"}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), "kio already at") || !strings.Contains(e.out.String(), "configuration already at") {
		t.Fatalf("both checkouts must be pulled:\n%s", e.out)
	}

	write(t, filepath.Join(cfg, "scratch.txt"), "x\n")
	if err := e.app.Run(ctx, []string{"update"}); !isUserError(err) || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("a dirty configuration checkout must refuse: %v", err)
	}
}

func TestSetupConfigAcceptsTildeAndRelativePaths(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	home := e.vars["HOME"]
	configDirFrom(t, e, filepath.Join(home, "my-config"), "https://kb.example")
	if err := e.app.Run(context.Background(), []string{"setup", "config", "~/my-config"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "knowledge base https://kb.example") {
		t.Fatalf("got %q", e.out)
	}

	// A relative path is taken from the working directory, and the link is replaced.
	configDirFrom(t, e, filepath.Join(e.repo, "cfg"), "https://two.example")
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"setup", "config", "cfg"}); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(e.vars["KI_CONFIG_DIR"], "config"))
	if err != nil || !strings.HasSuffix(target, "cfg") {
		t.Fatalf("link: %q %v", target, err)
	}

	// A dangling link is reported with the way out, by every command.
	if err := os.Remove(filepath.Join(e.repo, "cfg", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(e.repo, "cfg")); err != nil {
		t.Fatal(err)
	}
	err = e.app.Run(context.Background(), []string{"status"})
	if !isUserError(err) || !strings.Contains(err.Error(), "points nowhere") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateAndMeccaReadTheLinkedConfig(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	dir := configDirFrom(t, e, filepath.Join(t.TempDir(), "my-config"), "https://kb.example")
	write(t, filepath.Join(dir, "projects.yaml"), twoIdentitiesYAML)
	if err := e.app.Run(context.Background(), []string{"setup", "config", dir}); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"validate"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "2 project(s), 2 identit(ies)") {
		t.Fatalf("validate must read the linked configuration:\n%s", e.out)
	}
}
