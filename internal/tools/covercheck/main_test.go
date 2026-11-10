package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profile = `mode: atomic
example.com/m/internal/scope/scope.go:1.1,2.2 3 1
example.com/m/internal/scope/scope.go:3.1,4.2 1 0
example.com/m/internal/config/config.go:1.1,2.2 4 0
example.com/m/internal/config/config.go:1.1,2.2 4 2
`

func TestRealMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"above threshold", []string{"-min", "70", path, "internal/scope", "internal/config"}, 0, "ok   internal/scope: 75.0%"},
		{"below threshold", []string{"-min", "80", path, "internal/scope"}, 1, "FAIL internal/scope: 75.0% < 80%"},
		{"duplicate block counted once, as covered", []string{"-min", "100", path, "internal/config"}, 0, "internal/config: 100.0%"},
		{"unknown package", []string{path, "internal/nope"}, 1, "no coverage data"},
		{"usage", []string{path}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := realMain(tt.args, &out, &errOut); code != tt.code {
				t.Fatalf("exit %d, want %d (%s%s)", code, tt.code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output %q does not contain %q", out.String(), tt.want)
			}
		})
	}
}

func TestParseProfileRejectsGarbage(t *testing.T) {
	if _, err := parseProfile(strings.NewReader("mode: set\nnot a line\n")); err == nil {
		t.Fatal("garbage must be rejected")
	}
}
