package ki

import "testing"

func TestIssueBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, title, want string
	}{
		{name: "feat with scope", title: "feat(alerting): route API Gateway route-5xx alarms per owning team", want: "feat/7-route-api-gateway-route-5xx-alarms-per"},
		{name: "fix", title: "fix: node memory alarm flaps", want: "fix/7-node-memory-alarm-flaps"},
		{name: "breaking", title: "feat!: drop the old flag", want: "feat/7-drop-the-old-flag"},
		{name: "upper-case prefix", title: "Docs(skills): ready PRs", want: "docs/7-ready-prs"},
		{name: "no prefix", title: "Sentry Observability: docs and runbooks", want: "chore/7-sentry-observability-docs-and-runbooks"},
		{name: "unknown prefix", title: "wip: something", want: "chore/7-wip-something"},
		{name: "punctuation only", title: "feat: ???", want: "feat/7"},
		{name: "empty", title: "", want: "chore/7"},
		{name: "non-ascii dropped", title: "fix: café crème", want: "fix/7-caf-cr-me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IssueBranch(7, tt.title); got != tt.want {
				t.Fatalf("IssueBranch(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestTaskBranch(t *testing.T) {
	t.Parallel()
	const id = "j-20261028-101500-ab12cd34"
	for title, want := range map[string]string{
		"Fix the build cache key": "chore/fix-the-build-cache-key-ab12cd34",
		"":                        "chore/task-ab12cd34",
		"!!!":                     "chore/task-ab12cd34",
		"Repair the cache key of the worker image builder": "chore/repair-the-cache-key-of-the-worker-ab12cd34",
	} {
		if got := TaskBranch(title, id); got != want {
			t.Errorf("TaskBranch(%q) = %q, want %q", title, got, want)
		}
		if m := LeakMatch(TaskBranch(title, id)); m != "" {
			t.Errorf("TaskBranch(%q) matches the leak guard: %q", title, m)
		}
	}
}

func TestUniqueBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, jobID, want string
		taken             bool
	}{
		{name: "free", jobID: "j-20261028-101500-ab12", want: "fix/7-x"},
		{name: "taken", jobID: "j-20261028-101500-ab12", taken: true, want: "fix/7-x-ab12"},
		{name: "taken, id without hyphen", jobID: "job1", taken: true, want: "fix/7-x-job1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := UniqueBranch("fix/7-x", tt.jobID, func(string) bool { return tt.taken })
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlugCutsAtAWordBoundary(t *testing.T) {
	t.Parallel()
	got := slug("one two three four five six seven eight nine ten")
	if got != "one-two-three-four-five-six-seven-eight" || len(got) > slugMax {
		t.Fatalf("got %q (%d)", got, len(got))
	}
}
