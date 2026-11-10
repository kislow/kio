package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func doctorOutput(t *testing.T, e *env) string {
	t.Helper()
	e.out.Reset()
	// The KB check may fail here; only the behind line and exit code matter.
	_ = e.app.Run(context.Background(), []string{"doctor"})
	return e.out.String()
}

func TestDoctorWarnsWhenHarnessBehind(t *testing.T) {
	e := newEnv(t, "https://github.com/me/app")
	_, other := gitHarness(t, e)
	ctx := context.Background()

	if out := doctorOutput(t, e); strings.Contains(out, "harness behind") {
		t.Fatalf("current checkout must not warn:\n%s", out)
	}

	write(t, filepath.Join(other, "a.txt"), "a\n")
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "a"}, {"push", "-q"}} {
		if out, err := gitRun(ctx, other, commandTimeout, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out := doctorOutput(t, e); strings.Contains(out, "harness behind") {
		t.Fatalf("doctor must not fetch, so the new commit is unseen:\n%s", out)
	}

	if out, err := gitRun(ctx, e.harness, commandTimeout, "fetch", "-q"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out := doctorOutput(t, e)
	for _, want := range []string{"! harness behind", "1 commit behind origin/main; run ki update"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗ harness") {
		t.Errorf("behind must be a warning, not a failure:\n%s", out)
	}
}

func TestDoctorIgnoresCheckoutWithoutUpstream(t *testing.T) {
	e := newEnv(t, "https://github.com/me/app")
	gitHarness(t, e)
	if out, err := gitRun(context.Background(), e.harness, commandTimeout, "branch", "--unset-upstream"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out := doctorOutput(t, e); strings.Contains(out, "harness behind") {
		t.Fatalf("no upstream must not warn:\n%s", out)
	}
}

func TestWarningLeavesDoctorExitCodeAlone(t *testing.T) {
	c := &checks{}
	c.warn("harness behind", "x")
	if c.failures != 0 || c.passes != 0 {
		t.Fatalf("warn must not count: %+v", c)
	}
}
