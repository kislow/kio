package ki

import (
	"errors"
	"slices"
	"testing"
)

func TestParseMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want Mode
		err  error
	}{
		{"", ModeAsk, nil},
		{"ask", ModeAsk, nil},
		{"auto", ModeAuto, nil},
		{"skip", ModeSkip, nil},
		{"edits", ModeEdits, nil},
		{"accept-edits", "", ErrUnknownMode},
		{"yolo", "", ErrUnknownMode},
		{"Skip", "", ErrUnknownMode},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMode(tt.in)
			if got != tt.want || !errors.Is(err, tt.err) {
				t.Fatalf("ParseMode(%q) = %q, %v", tt.in, got, err)
			}
			if err != nil && err.Error() != `unknown mode "`+tt.in+`": use ask, auto, edits or skip` {
				t.Fatalf("error text %q", err)
			}
		})
	}
}

func TestModeArgsAllow(t *testing.T) {
	t.Parallel()
	allow := []string{"git", "gh pr", "go"}
	claudeAllow := []string{"--permission-mode", "acceptEdits", "--allowedTools=Bash(git:*)", "--allowedTools=Bash(gh pr:*)", "--allowedTools=Bash(go:*)"}
	tests := []struct {
		name  string
		agent string
		mode  Mode
		allow []string
		want  []string
		err   string
	}{
		{"claude edits, no list", "claude", ModeEdits, nil, []string{"--permission-mode", "acceptEdits"}, ""},
		{"claude edits, list", "claude", ModeEdits, allow, claudeAllow, ""},
		{"claude edits by path", "/opt/bin/claude", ModeEdits, []string{"git"}, []string{"--permission-mode", "acceptEdits", "--allowedTools=Bash(git:*)"}, ""},
		{"claude other modes ignore the list", "claude", ModeAuto, allow, []string{"--permission-mode", "auto"}, ""},
		{"claude skip ignores the list", "claude", ModeSkip, allow, []string{"--dangerously-skip-permissions"}, ""},
		{"claude ask ignores the list", "claude", ModeAsk, allow, []string{"--permission-mode", "manual"}, ""},
		{"agy edits, no list", "agy", ModeEdits, nil, []string{"--mode", "accept-edits"}, ""},
		{"agy edits, list stays in settings", "agy", ModeEdits, allow, []string{"--mode", "accept-edits"}, ""},
		{"agy skip", "agy", ModeSkip, allow, []string{"--dangerously-skip-permissions"}, ""},
		{"codex edits", "codex", ModeEdits, allow, nil, "mode not supported: codex has no edits mode; use ask, auto or skip"},
		{"cursor edits", "cursor-agent", ModeEdits, allow, nil, "mode not supported: cursor-agent has no edits mode; use ask, auto or skip"},
		{"cursor edits as agent", "agent", ModeEdits, nil, nil, "mode not supported: agent has no edits mode; use ask, auto or skip"},
		{"unknown agent edits", "aider", ModeEdits, allow, nil, "mode not supported: ki does not know aider's flags for edits mode; use --mode ask and pass the agent's own flag after --"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, overridden, err := ModeArgsAllow(tt.agent, tt.mode, nil, tt.allow)
			if overridden {
				t.Fatal("no agent args, nothing to override")
			}
			if tt.err != "" {
				if err == nil || err.Error() != tt.err || !errors.Is(err, ErrModeUnsupported) {
					t.Fatalf("err %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestModeArgsAllowDoesNotChangeTheTable(t *testing.T) {
	t.Parallel()
	a, _, _ := ModeArgsAllow("claude", ModeEdits, nil, []string{"git"})
	b, _, _ := ModeArgs("claude", ModeEdits, nil)
	if len(a) != 3 || !slices.Equal(b, []string{"--permission-mode", "acceptEdits"}) {
		t.Fatalf("the allow-list leaked into the mode table: %v then %v", a, b)
	}
}

func TestSupportsMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent string
		mode  Mode
		want  bool
	}{
		{"claude", ModeAuto, true},
		{"claude", ModeEdits, true},
		{"agy", ModeEdits, true},
		{"agy", ModeAuto, false},
		{"codex", ModeEdits, false},
		{"codex", ModeAuto, true},
		{"cursor-agent", ModeEdits, false},
		{"cursor-agent", ModeAuto, true},
		{"cursor-agent", ModeSkip, true},
		{"aider", ModeAsk, true},
		{"aider", ModeEdits, false},
	}
	for _, tt := range tests {
		t.Run(tt.agent+"/"+string(tt.mode), func(t *testing.T) {
			t.Parallel()
			if got := SupportsMode(tt.agent, tt.mode); got != tt.want {
				t.Fatalf("SupportsMode = %v", got)
			}
		})
	}
}

func TestLaunchArgvPerMode(t *testing.T) {
	claude := []string{"claude", "--mcp-config", "/m.json", "--append-system-prompt-file", "/i.md", "--settings", ClaudeSettings}
	tests := []struct {
		agent string
		mode  Mode
		want  []string
		err   error
	}{
		{"claude", ModeAsk, append(slices.Clone(claude), "--permission-mode", "manual", "hi"), nil},
		{"claude", ModeAuto, append(slices.Clone(claude), "--permission-mode", "auto", "hi"), nil},
		{"claude", ModeSkip, append(slices.Clone(claude), "--dangerously-skip-permissions", "hi"), nil},
		{"claude", ModeEdits, append(slices.Clone(claude), "--permission-mode", "acceptEdits", "hi"), nil},
		{"codex", ModeEdits, nil, ErrModeUnsupported},
		{"cursor-agent", ModeEdits, nil, ErrModeUnsupported},
		{"agy", ModeEdits, []string{"agy", "--mode", "accept-edits", "hi"}, nil},
		{"aider", ModeEdits, nil, ErrModeUnsupported},
		{"codex", ModeAsk, []string{"codex", "--sandbox", "workspace-write", "--ask-for-approval", "on-request", "hi"}, nil},
		{"codex", ModeAuto, []string{"codex", "--approve-for-me", "-c", "sandbox_workspace_write.network_access=true", "hi"}, nil},
		{"codex", ModeSkip, []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "hi"}, nil},
		{"cursor-agent", ModeAsk, []string{"cursor-agent", "hi"}, nil},
		{"cursor-agent", ModeAuto, []string{"cursor-agent", "--sandbox", "enabled", "hi"}, nil},
		{"cursor-agent", ModeSkip, []string{"cursor-agent", "--force", "hi"}, nil},
		{"/usr/local/bin/agent", ModeSkip, []string{"/usr/local/bin/agent", "--force", "hi"}, nil},
		{"agy", ModeAsk, []string{"agy", "hi"}, nil},
		{"agy", ModeAuto, nil, ErrModeUnsupported},
		{"agy", ModeSkip, []string{"agy", "--dangerously-skip-permissions", "hi"}, nil},
		{"aider", ModeAsk, []string{"aider", "hi"}, nil},
		{"aider", ModeAuto, nil, ErrModeUnsupported},
		{"aider", ModeSkip, nil, ErrModeUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.agent+"/"+string(tt.mode), func(t *testing.T) {
			agentArgs := []string{"hi"}
			modeArgs, overridden, err := ModeArgs(tt.agent, tt.mode, agentArgs)
			if !errors.Is(err, tt.err) || overridden {
				t.Fatalf("ModeArgs: overridden %v, err %v, want err %v", overridden, err, tt.err)
			}
			if err != nil {
				return
			}
			l := BuildLaunch(LaunchInput{
				Agent: tt.agent, AgentArgs: agentArgs, ModeArgs: modeArgs,
				MCPConfigPath: "/m.json", InstructionsPath: "/i.md",
			})
			if !slices.Equal(l.Argv, tt.want) {
				t.Fatalf("argv %v, want %v", l.Argv, tt.want)
			}
		})
	}
}

func TestAgentArgsWinOverMode(t *testing.T) {
	tests := []struct {
		agent     string
		mode      Mode
		agentArgs []string
	}{
		{"claude", ModeSkip, []string{"--permission-mode", "plan"}},
		{"claude", ModeAsk, []string{"--dangerously-skip-permissions"}},
		{"codex", ModeAuto, []string{"--sandbox", "read-only"}},
		{"codex", ModeAsk, []string{"-a=never"}},
		{"codex", ModeSkip, []string{"--approve-for-me"}},
		{"cursor-agent", ModeAuto, []string{"--yolo"}},
		{"cursor-agent", ModeAuto, []string{"--sandbox", "disabled"}},
		{"agy", ModeEdits, []string{"--mode", "plan"}},
		{"agy", ModeEdits, []string{"--mode=accept-edits"}},
		{"claude", ModeEdits, []string{"--permission-mode", "plan"}},
	}
	for _, tt := range tests {
		t.Run(tt.agent+"/"+string(tt.mode), func(t *testing.T) {
			args, overridden, err := ModeArgs(tt.agent, tt.mode, tt.agentArgs)
			if err != nil || !overridden || args != nil {
				t.Fatalf("got %v, overridden %v, err %v", args, overridden, err)
			}
		})
	}
}

func TestUnsupportedModeNamesTheAlternatives(t *testing.T) {
	_, _, err := ModeArgs("cursor-agent", ModeEdits, nil)
	if err == nil || err.Error() != "mode not supported: cursor-agent has no edits mode; use ask, auto or skip" {
		t.Fatalf("got %v", err)
	}
	_, _, err = ModeArgs("agy", ModeAuto, nil)
	if err == nil || err.Error() != "mode not supported: agy has no auto mode; use ask, edits or skip" {
		t.Fatalf("got %v", err)
	}
}

func TestAgentArgsWinOverEditsWithAnAllowList(t *testing.T) {
	t.Parallel()
	args, overridden, err := ModeArgsAllow("claude", ModeEdits, []string{"--dangerously-skip-permissions"}, []string{"git"})
	if err != nil || !overridden || args != nil {
		t.Fatalf("got %v, overridden %v, err %v", args, overridden, err)
	}
}
