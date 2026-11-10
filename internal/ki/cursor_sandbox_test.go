package ki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestEnsureCursorSandboxAllowsOnlyGitHubAPI(t *testing.T) {
	dir := t.TempDir()
	wrote, err := EnsureCursorSandbox(dir)
	if err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".cursor", "sandbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		NetworkPolicy struct {
			Default string   `json:"default"`
			Allow   []string `json:"allow"`
		} `json:"networkPolicy"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	if !slices.Equal(got.NetworkPolicy.Allow, []string{"api.github.com"}) {
		t.Errorf("allow = %v", got.NetworkPolicy.Allow)
	}
	if got.NetworkPolicy.Default == "allow" {
		t.Error("default must stay deny")
	}
}

func TestEnsureCursorSandboxKeepsAnExistingPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "sandbox.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	const own = `{"networkPolicy":{"allow":["pypi.org"]}}`
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	wrote, err := EnsureCursorSandbox(dir)
	if err != nil || wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if data, _ := os.ReadFile(path); string(data) != own {
		t.Errorf("file changed: %s", data)
	}
}

func TestAppendLineOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "info", "exclude")
	for range 2 {
		if err := AppendLineOnce(path, ".cursor/sandbox.json"); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != ".cursor/sandbox.json\n" {
		t.Errorf("exclude = %q", data)
	}
	if err := os.WriteFile(path, []byte("build/"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendLineOnce(path, ".cursor/sandbox.json"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "build/\n.cursor/sandbox.json\n" {
		t.Errorf("exclude = %q", data)
	}
}
