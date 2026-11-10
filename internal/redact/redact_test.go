package redact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		in       string
		redacted bool
	}{
		{"AKIAABCDEFGHIJKLMNOP", true},
		{"password: hunter2hunter2", true},
		{"postgres://app:s3cr3tpass@db.internal:5432/x", true},
		{"ghp_" + strings.Repeat("a", 36), true},
		{"-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----", true},
		{"arn:aws:secretsmanager:*:*:secret:acme/app-config", false},
		{"permissions: id-token: write-access", false},
		{"xghp_" + strings.Repeat("a", 36), false},
		{"éghp_" + strings.Repeat("a", 36), false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := Redact("see " + tt.in + " here")
			if strings.Contains(got, Redacted) != tt.redacted {
				t.Fatalf("Redact(%q) = %q", tt.in, got)
			}
			if !tt.redacted && got != "see "+tt.in+" here" {
				t.Fatalf("changed: %q", got)
			}
		})
	}
	if got := Redact("token=abcdefgh1 and api_key: 'zzzzzzzzz'"); got != "token="+Redacted+" and api_key: '"+Redacted+"'" {
		t.Fatalf("assignments keep their key and quote: %q", got)
	}
}
