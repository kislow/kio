package standup

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/pkg/projects"
)

const projectsYAML = `sensitive_brains: []
identities:
  personal: {github_user: me, email: me@example.com, credential: personal, ingest: true}
  work: {github_user: me_work, email: me@work.example, credential: work, vault: true, ingest: true}
  client: {github_user: me-client, email: me@client.example, credential: client, vault: false, ingest: true}
projects:
  - {name: kio, repo: https://github.com/me/kio, identity: personal, brains: [cortex], vault_folders: [ki]}
  - {name: acme-site, repo: https://github.com/acme/site, identity: personal, brains: [cortex], vault_folders: [acme]}
  - {name: work-infra, repo: https://github.com/workorg/infra, identity: work, brains: [work], board: Ops, vault_folders: [work]}
  - {name: work-deploy, repo: https://github.com/workorg/deploy, identity: work, brains: [work], board: Ops, vault_folders: [work]}
  - {name: client-api, repo: https://github.com/clientorg/api, identity: client, brains: [client]}
`

func loadConfig(t *testing.T) *projects.Config {
	t.Helper()
	cfg, err := projects.Parse([]byte(projectsYAML))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSince(t *testing.T) {
	dubai := time.FixedZone("GST", 4*3600)
	at := func(day int, hour int) time.Time { return time.Date(2026, 10, day, hour, 30, 0, 0, dubai) }
	cases := []struct {
		name, flag string
		now        time.Time
		want       string
		err        bool
	}{
		{name: "tuesday covers monday", now: at(20, 9), want: "2026-10-19"},
		{name: "monday covers friday onwards", now: at(19, 9), want: "2026-10-16"},
		{name: "saturday covers friday", now: at(24, 0), want: "2026-10-23"},
		{name: "sunday covers friday", now: at(25, 18), want: "2026-10-23"},
		{name: "flag overrides", flag: "2026-10-01", now: at(20, 9), want: "2026-10-01"},
		{name: "flag in the future", flag: "2026-11-01", now: at(20, 9), err: true},
		{name: "flag not a date", flag: "yesterday", now: at(20, 9), err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Since(tc.now, tc.flag)
			if tc.err {
				if !errors.Is(err, ErrBadSince) {
					t.Fatalf("want ErrBadSince, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Format(time.DateOnly) != tc.want || got.Hour() != 0 || got.Location() != dubai {
				t.Fatalf("got %v, want %s 00:00 local", got, tc.want)
			}
		})
	}
}

func TestSelectIdentity(t *testing.T) {
	cfg := loadConfig(t)
	work := cfg.Identities["work"]
	client := cfg.Identities["client"]
	cases := []struct {
		name, flag string
		current    *projects.Identity
		want       string
		errHas     string
	}{
		{name: "flag wins over the repo", flag: "personal", current: &work, want: "personal"},
		{name: "current repo", current: &work, want: "work"},
		{name: "unmapped repo needs a flag", errHas: "pass --identity (personal, work)"},
		{name: "unknown flag", flag: "nobody", errHas: `unknown identity "nobody"`},
		{name: "vault false refused by flag", flag: "client", errHas: "vault: false"},
		{name: "vault false refused from repo", current: &client, errHas: "vault: false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := SelectIdentity(cfg, tc.flag, tc.current)
			if tc.errHas != "" {
				if !errors.Is(err, ErrIdentity) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("want error with %q, got %v", tc.errHas, err)
				}
				return
			}
			if err != nil || id.Name != tc.want {
				t.Fatalf("got %q, %v; want %q", id.Name, err, tc.want)
			}
		})
	}
}

func TestScopeKeepsIdentitiesApart(t *testing.T) {
	cfg := loadConfig(t)
	cases := []struct {
		identity string
		boards   []Board
		folders  []string
		allowed  []string
		refused  []string
	}{
		{
			identity: "work",
			boards:   []Board{{Owner: "workorg", Title: "Ops"}},
			folders:  []string{"work", "work-deploy", "work-infra"},
			allowed:  []string{"workorg/infra", "WorkOrg/other", "someone/unlisted"},
			refused:  []string{"me/kio", "acme/site", "clientorg/api"},
		},
		{
			identity: "personal",
			folders:  []string{"acme", "acme-site", "ki", "kio"},
			allowed:  []string{"me/kio", "acme/site", "someone/unlisted"},
			refused:  []string{"workorg/infra", "clientorg/api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.identity, func(t *testing.T) {
			s := NewScope(cfg, cfg.Identities[tc.identity])
			if !slices.Equal(s.Boards, tc.boards) {
				t.Errorf("boards %v, want %v", s.Boards, tc.boards)
			}
			if !slices.Equal(s.VaultFolders, tc.folders) {
				t.Errorf("vault folders %v, want %v", s.VaultFolders, tc.folders)
			}
			for _, r := range tc.allowed {
				if !s.Allows(r) {
					t.Errorf("%s refused", r)
				}
			}
			for _, r := range tc.refused {
				if s.Allows(r) {
					t.Errorf("%s allowed", r)
				}
			}
		})
	}
}

func TestScopeDerivesNoFamilyFolder(t *testing.T) {
	cfg, err := projects.Parse([]byte(`identities:
  work: {github_user: me, email: me@work.example, credential: work, ingest: true}
projects:
  - {name: acme-api, repo: https://github.com/acme/api, identity: work, brains: [cortex]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := NewScope(cfg, cfg.Identities["work"]).VaultFolders; !slices.Equal(got, []string{"acme-api"}) {
		t.Errorf("vault folders %v, want only the project's own", got)
	}
}
