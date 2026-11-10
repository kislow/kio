package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const configInstructions = "# Owner conventions\n\nUse sentence case.\n"

// writeConfigInstructions adds agent/instructions.md to the configuration
// directory the test env falls back to, the harness's deploy/config.
func writeConfigInstructions(t *testing.T, e *env) string {
	t.Helper()
	path := filepath.Join(e.harness, "deploy", "config", ki.InstructionsFile)
	write(t, path, configInstructions)
	return path
}

func TestInstructionsTextLayersTheConfigAfterTheHarness(t *testing.T) {
	for name, tt := range map[string]struct {
		config string
		want   string
	}{
		"absent":  {"", "# Instructions\n"},
		"empty":   {" \n", "# Instructions\n"},
		"present": {configInstructions, "# Instructions\n\n# Owner conventions\n\nUse sentence case.\n"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "")
			if tt.config != "" {
				write(t, filepath.Join(e.harness, "deploy", "config", ki.InstructionsFile), tt.config)
			}
			h, err := e.app.current(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := h.instructionsText()
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestClaudeSessionAppendsOneFileWithBothTexts(t *testing.T) {
	e := newEnv(t, "git@github.com:work/app.git")
	ctx := context.Background()
	root, _ := filepath.EvalSymlinks(e.harness)
	harnessFile := filepath.Join(root, ki.InstructionsFile)

	p, err := e.app.prepare(ctx, ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.instructions != harnessFile {
		t.Fatalf("without a config file the harness's is passed as it is, got %s", p.instructions)
	}
	if files, _ := filepath.Glob(filepath.Join(e.vars["KI_CONFIG_DIR"], "run", "instructions-*")); len(files) != 0 {
		t.Fatalf("no run file without a config file: %v", files)
	}

	writeConfigInstructions(t, e)
	p, err = e.app.prepare(ctx, ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.instructions) != filepath.Join(e.vars["KI_CONFIG_DIR"], "run") {
		t.Fatalf("the combined file belongs under run/: %s", p.instructions)
	}
	got, _ := os.ReadFile(p.instructions)
	if string(got) != "# Instructions\n\n# Owner conventions\n\nUse sentence case.\n" {
		t.Fatalf("combined file:\n%s", got)
	}
	if info, err := os.Stat(p.instructions); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the run file must be private: %v %v", info, err)
	}
	launch := ki.BuildLaunch(p.launchInput(ki.RunArgs{Agent: "claude"}, "", nil))
	i := slices.Index(launch.Argv, "--append-system-prompt-file")
	if i < 0 || launch.Argv[i+1] != p.instructions {
		t.Fatalf("argv %v", launch.Argv)
	}

	old := time.Now().Add(-2 * runFileMaxAge)
	stale := filepath.Join(e.vars["KI_CONFIG_DIR"], "run", "instructions-stale.md")
	write(t, stale, "x")
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.prepare(ctx, ki.RunArgs{Agent: "claude", Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("an old instructions file is pruned like the MCP configs: %v", err)
	}
	if _, err := os.Stat(p.instructions); err != nil {
		t.Fatalf("the current file stays: %v", err)
	}
}

func TestSetupCodexAndCursorCarryTheConfigText(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.vars["CODEX_HOME"] = filepath.Join(e.vars["HOME"], "codex")
	t.Setenv("HOME", e.vars["HOME"])
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.vars["HOME"], ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.vars["HOME"], ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	write(t, filepath.Join(e.repo, ".gitignore"), ".cursor/\n")
	ctx := context.Background()

	check := func(label string, wantConfig bool) {
		t.Helper()
		agents, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "AGENTS.md"))
		rule, _ := os.ReadFile(filepath.Join(e.repo, cursorRule))
		for name, got := range map[string]string{"AGENTS.md": string(agents), "cursor rule": string(rule)} {
			if !strings.Contains(got, "# Instructions") {
				t.Errorf("%s: %s lacks the harness text:\n%s", label, name, got)
			}
			has := strings.Contains(got, "Use sentence case.")
			if has != wantConfig || has && strings.Index(got, "# Owner conventions") < strings.Index(got, "# Instructions") {
				t.Errorf("%s: %s config text present=%v, want %v, after the harness's:\n%s", label, name, has, wantConfig, got)
			}
		}
	}
	setup := func() {
		t.Helper()
		for _, args := range [][]string{{"setup", "codex"}, {"cursor"}} {
			if err := e.app.Run(ctx, args); err != nil {
				t.Fatal(err)
			}
		}
	}
	setup()
	check("absent", false)

	writeConfigInstructions(t, e)
	setup()
	check("present", true)
}

func TestStatusAndDoctorNameTheConfigInstructions(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	status := func() map[string]any {
		e.out.Reset()
		if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got, ok := status()["config_instructions"]; !ok || got != nil {
		t.Fatalf("no file reports null, got %v (present %v)", got, ok)
	}
	path := writeConfigInstructions(t, e)
	want, _ := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(path)))
	if got := status()["config_instructions"]; got != filepath.Join(want, ki.InstructionsFile) {
		t.Fatalf("got %v", got)
	}
	e.out.Reset()
	_ = e.app.Run(context.Background(), []string{"doctor"})
	if !strings.Contains(e.out.String(), "· instructions") {
		t.Fatalf("doctor must name the file:\n%s", e.out)
	}
}

// Each worker carries the text its interactive session gets: Claude Code as
// the appended file, Codex and Cursor in front of the task. agy gets none.
func TestDelegatedWorkersCarryTheCombinedInstructions(t *testing.T) {
	const combined = "# Instructions\n\n# Owner conventions\n\nUse sentence case."
	for _, tt := range []struct {
		agent, result string
		inPrompt      bool
	}{
		{"claude", claudeDone, false},
		{"codex", codexEvents, true},
		{"agent", cursorBlocked, true},
	} {
		t.Run(tt.agent, func(t *testing.T) {
			d := newDelegateEnv(t)
			d.worker["RESULT"] = tt.result
			writeConfigInstructions(t, d.env)
			if err := d.delegate("--agent", tt.agent, "--task", "fix it", "--force", "--json"); err != nil {
				t.Fatal(err)
			}
			args := d.args(t)
			task := args[len(args)-1]
			if tt.inPrompt {
				if !strings.HasPrefix(task, combined) || !strings.Contains(task, "fix it") {
					t.Fatalf("the prompt must open with both texts:\n%s", task)
				}
				return
			}
			i := slices.Index(args, "--append-system-prompt-file")
			if i < 0 {
				t.Fatalf("args %v", args)
			}
			got, _ := os.ReadFile(args[i+1])
			if !strings.HasPrefix(string(got), combined) || strings.Contains(task, "Owner conventions") {
				t.Fatalf("file %q, prompt %q", got, task)
			}
		})
	}
}

func TestDelegatedCursorWorkerGetsTheHarnessTextAlone(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = cursorBlocked
	if err := d.delegate("--agent", "agent", "--task", "fix it", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	args := d.args(t)
	if task := args[len(args)-1]; !strings.HasPrefix(task, "# Instructions") {
		t.Fatalf("without a config file the prompt opens with the harness text only:\n%s", task)
	}
}
