package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kislow/kio"
	"github.com/kislow/kio/internal/ki"
)

const embeddedInstructions = "# Embedded instructions\n"

// noCheckoutEnv is a machine with a release binary: no checkout, a linked
// configuration without a knowledge base, and skills embedded in the app.
func noCheckoutEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, "https://github.com/work/app")
	cfg := configDirFrom(t, e, filepath.Join(t.TempDir(), "my-config"), "")
	write(t, filepath.Join(cfg, "config.yaml"), "# no knowledge base\n")
	e.vars["KI_HOME"] = filepath.Join(t.TempDir(), "nowhere")
	e.app.Version = "v1.2.3"
	e.app.Data = fstest.MapFS{
		"agent/instructions.md":     {Data: []byte(embeddedInstructions)},
		"skills/ki-demo/SKILL.md":   {Data: []byte("# demo\n")},
		"skills/ki-demo/notes/a.md": {Data: []byte("a\n")},
		"skills/ki-other/SKILL.md":  {Data: []byte("# other\n")},
	}
	if err := e.app.Run(context.Background(), []string{"setup", "config", cfg}); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	return e
}

func TestEmbeddedDataHasWhatKiNeeds(t *testing.T) {
	for _, name := range []string{"agent/instructions.md", "agent/knowledge-base.md", "skills/ki-review/SKILL.md", "skills/ki-resolve/standards.md"} {
		if body, err := readData(kio.Data, name); err != nil || body == "" {
			t.Errorf("%s is not embedded: %q, %v", name, body, err)
		}
	}
}

func TestReleaseBinaryWorksWithoutACheckout(t *testing.T) {
	e := noCheckoutEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{{"setup", "machine", "work"}, {"setup", "skills"}, {"status"}, {"status", "--json"}} {
		e.out.Reset()
		if err := e.app.Run(ctx, args); err != nil {
			t.Fatalf("ki %s: %v\n%s", strings.Join(args, " "), err, e.out)
		}
	}
	e.out.Reset()
	_ = e.app.Run(ctx, []string{"doctor"}) // other checks (gh login, agent setup) fail on a bare test machine
	if !strings.Contains(e.out.String(), "✓ checkout") || !strings.Contains(e.out.String(), "embedded (version v1.2.3)") {
		t.Fatalf("doctor names the embedded copy and does not fail it:\n%s", e.out)
	}
}

func TestSessionUsesEmbeddedInstructions(t *testing.T) {
	e := noCheckoutEnv(t)
	p, err := e.app.prepare(context.Background(), ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.instructionsText != embeddedInstructions {
		t.Fatalf("instructions text %q", p.instructionsText)
	}
	got, err := os.ReadFile(p.instructions)
	if err != nil || string(got) != embeddedInstructions {
		t.Fatalf("instructions file %s: %q, %v", p.instructions, got, err)
	}
	if filepath.Dir(p.instructions) != filepath.Join(e.vars["KI_CONFIG_DIR"], "run") {
		t.Fatalf("the embedded text is passed as a run file, got %s", p.instructions)
	}
}

func TestCheckoutWinsOverEmbedded(t *testing.T) {
	e := noCheckoutEnv(t)
	e.vars["KI_HOME"] = e.harness
	p, err := e.app.prepare(context.Background(), ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.instructionsText != "# Instructions\n" {
		t.Fatalf("the checkout's instructions win, got %q", p.instructionsText)
	}
	root, _ := filepath.EvalSymlinks(e.harness)
	if p.instructions != filepath.Join(root, ki.InstructionsFile) {
		t.Fatalf("the checkout's file is passed, got %s", p.instructions)
	}
}

func TestNoCheckoutAndNoConfigurationIsAClearError(t *testing.T) {
	e := newEnv(t, "")
	e.vars["KI_HOME"] = filepath.Join(t.TempDir(), "nowhere")
	err := e.app.Run(context.Background(), []string{"status"})
	if !isUserError(err) || !strings.Contains(err.Error(), "ki setup config") {
		t.Fatalf("got %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"setup", "skills"}); err != nil {
		t.Fatalf("skills need no configuration: %v", err)
	}
}

func TestSetupSkillsLinksTheEmbeddedCopy(t *testing.T) {
	e := noCheckoutEnv(t)
	if err := e.app.Run(context.Background(), []string{"setup", "skills"}); err != nil {
		t.Fatal(err)
	}
	home := e.vars["HOME"]
	want := filepath.Join(e.vars["KI_CONFIG_DIR"], "skills", "v1.2.3")
	for _, dir := range []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills")} {
		for _, name := range []string{"ki-demo", "ki-other"} {
			got, err := os.Readlink(filepath.Join(dir, name))
			if err != nil || got != filepath.Join(want, name) {
				t.Fatalf("%s/%s -> %q, %v", dir, name, got, err)
			}
		}
	}
	if body, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "ki-demo", "notes", "a.md")); err != nil || string(body) != "a\n" {
		t.Fatalf("nested skill files are written: %q, %v", body, err)
	}

	e.app.Version = "v1.3.0"
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"setup", "skills"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(home, ".claude", "skills", "ki-demo")); got != filepath.Join(e.vars["KI_CONFIG_DIR"], "skills", "v1.3.0", "ki-demo") {
		t.Fatalf("an upgrade relinks to the new version, got %s", got)
	}
}

