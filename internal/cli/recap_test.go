package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const recapProjects = `identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: true, ingest: true}
  client: {github_user: alice-client, email: alice@client.example, credential: client, vault: false, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app]}
  - {name: client-api, repo: https://github.com/client/api, identity: client, brains: [client]}
`

// fakeGH puts a gh on PATH that prints a token for alice-work only.
func fakeGH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$4\" = alice-work ] && echo tok && exit 0\nexit 1\n"
	write(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil { // #nosec G302 -- the fake gh must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeGraphQL answers the activity query with one merged PR.
func fakeGraphQL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer tok" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		merged := map[string]any{
			"__typename": "PullRequest", "number": 7, "title": "Ship it", "state": "MERGED",
			"url": "https://github.com/work/app/pull/7", "mergedAt": "2026-10-02T00:00:00Z",
			"repository": map[string]any{"nameWithOwner": "work/app"},
		}
		body := map[string]any{"data": map[string]any{
			"viewer": map[string]any{"login": "alice-work"},
			"merged": map[string]any{"issueCount": 1, "nodes": []any{merged}},
		}}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRecap(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		args   []string
		// errHas is a user error; empty means success with out in stdout.
		errHas string
		out    string
	}{
		{name: "repo identity, json", remote: "https://github.com/work/app", args: []string{"--json", "--since", "2026-10-01"}, out: `"url": "https://github.com/work/app/pull/7"`},
		{name: "flag identity, text", args: []string{"--identity", "work"}, out: "work (alice-work) since"},
		{name: "unmapped repo", errHas: "pass --identity (work)"},
		{name: "vault false identity", remote: "https://github.com/client/api", errHas: `"client" is marked vault: false`},
		{name: "bad since", args: []string{"--identity", "work", "--since", "monday"}, errHas: "not YYYY-MM-DD"},
		{name: "bad flag", args: []string{"--both"}, errHas: "usage: ki recap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.remote)
			fakeGH(t)
			write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), recapProjects)
			e.vars["KI_GITHUB_GRAPHQL_URL"] = fakeGraphQL(t)
			err := e.app.Run(context.Background(), append([]string{"recap"}, tc.args...))
			if tc.errHas != "" {
				if !isUserError(err) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("want user error with %q, got %v", tc.errHas, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(e.out.String(), tc.out) {
				t.Fatalf("want %q in:\n%s", tc.out, e.out)
			}
		})
	}
}

func TestRecapNeedsTheIdentitysGhLogin(t *testing.T) {
	e := newEnv(t, "")
	fakeGH(t)
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"),
		strings.Replace(recapProjects, "vault: false", "vault: true", 1))
	err := e.app.Run(context.Background(), []string{"recap", "--identity", "client"})
	if !isUserError(err) || !strings.Contains(err.Error(), "gh is not logged in as alice-client") {
		t.Fatalf("got %v", err)
	}
}

// untilRe masks the report's run time, the only field that differs between two runs.
var untilRe = regexp.MustCompile(`"until": "[^"]*"`)

func TestStandupAliasRunsRecap(t *testing.T) {
	run := func(cmd string) (stdout, stderr string) {
		e := newEnv(t, "")
		fakeGH(t)
		write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), recapProjects)
		e.vars["KI_GITHUB_GRAPHQL_URL"] = fakeGraphQL(t)
		args := []string{cmd, "--identity", "work", "--since", "2026-10-01", "--json"}
		if err := e.app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		return untilRe.ReplaceAllString(e.out.String(), `"until": ""`), e.app.Stderr.(*bytes.Buffer).String()
	}
	wantOut, wantErr := run("recap")
	gotOut, gotErr := run("standup")
	if gotOut != wantOut {
		t.Fatalf("stdout differs:\nstandup: %s\nrecap: %s", gotOut, wantOut)
	}
	if wantErr != "" {
		t.Fatalf("recap wrote to stderr: %q", wantErr)
	}
	if !strings.Contains(gotErr, "`ki standup` is now `ki recap`") {
		t.Fatalf("want deprecation note on stderr, got %q", gotErr)
	}
}
