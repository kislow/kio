package ki

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCombineInstructions(t *testing.T) {
	for name, tt := range map[string]struct{ harness, config, want string }{
		"no config is the harness text as it is": {"# H\n", "", "# H\n"},
		"blank config is ignored":                {"# H\n", " \n\n", "# H\n"},
		"config follows after a blank line":      {"# H\n", "# C\n", "# H\n\n# C\n"},
		"trailing newlines do not stack":         {"# H\n\n\n", "\n# C\n\n", "# H\n\n# C\n"},
		"no harness text leaves the config":      {"", "# C\n", "# C\n"},
		"neither":                                {"", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CombineInstructions(tt.harness, tt.config); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstructionsRunFileFollowsTheText(t *testing.T) {
	a, b := InstructionsRunFile("/run", "one"), InstructionsRunFile("/run", "two")
	if a == b || a != InstructionsRunFile("/run", "one") {
		t.Fatalf("the name must follow the text: %s %s", a, b)
	}
	if filepath.Dir(a) != "/run" || !strings.HasPrefix(filepath.Base(a), "instructions-") || filepath.Ext(a) != ".md" {
		t.Fatalf("name %s", a)
	}
}

func TestHeadlessInstructionsGoInFrontOfTheTaskForCodexAndCursor(t *testing.T) {
	h := &Headless{Task: "do it", LastMessage: "/j/last.txt", Instructions: "# I\n"}
	want := "# I\n\n---\n\ndo it"
	for agent, task := range map[string]string{"codex": want, "cursor-agent": want, "agent": want, "claude": "do it", "agy": "do it"} {
		args := headlessArgs(agent, h)
		if got := args[len(args)-1]; got != task {
			t.Errorf("%s: got %q, want %q", agent, got, task)
		}
	}
	if got := headlessArgs("codex", &Headless{Task: "do it", LastMessage: "/l"}); !reflect.DeepEqual(got, []string{"--json", "--output-last-message", "/l", "do it"}) {
		t.Fatalf("no instructions leaves the task alone: %v", got)
	}
}
