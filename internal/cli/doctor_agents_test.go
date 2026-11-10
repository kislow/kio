package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

func TestCheckAgentsCodexKnowledgeBase(t *testing.T) {
	for name, tc := range map[string]struct {
		installed bool
		url       string
		kbBlock   bool
		wantMark  string // mark of the Codex ki-kb line, "" when there is none
		wantFails int
	}{
		"local mode":                             {installed: true, wantMark: "·"},
		"url without ki-kb entry":                {installed: true, url: "https://kb.example", wantMark: "✗", wantFails: 1},
		"url with ki-kb entry":                   {installed: true, url: "https://kb.example", kbBlock: true, wantMark: "✓"},
		"local mode with a leftover ki-kb entry": {installed: true, kbBlock: true, wantMark: "·"},
		"codex not installed":                    {url: "https://kb.example"},
	} {
		t.Run(name, func(t *testing.T) {
			bin, codexHome := t.TempDir(), t.TempDir()
			if tc.installed {
				write(t, filepath.Join(bin, "codex"), "#!/bin/sh\n")
				if err := os.Chmod(filepath.Join(bin, "codex"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			write(t, filepath.Join(codexHome, "AGENTS.md"), managedBlock(t, ki.MarkdownComment()))
			if tc.kbBlock {
				write(t, filepath.Join(codexHome, "config.toml"), managedBlock(t, ki.TOMLComment()))
			}
			vars := map[string]string{"CODEX_HOME": codexHome, "KI_MCP_URL": tc.url}
			a := &App{Getenv: func(k string) string { return vars[k] }}
			c := &checks{}
			a.checkAgents(c, &here{})

			var mark string
			for _, s := range c.sections {
				for _, r := range s.rows {
					if r.label == "Codex" && (r.detail == "no knowledge base configured" || r.detail == "knows the ki-kb server" || r.detail == "does not know the ki-kb server") {
						mark = r.mark
					}
				}
			}
			if mark != tc.wantMark {
				t.Errorf("Codex ki-kb line mark = %q, want %q", mark, tc.wantMark)
			}
			// "on PATH" fails without any agent, which is not what is tested here.
			fails := c.failures
			if !tc.installed {
				fails--
			}
			if fails != tc.wantFails {
				t.Errorf("failures = %d, want %d", fails, tc.wantFails)
			}
		})
	}
}

func managedBlock(t *testing.T, c ki.Comment) string {
	t.Helper()
	out, err := ki.UpsertBlock("", "managed", c, "")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
