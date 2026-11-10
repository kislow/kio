package scope

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func isSensitive(b string) bool { return b == "client" }

func TestResolve(t *testing.T) {
	all := []string{"cortex", "acme", "voice", "client"}
	tests := []struct {
		name      string
		granted   []string
		requested []string
		want      []string
		forbidden bool
	}{
		{"default excludes client", all, nil, []string{"acme", "cortex", "voice"}, false},
		{"repo scope adds cortex", all, []string{"acme"}, []string{"acme", "cortex"}, false},
		{"client only by name", all, []string{"acme", "client"}, []string{"acme", "client", "cortex"}, false},
		{"client not granted fails closed", []string{"cortex", "acme"}, []string{"client"}, nil, true},
		{"unknown brain fails closed", []string{"cortex"}, []string{"voice"}, nil, true},
		{"sensitive-only token default is empty", []string{"client"}, nil, nil, true},
		{"sensitive-only token by name", []string{"client"}, []string{"client"}, []string{"client"}, false},
		{"family grant covers a named member", []string{"cortex", "client", "client-*"}, []string{"client-acme"}, []string{"client-acme", "cortex"}, false},
		{"family grant is never expanded by default", []string{"cortex", "client-*"}, nil, []string{"cortex"}, false},
		{"family grant does not cover its base", []string{"client-*"}, []string{"client"}, nil, true},
		{"family grant does not cover a lookalike", []string{"client-*"}, []string{"clientx"}, nil, true},
		{"pattern cannot be requested", []string{"client-*"}, []string{"client-*"}, nil, true},
		{"other family not covered", []string{"client-*"}, []string{"voice-acme"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.granted, tt.requested, isSensitive)
			if tt.forbidden {
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("want ErrForbidden, got %v (%v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSharedMatchCases runs the cases in testdata/brains/matches.json, which the
// Python implementations are tested against too.
func TestSharedMatchCases(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/brains/matches.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Matches []struct {
			Entry, Name string
			Want        bool
		} `json:"matches"`
		ValidEntries   []string `json:"valid_entries"`
		InvalidEntries []string `json:"invalid_entries"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Matches) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases.Matches {
		if got := Matches(c.Entry, c.Name); got != c.Want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.Entry, c.Name, got, c.Want)
		}
	}
	for _, e := range cases.ValidEntries {
		if !ValidEntry(e) {
			t.Errorf("ValidEntry(%q) = false", e)
		}
	}
	for _, e := range cases.InvalidEntries {
		if ValidEntry(e) {
			t.Errorf("ValidEntry(%q) = true", e)
		}
	}
}

func TestParseHeader(t *testing.T) {
	got, err := ParseHeader(" acme, client ,,")
	if err != nil || !reflect.DeepEqual(got, []string{"acme", "client"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := ParseHeader("brain:client"); err == nil {
		t.Fatal("colon must be rejected")
	}
	if _, err := ParseHeader("client-*"); err == nil {
		t.Fatal("a family pattern must not be requestable")
	}
	if _, err := ParseHeader("../etc"); err == nil {
		t.Fatal("path-like names must be rejected")
	}
}
