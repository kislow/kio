package ki

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveFolder(t *testing.T) {
	cfg := loadFixture(t)
	acme, payments := "git@github.com:client-a/acme.git", "https://github.com/client-a/payments"
	tests := []struct {
		name      string
		remotes   []string
		extra     []string
		identity  string
		projects  []string
		brains    []string
		sensitive bool
		unmapped  bool
	}{
		{"one repo", []string{acme}, nil, "client-a", []string{"acme"}, []string{"acme", "voice"}, false, false},
		{
			"union keeps sensitive brains like Resolve",
			[]string{acme, payments},
			nil, "client-a",
			[]string{"acme", "payments-docs", "payments-client"},
			[]string{"acme", "voice", "client"},
			true, false,
		},
		{"unmapped repos are ignored", []string{"https://github.com/someone/else", acme}, nil, "client-a", []string{"acme"}, []string{"acme", "voice"}, false, false},
		{"the same repo twice counts once", []string{acme, acme}, nil, "client-a", []string{"acme"}, []string{"acme", "voice"}, false, false},
		{"no repos", nil, nil, "", nil, []string{"cortex"}, false, true},
		{"only unmapped repos", []string{"https://github.com/someone/else"}, []string{"voice"}, "", nil, []string{"cortex", "voice"}, false, true},
		{"an extra brain can make it sensitive", []string{acme}, []string{"client"}, "client-a", []string{"acme"}, []string{"acme", "voice", "client"}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ResolveFolder(cfg, tt.remotes, tt.extra)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if s.Identity != nil {
				got = s.Identity.Name
			}
			if got != tt.identity || !reflect.DeepEqual(s.Projects, tt.projects) || !reflect.DeepEqual(s.Brains, tt.brains) ||
				s.Sensitive != tt.sensitive || s.Unmapped != tt.unmapped || !s.Folder {
				t.Fatalf("got %+v", s)
			}
		})
	}
}

func TestResolveFolderRefusesSeveralIdentities(t *testing.T) {
	cfg := loadFixture(t)
	cfg.Projects[1].Identity = cfg.Identities["personal"]
	_, err := ResolveFolder(cfg, []string{"git@github.com:client-a/acme.git", "https://github.com/client-a/payments"}, nil)
	var mixed MixedIdentitiesError
	if !errors.As(err, &mixed) || !reflect.DeepEqual(mixed.Identities, []string{"client-a", "personal"}) || !strings.Contains(err.Error(), "--identity NAME") {
		t.Fatalf("got %v", err)
	}
}

func TestIdentitySessionLeavesSensitiveBrainsOut(t *testing.T) {
	cfg := loadFixture(t)
	id := cfg.Identities["client-a"]
	s := IdentitySession(cfg, id, nil)
	if !reflect.DeepEqual(s.Brains, []string{"cortex", "acme", "voice"}) || s.Sensitive || s.Identity.Name != "client-a" || !s.Folder {
		t.Fatalf("got %+v", s)
	}
	if s = IdentitySession(cfg, id, []string{"client"}); !s.Sensitive || s.Brains[len(s.Brains)-1] != "client" {
		t.Fatalf("a named sensitive brain must switch the token: %+v", s)
	}
}

func TestParseRunArgsIdentity(t *testing.T) {
	if r := ParseRunArgs([]string{"--identity", "work", "--force"}, ""); r.Identity != "work" || !r.Force || len(r.AgentArgs) != 0 {
		t.Fatalf("got %+v", r)
	}
	if r := ParseRunArgs([]string{"--", "--identity", "work"}, ""); r.Identity != "" {
		t.Fatalf("after -- it belongs to the agent: %+v", r)
	}
}
