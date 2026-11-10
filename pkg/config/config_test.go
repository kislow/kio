package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestTokens(t *testing.T) {
	dir := t.TempDir()
	cfgPath := write(t, dir, "config.json", `{
	  "listen": "192.0.2.1:8787",
	  "tokens_file": "x",
	  "brains": {
	    "cortex":   {"graph": "cortex", "backend": "http://kb-query.harness:8000"},
	    "acme": {"graph": "brain-acme", "backend": "http://kb-query.harness:8000"},
	    "client":      {"graph": "brain-client", "backend": "http://kb-query.harness:8000", "sensitive": true},
	    "client-*":    {"graph": "brain-client-*", "backend": "http://kb-query.harness:8000", "sensitive": true}
	  }
	}`)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sensitive("client") || cfg.Sensitive("acme") || cfg.DefaultBudget != 3000 {
		t.Fatal("unexpected config defaults")
	}
	if b, ok := cfg.Lookup("client-acme"); !ok || b.Graph != "brain-client-acme" || !b.Sensitive || b.Backend != "http://kb-query.harness:8000" {
		t.Fatalf("family member lookup: %+v %v", b, ok)
	}
	for _, name := range []string{"client-*", "clientx", "nope", "client-"} {
		if _, ok := cfg.Lookup(name); ok {
			t.Fatalf("Lookup(%q) must fail", name)
		}
	}
	if !cfg.Sensitive("client-acme") || !cfg.Sensitive("client-*") || !cfg.Sensitive("unknown") {
		t.Fatal("family members, families and unknown brains are sensitive")
	}

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	tokPath := write(t, dir, "tokens.json", `[
	  {"id": "laptop-1", "sha256": "`+hash("good")+`", "brains": ["cortex", "acme"], "expires": "`+future+`"},
	  {"id": "old",      "sha256": "`+hash("old")+`",  "brains": ["cortex"], "expires": "`+past+`"}
	]`)
	toks, err := LoadTokens(tokPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	verify := toks.Verifier()

	info, err := verify(context.Background(), "good", nil)
	if err != nil || info.UserID != "laptop-1" || len(info.Scopes) != 2 {
		t.Fatalf("good token: %v %+v", err, info)
	}
	if _, err := verify(context.Background(), "old", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expired token must fail: %v", err)
	}
	if _, err := verify(context.Background(), "nope", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("unknown token must fail: %v", err)
	}

	famPath := write(t, dir, "fam.json", `[
	  {"id":"a","sha256":"`+hash("a")+`","brains":["cortex","client-*"],"expires":"`+future+`"},
	  {"id":"b","sha256":"`+hash("b")+`","brains":["cortex","client-acme"],"expires":"`+future+`"}
	]`)
	if _, err := LoadTokens(famPath, cfg); err != nil {
		t.Fatalf("family and member grants must load: %v", err)
	}
	for _, grant := range []string{"voice-*", "client-", "*"} {
		p := write(t, dir, "badfam.json", `[{"id":"x","sha256":"`+hash("x")+`","brains":["`+grant+`"],"expires":"`+future+`"}]`)
		if _, err := LoadTokens(p, cfg); err == nil {
			t.Fatalf("grant %q must be rejected", grant)
		}
	}

	badPath := write(t, dir, "bad.json", `[{"id":"x","sha256":"`+hash("x")+`","brains":["voice"],"expires":"`+future+`"}]`)
	if _, err := LoadTokens(badPath, cfg); err == nil {
		t.Fatal("token granting an unknown brain must be rejected")
	}
}

func TestLoadRejectsBadFamilies(t *testing.T) {
	dir := t.TempDir()
	tests := map[string]string{
		"family with plain graph":  `"client-*": {"graph": "brain-client", "backend": "http://p"}`,
		"plain brain with pattern": `"client": {"graph": "brain-client-*", "backend": "http://p"}`,
		"overlapping families":     `"client-*": {"graph": "brain-client-*", "backend": "http://p"}, "client-eu-*": {"graph": "brain-client-eu-*", "backend": "http://p"}`,
		"member shadowing family":  `"client-*": {"graph": "brain-client-*", "backend": "http://p"}, "client-acme": {"graph": "brain-client-acme", "backend": "http://main"}`,
		"bare star":                `"*": {"graph": "*", "backend": "http://p"}`,
	}
	for name, brains := range tests {
		t.Run(name, func(t *testing.T) {
			p := write(t, dir, "c.json", `{"listen":"x","tokens_file":"x","brains":{"cortex":{"graph":"cortex","backend":"http://m"},`+brains+`}}`)
			if _, err := Load(p); err == nil {
				t.Fatal("must be rejected")
			}
		})
	}
}
