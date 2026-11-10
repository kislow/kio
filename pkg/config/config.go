// Package config loads the MCP server configuration and token store.
package config

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kislow/kio/pkg/scope"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// Brain maps a logical brain name to the graph that stores it and the
// kb-query backend that can reach that graph. A family entry, keyed
// "client-*" with graph "brain-client-*", serves every "client-<suffix>"
// brain from graph "brain-client-<suffix>", so a new sub-project needs no config.
type Brain struct {
	Graph     string `json:"graph"`
	Backend   string `json:"backend"`
	Sensitive bool   `json:"sensitive"`
}

// Config is the server configuration file.
type Config struct {
	// Listen must be a VPN-only address; the server is never exposed publicly.
	Listen     string           `json:"listen"`
	TokensFile string           `json:"tokens_file"`
	Brains     map[string]Brain `json:"brains"`
	// DefaultBudget is the token budget for get_context when the caller sets none.
	DefaultBudget int `json:"default_budget"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Listen == "" || c.TokensFile == "" {
		return nil, fmt.Errorf("listen and tokens_file are required")
	}
	if _, ok := c.Brains[scope.Cortex]; !ok {
		return nil, fmt.Errorf("brains must include %q", scope.Cortex)
	}
	for name, b := range c.Brains {
		family := scope.IsFamily(name)
		if family != scope.IsFamily(b.Graph) || !scope.ValidEntry(name) || !scope.ValidEntry(b.Graph) {
			return nil, fmt.Errorf("invalid brain or graph name %q/%q", name, b.Graph)
		}
		if b.Backend == "" {
			return nil, fmt.Errorf("brain %q has no backend", name)
		}
		// Overlaps would make the backend for a brain depend on lookup order.
		for other := range c.Brains {
			if other != name && family && (scope.Matches(name, other) || scope.Matches(name, strings.TrimSuffix(other, scope.FamilySuffix))) {
				return nil, fmt.Errorf("brain family %q overlaps %q", name, other)
			}
		}
	}
	if c.DefaultBudget == 0 {
		c.DefaultBudget = 3000
	}
	return &c, nil
}

// Lookup returns the brain for a concrete name, resolving family members to
// their own graph. Unknown names and patterns are not found.
func (c *Config) Lookup(name string) (Brain, bool) {
	if !scope.ValidName(name) {
		return Brain{}, false
	}
	if b, ok := c.Brains[name]; ok {
		return b, true
	}
	for key, b := range c.Brains {
		if scope.IsFamily(key) && scope.Matches(key, name) {
			suffix := strings.TrimPrefix(name, strings.TrimSuffix(key, "*"))
			b.Graph = strings.TrimSuffix(b.Graph, "*") + suffix
			return b, true
		}
	}
	return Brain{}, false
}

// Sensitive reports whether a brain may only be read when named explicitly.
// Unknown brains count as sensitive, so they are never part of a default scope.
func (c *Config) Sensitive(brain string) bool {
	if fam, ok := c.Brains[brain]; ok && scope.IsFamily(brain) {
		return fam.Sensitive
	}
	b, ok := c.Lookup(brain)
	return !ok || b.Sensitive
}

// Token is one entry in the token store. Only the SHA-256 of the secret is stored.
type Token struct {
	ID      string    `json:"id"`
	SHA256  string    `json:"sha256"`
	Brains  []string  `json:"brains"`
	Expires time.Time `json:"expires"`
}

// Tokens is a loaded token store.
type Tokens struct {
	byHash map[[32]byte]Token
}

// LoadTokens reads the token store and checks every granted brain exists.
func LoadTokens(path string, cfg *Config) (*Tokens, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from the config file
	if err != nil {
		return nil, fmt.Errorf("reading tokens: %w", err)
	}
	var list []Token
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	t := &Tokens{byHash: map[[32]byte]Token{}}
	for _, tok := range list {
		h, err := hex.DecodeString(tok.SHA256)
		if err != nil || len(h) != 32 {
			return nil, fmt.Errorf("token %q: sha256 must be 64 hex chars", tok.ID)
		}
		for _, b := range tok.Brains {
			_, isFamilyKey := cfg.Brains[b]
			isFamilyKey = isFamilyKey && scope.IsFamily(b)
			if _, ok := cfg.Lookup(b); !ok && !isFamilyKey {
				return nil, fmt.Errorf("token %q grants unknown brain %q", tok.ID, b)
			}
		}
		if tok.Expires.IsZero() {
			return nil, fmt.Errorf("token %q needs an expiry", tok.ID)
		}
		t.byHash[[32]byte(h)] = tok
	}
	return t, nil
}

// Verifier returns a bearer-token verifier for the MCP auth middleware.
// TokenInfo.Scopes carries the granted brains; UserID carries the token id.
func (t *Tokens) Verifier() auth.TokenVerifier {
	return func(_ context.Context, secret string, _ *http.Request) (*auth.TokenInfo, error) {
		sum := sha256.Sum256([]byte(secret))
		for h, tok := range t.byHash {
			if subtle.ConstantTimeCompare(h[:], sum[:]) == 1 {
				if time.Now().After(tok.Expires) {
					return nil, fmt.Errorf("%w: token %s expired", auth.ErrInvalidToken, tok.ID)
				}
				return &auth.TokenInfo{UserID: tok.ID, Scopes: tok.Brains, Expiration: tok.Expires}, nil
			}
		}
		return nil, auth.ErrInvalidToken
	}
}