func TestSetupSkillsKeepsWhatIsNotALink(t *testing.T) {
	e := noCheckoutEnv(t)
	mine := filepath.Join(e.vars["HOME"], ".claude", "skills", "ki-demo")
	write(t, filepath.Join(mine, "SKILL.md"), "mine\n")
	if err := e.app.Run(context.Background(), []string{"setup", "skills"}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(body) != "mine\n" {
		t.Fatalf("a directory that is not a link was overwritten: %q", body)
	}
	if !strings.Contains(e.out.String(), "exists and is not a link") {
		t.Fatalf("the skip is reported:\n%s", e.out)
	}
	if _, err := os.Readlink(filepath.Join(e.vars["HOME"], ".claude", "skills", "ki-other")); err != nil {
		t.Fatalf("the other skills are still linked: %v", err)
	}
}

func TestSetupSkillsLinksTheCheckoutAndDropsRetiredLinks(t *testing.T) {
	e := newEnv(t, "")
	write(t, filepath.Join(e.harness, "skills", "ki-demo", "SKILL.md"), "# demo\n")
	home := e.vars["HOME"]
	stale := filepath.Join(home, ".claude", "skills", "ki-standup")
	if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), stale); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Run(context.Background(), []string{"setup", "skills"}); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(e.harness)
	if got, _ := os.Readlink(filepath.Join(home, ".agents", "skills", "ki-demo")); got != filepath.Join(root, "skills", "ki-demo") {
		t.Fatalf("with a checkout the skills link to it, got %q", got)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Fatal("the retired ki-standup link stays")
	}
	if _, err := os.Stat(filepath.Join(e.vars["KI_CONFIG_DIR"], "skills")); err == nil {
		t.Fatal("nothing is copied when there is a checkout")
	}
}

func TestSkillRunFindsAnEmbeddedSkill(t *testing.T) {
	e := noCheckoutEnv(t)
	s, err := e.app.loadSkill("ki-demo")
	if err != nil || s == nil {
		t.Fatalf("%v", err)
	}
}

func TestUpdateWithoutCheckoutSaysToUpgradeTheBinary(t *testing.T) {
	e := noCheckoutEnv(t)
	if err := e.app.Run(context.Background(), []string{"update"}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	for _, want := range []string{"upgrade the binary", "go install github.com/kislow/kio/cmd/ki@latest", "linked 2 skills"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, e.out)
		}
	}
	if e.execd != nil {
		t.Fatalf("nothing is exec'd, got %v", e.execd)
	}
}

// tagFixture turns the test checkout into a clone detached on v0.1.0, with
// origin ahead by a v0.2.0 and a v0.10.0 tag.
func tagFixture(t *testing.T, e *env) (current func() string) {
	t.Helper()
	_, other := gitHarness(t, e)
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		all := append([]string{"-c", "user.name=t", "-c", "user.email=t@x", "-c", "tag.gpgSign=false", "-c", "commit.gpgSign=false"}, args...)
		if out, err := gitRun(ctx, dir, commandTimeout, all...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(e.harness, "tag", "v0.1.0")
	run(e.harness, "push", "-q", "--tags")
	run(e.harness, "checkout", "-q", "--detach", "v0.1.0")
	for _, tag := range []string{"v0.2.0", "v0.10.0", "v0.11.0-rc.1"} {
		write(t, filepath.Join(other, "n-"+tag), tag)
		run(other, "add", "-A")
		run(other, "commit", "-q", "-m", tag)
		run(other, "tag", tag)
	}
	run(other, "push", "-q")
	run(other, "push", "-q", "--tags")
	return func() string { return git(ctx, e.harness, "describe", "--tags", "--exact-match", "HEAD") }
}

func TestUpdateMovesATagCheckoutToTheNewestRelease(t *testing.T) {
	e := newEnv(t, "")
	current := tagFixture(t, e)
	if err := e.app.Run(context.Background(), []string{"update"}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	if got := current(); got != "v0.10.0" {
		t.Fatalf("at %q, want v0.10.0 (the newest release, not the release candidate)\n%s", got, e.out)
	}
	if !strings.Contains(e.out.String(), "v0.1.0 -> v0.10.0") {
		t.Fatalf("the move is reported:\n%s", e.out)
	}

	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"update"}); err != nil || !strings.Contains(e.out.String(), "already at v0.10.0") {
		t.Fatalf("a second update is a no-op: %v\n%s", err, e.out)
	}
}

func TestUpdateRefusesACheckoutDetachedOffATag(t *testing.T) {
	e := newEnv(t, "")
	gitHarness(t, e)
	gitIn(t, e.harness, "checkout", "-q", "--detach", "HEAD")
	if err := e.app.Run(context.Background(), []string{"update"}); !isUserError(err) || !strings.Contains(err.Error(), "not on a release tag") {
		t.Fatalf("got %v", err)
	}
}
