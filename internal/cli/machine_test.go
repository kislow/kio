package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoIdentitiesYAML = `sensitive_brains: []
identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: false, ingest: true}
  other: {github_user: alice-other, email: alice@other.example, credential: other, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app]}
  - {name: site, repo: https://github.com/other/site, identity: other, brains: [cortex]}
`

func TestSetupMachineWritesTheFile(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)

	if err := e.app.Run(context.Background(), []string{"setup", "machine", "nope"}); !isUserError(err) || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("unknown identity must refuse: %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"setup", "machine"}); !isUserError(err) {
		t.Fatalf("no identity must refuse: %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"setup", "machine", "work"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "  - work\n") || strings.Contains(string(data), "other") {
		t.Fatalf("got %q", data)
	}
	if !strings.Contains(e.out.String(), "carries work") {
		t.Fatalf("got %q", e.out)
	}
}

func TestDoctorNotesIdentitiesNotOnThisMachine(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	fakeGH(t) // the gh login lines only print when gh is on PATH
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\n")

	_ = e.app.Run(context.Background(), []string{"doctor"}) // other checks may fail on CI
	out := e.out.String()
	for _, want := range []string{"✓ machine", "carries work", "· gh login", "alice-other (other) not on this machine"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alice-other (other) not logged in") {
		t.Fatalf("an identity not on this machine must not fail:\n%s", out)
	}

	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [nobody]\n")
	e.out.Reset()
	_ = e.app.Run(context.Background(), []string{"doctor"})
	if !strings.Contains(e.out.String(), "✗ machine") || !strings.Contains(e.out.String(), `"nobody"`) {
		t.Fatalf("a bad machine file must fail:\n%s", e.out)
	}
}

func TestRunRefusesARepoOfAnIdentityNotOnThisMachine(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [other]\n")
	gitIn(t, e.repo, "config", "user.email", "alice@work.example")

	err := e.app.Run(context.Background(), []string{"--agent", "sh"})
	if !isUserError(err) || !strings.Contains(err.Error(), "does not carry") || !strings.Contains(err.Error(), "ki setup machine other work") {
		t.Fatalf("want machine refusal with the fix, got %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil {
		t.Fatalf("--force must still work: %v", err)
	}
}

func TestModelPolicyDrivesTheSession(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"),
		"identities: [work]\nmodels:\n  judgement: {sh: big-model, claude: opus}\n  execution: {claude: haiku}\neffort:\n  execution: low\n")
	gitIn(t, e.repo, "config", "user.email", "alice@work.example")

	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.execEnv, "\n"), "KI_MODEL=big-model") {
		t.Fatalf("the session model must come from the policy: %v", e.execEnv)
	}

	// --model wins over the policy for one run.
	e.execEnv = nil
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh", "--model", "tiny"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.execEnv, "\n"), "KI_MODEL=tiny") {
		t.Fatalf("--model must override: %v", e.execEnv)
	}
}

func TestSetupMachineKeepsThePolicy(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nmodels:\n  execution: {claude: haiku}\nroles:\n  execution:\n    strategy: ordered\n    providers: [codex:gpt-5-mini]\ndelegate:\n  allow: [git, go]\n")
	if err := e.app.Run(context.Background(), []string{"setup", "machine", "work", "other"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  - other\n", "  - work\n", "execution:\n    claude: haiku\n", "codex:gpt-5-mini\n", "delegate:\n  allow: [git, go]\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("lacks %q:\n%s", want, data)
		}
	}
}

func TestSetupMachineRepairsARenamedIdentity(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), twoIdentitiesYAML)
	path := filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml")
	write(t, path, "identities: [retired]\nmodels:\n  execution: {claude: haiku}\neffort:\n  execution: medium\nroles:\n  execution:\n    strategy: ordered\n    providers: [codex:gpt-5-mini]\ndelegate:\n  allow: [make]\n")

	if err := e.app.Run(context.Background(), []string{"setup", "machine", "work"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  - work\n", "claude: haiku\n", "execution: medium\n", "codex:gpt-5-mini\n", "allow: [make]\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "retired") {
		t.Fatalf("old identity must be replaced:\n%s", data)
	}

	broken := "identities: [retired]\nmodels:\n  thinking: {claude: opus}\n"
	write(t, path, broken)
	err = e.app.Run(context.Background(), []string{"setup", "machine", "work"})
	if !isUserError(err) || !strings.Contains(err.Error(), "unknown class") {
		t.Fatalf("a policy that does not parse must refuse: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != broken {
		t.Fatalf("must not rewrite a policy that does not parse:\n%s", got)
	}
}
