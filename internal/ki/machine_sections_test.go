package ki

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

type demoBlock struct {
	Name string `yaml:"name"`
}

//nolint:gochecknoinits // the test section must exist before any test reads the registry
func init() {
	registerMachineSection(machineSection{
		key: "demo",
		parse: func(path string, node ast.Node) (any, error) {
			var raw demoBlock
			if err := yaml.NodeToValue(node, &raw, yaml.Strict()); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if raw.Name == "" {
				return nil, fmt.Errorf("%s: demo.name is required", path)
			}
			return raw, nil
		},
		render: func(b *strings.Builder, v any) { fmt.Fprintf(b, "demo:\n  name: %s\n", v.(demoBlock).Name) },
	})
}

func loadDemo(t *testing.T, content string) (*Machine, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadMachine(path, map[string]struct{}{"personal": {}})
}

func TestRegisteredSectionIsParsedAndStrict(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ yaml, wantErr string }{
		"valid":             {"identities: [personal]\ndemo: {name: x}\n", ""},
		"null block":        {"identities: [personal]\ndemo:\n", ""},
		"missing field":     {"identities: [personal]\ndemo: {}\n", "demo.name is required"},
		"unknown in block":  {"identities: [personal]\ndemo: {name: x, extra: 1}\n", "extra"},
		"unknown top level": {"identities: [personal]\ndemo: {name: x}\nother: 1\n", "other"},
		"not a mapping":     {"- identities\n", "mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, err := loadDemo(t, tc.yaml)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error=%v want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "valid" && m.Sections["demo"] != (demoBlock{Name: "x"}) {
				t.Fatalf("sections=%v", m.Sections)
			}
		})
	}
}

func TestRegisteredSectionSurvivesARewrite(t *testing.T) {
	t.Parallel()
	m := Machine{Identities: []string{"personal"}, Sections: map[string]any{"demo": demoBlock{Name: "x"}}}
	back, err := loadDemo(t, RenderMachine(m))
	if err != nil {
		t.Fatal(err)
	}
	if back.Sections["demo"] != (demoBlock{Name: "x"}) {
		t.Fatalf("section lost: %v", back.Sections)
	}
}
