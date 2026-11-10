package ki

import (
	"strings"
	"testing"
)

func TestParseIssueArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, arg, owner, repo string
		want                   IssueRef
		wantErr                string
	}{
		{name: "bare number", arg: "217", owner: "alice", repo: "app", want: IssueRef{Owner: "alice", Repo: "app", Number: 217}},
		{name: "trimmed", arg: "  12 ", owner: "acme", repo: "app", want: IssueRef{Owner: "acme", Repo: "app", Number: 12}},
		{name: "explicit", arg: "Other/Repo#4", want: IssueRef{Owner: "Other", Repo: "Repo", Number: 4}},
		{name: "explicit ignores origin", arg: "a/b#9", owner: "nope", repo: "nope", want: IssueRef{Owner: "a", Repo: "b", Number: 9}},
		{name: "pull request url", arg: "https://github.com/a/b/pull/12", owner: "a", repo: "b", wantErr: "#12 is a pull request; use --task with a brief for it"},
		{name: "pull request url with path", arg: "https://github.com/a/b/pull/12/files", wantErr: "#12 is a pull request"},
		{name: "no origin", arg: "7", wantErr: "no GitHub origin"},
		{name: "zero", arg: "0", owner: "a", repo: "b", wantErr: "invalid issue"},
		{name: "word", arg: "nope", owner: "a", repo: "b", wantErr: "invalid issue"},
		{name: "hash only", arg: "#3", owner: "a", repo: "b", wantErr: "invalid issue"},
		{name: "missing number", arg: "a/b#", wantErr: "invalid issue"},
		{name: "empty", arg: "  ", owner: "a", repo: "b", wantErr: "invalid issue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseIssueArg(tt.arg, tt.owner, tt.repo)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil || got != tt.want || got.String() != tt.want.String() {
				t.Fatalf("got %+v (%s), %v", got, got.String(), err)
			}
		})
	}
}

func TestRefusal(t *testing.T) {
	t.Parallel()
	got := Refusal(IssueRef{Owner: "work", Repo: "app", Number: 7})
	if !strings.Contains(got, "work/app#7") || !strings.Contains(got, AgentWorkingLabel) || !strings.Contains(got, "--steal") {
		t.Fatalf("refusal %q", got)
	}
}

func TestIssueBrief(t *testing.T) {
	t.Parallel()
	got := IssueBrief(IssueRef{Owner: "work", Repo: "app", Number: 7}, " Fix it ", "\nThe tests fail.\n")
	if got != "Issue work/app#7: Fix it\n\nThe tests fail." {
		t.Fatalf("%q", got)
	}
}
