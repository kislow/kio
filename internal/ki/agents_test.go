package ki

import (
	"errors"
	"strings"
	"testing"
)

func TestUpsertBlockAppendsAndReplaces(t *testing.T) {
	user := "model = \"gpt-5\"\n\n[mcp_servers.github]\nurl = \"https://x\"\n"

	first, err := UpsertBlock(user, CodexMCPBlock("http://192.0.2.1:8787", MCPServer{}), TOMLComment(), CodexTable)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, user) {
		t.Fatal("content outside the block must be preserved")
	}
	if !HasBlock(first, TOMLComment()) || strings.Count(first, CodexTable) != 1 {
		t.Fatalf("expected exactly one managed table:\n%s", first)
	}

	second, err := UpsertBlock(first, CodexMCPBlock("http://192.0.2.9:8787", MCPServer{}), TOMLComment(), CodexTable)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(second, CodexTable) != 1 || !strings.Contains(second, "192.0.2.9") || strings.Contains(second, "192.0.2.1") {
		t.Fatalf("re-running must replace the block in place:\n%s", second)
	}
	if !strings.HasPrefix(second, user) {
		t.Fatal("content outside the block must still be preserved")
	}

	third, _ := UpsertBlock(second, CodexMCPBlock("http://192.0.2.9:8787", MCPServer{}), TOMLComment(), CodexTable)
	if third != second {
		t.Fatal("setup must be idempotent")
	}
}

func TestUpsertBlockRefusesUnmanagedConflict(t *testing.T) {
	user := "[mcp_servers.ki-kb]\nurl = \"http://elsewhere\"\n"
	if _, err := UpsertBlock(user, CodexMCPBlock("u", MCPServer{}), TOMLComment(), CodexTable); !errors.Is(err, ErrUnmanagedConflict) {
		t.Fatalf("want ErrUnmanagedConflict, got %v", err)
	}
}

func TestUpsertBlockMarkdownEmptyFile(t *testing.T) {
	out, err := UpsertBlock("", "# Rules\n", MarkdownComment(), "")
	if err != nil || !strings.HasPrefix(out, "<!-- >>> ki managed block") || !strings.HasSuffix(out, "<<< ki managed block <<< -->\n") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestUpsertBlockBrokenMarkers(t *testing.T) {
	broken := TOMLComment().begin() + "\nurl = 1\n"
	if _, err := UpsertBlock(broken, "x", TOMLComment(), ""); err == nil {
		t.Fatal("a block without its end marker must not be guessed at")
	}
}

func TestCodexBlockReadsEnv(t *testing.T) {
	b := CodexMCPBlock("http://192.0.2.1:8787/", MCPServer{})
	for _, want := range []string{`url = "http://192.0.2.1:8787/mcp"`, `bearer_token_env_var = "KI_TOKEN"`, `"X-Ki-Brains" = "KI_BRAINS"`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
}

func TestCursorRuleAlwaysApplies(t *testing.T) {
	r := CursorRule("\n# Instructions\nbody\n")
	if !strings.HasPrefix(r, "---\n") || !strings.Contains(r, "alwaysApply: true") || !strings.HasSuffix(r, "body\n") {
		t.Fatalf("got %q", r)
	}
}

func TestUpsertBlockKeepsCRLF(t *testing.T) {
	in := "model = \"x\"\r\n"
	out, err := UpsertBlock(in, "a = 1\nb = 2", TOMLComment(), CodexTable)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatalf("mixed line endings: %q", out)
	}
	again, err := UpsertBlock(out, "a = 1\nb = 2", TOMLComment(), CodexTable)
	if err != nil || again != out {
		t.Fatalf("not idempotent on CRLF: %q", again)
	}
}

func TestClaudeAttributionOff(t *testing.T) {
	tests := []struct {
		name, in string
		changed  bool
		wantErr  bool
	}{
		{"empty file", "", true, false},
		{"other keys kept", `{"model":"opus","attribution":{"sessionUrl":true}}`, true, false},
		{"already off", `{"attribution":{"commit":"","pr":""}}`, false, false},
		{"custom trailer replaced", `{"attribution":{"commit":"Co-Authored-By: x","pr":""}}`, true, false},
		{"not an object", `[1,2]`, false, true},
		{"attribution not an object", `{"attribution":"none"}`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := ClaudeAttributionOff([]byte(tt.in))
			if (err != nil) != tt.wantErr || changed != tt.changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if err != nil {
				if !errors.Is(err, ErrBadSettings) {
					t.Fatalf("want ErrBadSettings, got %v", err)
				}
				return
			}
			if !ClaudeAttributionIsOff(out) {
				t.Fatalf("result still attributes: %s", out)
			}
			if strings.Contains(tt.in, "opus") && !strings.Contains(string(out), `"model": "opus"`) {
				t.Fatalf("other keys lost: %s", out)
			}
			if strings.Contains(tt.in, "sessionUrl") && !strings.Contains(string(out), `"sessionUrl": true`) {
				t.Fatalf("other attribution keys lost: %s", out)
			}
		})
	}
}

func TestCodexBlockCarriesTheVaultServer(t *testing.T) {
	b := CodexMCPBlock("http://192.0.2.1:8787", VaultServer("/opt/ki", "/Users/me/vault"))
	for _, want := range []string{CodexTable, CodexVaultTable, `command = "/opt/ki"`, `args = ["vault", "mcp"]`, `env = { KI_VAULT = "/Users/me/vault" }`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
	if strings.Contains(CodexMCPBlock("u", MCPServer{}), CodexVaultTable) {
		t.Fatal("no vault, no table")
	}
}

func TestCodexMCPBlockWithoutURL(t *testing.T) {
	if got := CodexMCPBlock("", MCPServer{}); got != "" {
		t.Fatalf("no URL and no vault: %q", got)
	}
	block := CodexMCPBlock("", VaultServer("/opt/ki", "/vault"))
	if strings.Contains(block, CodexTable) || !strings.HasPrefix(block, CodexVaultTable) {
		t.Fatalf("only the vault table:\n%s", block)
	}
}
