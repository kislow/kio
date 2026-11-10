package ki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	s, err := LoadSettings(write("ok.yaml", "knowledge_base:\n  url: https://kb.example/\n"))
	if err != nil || s.KnowledgeBase.URL != "https://kb.example" {
		t.Fatalf("got %+v, %v", s, err)
	}
	s, err = LoadSettings(filepath.Join(dir, "missing.yaml"))
	if err != nil || s.KnowledgeBase.URL != "" {
		t.Fatalf("a missing file is empty settings: %+v, %v", s, err)
	}
	for name, content := range map[string]string{
		"typo":   "knowledge_bse:\n  url: https://kb.example\n",
		"scheme": "knowledge_base:\n  url: kb.example\n",
		"extra":  "knowledge_base:\n  url: https://kb.example\n  token: x\n",
	} {
		if _, err := LoadSettings(write(name+".yaml", content)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestMissingConfigFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := MissingConfigFiles(dir); len(got) != len(ConfigFiles) {
		t.Fatalf("empty dir: %v", got)
	}
	for _, f := range ConfigFiles {
		if f == ConfigFile {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := MissingConfigFiles(dir); strings.Join(got, ",") != ConfigFile {
		t.Fatalf("got %v", got)
	}
}
