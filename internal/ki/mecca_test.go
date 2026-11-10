package ki

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseMeccaNew(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    MeccaNewArgs
		wantErr string
	}{
		{"project only", []string{"app"}, MeccaNewArgs{Target: "app", Agent: "claude"}, ""},
		{
			"every flag",
			[]string{"app", "--agent", "codex", "--task", "Fix it", "--mode", "auto", "--name", "fix", "--brain", "voice", "--brain", "app", "--model", "sonnet", "--force", "--attach", "--json", "--", "--resume", "x"},
			MeccaNewArgs{Target: "app", Agent: "codex", Task: "Fix it", Mode: "auto", Name: "fix", Brains: []string{"voice", "app"}, Model: "sonnet", Force: true, Attach: true, JSON: true, AgentArgs: []string{"--resume", "x"}},
			"",
		},
		{"flags before the target", []string{"--mode", "skip", "~/code/app"}, MeccaNewArgs{Target: "~/code/app", Agent: "claude", Mode: "skip"}, ""},
		{"missing target", []string{"--force"}, MeccaNewArgs{}, "missing project name or path"},
		{"value flag at the end", []string{"app", "--task"}, MeccaNewArgs{}, "--task needs a value"},
		{"brain at the end", []string{"app", "--brain"}, MeccaNewArgs{}, "--brain needs a value"},
		{"unknown flag", []string{"app", "--resume"}, MeccaNewArgs{}, "unknown flag --resume"},
		{"second target", []string{"app", "other"}, MeccaNewArgs{}, `unexpected argument "other"`},
		{"collects every problem", []string{"--agent", "a b", "--mode", "yolo", "--name", "a.b"}, MeccaNewArgs{}, `missing project name or path; invalid agent "a b"; unknown mode "yolo": use ask, auto, edits or skip; invalid window name "a.b"`},
		{"mode the agent lacks", []string{"app", "--agent", "agy", "--mode", "auto"}, MeccaNewArgs{}, "agy has no auto mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMeccaNew(tt.args, "claude")
			if tt.wantErr != "" {
				if !errors.Is(err, ErrMeccaArgs) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWindowName(t *testing.T) {
	tests := []struct {
		project, task, want string
	}{
		{"app", "", "app"},
		{"app", "Fix the login: 401s!", "app/fix-the-login-401s"},
		{"my.app:x y", "", "my-app-x-y"},
		{"app", "a very long task description that goes on", "app/a-very-long-task-descrip"},
		{"app", "abcdefghijklmnopqrstuvw xyz", "app/abcdefghijklmnopqrstuvw"},
		{"app", "!!!", "app"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := WindowName(tt.project, tt.task); got != tt.want {
				t.Fatalf("WindowName(%q, %q) = %q, want %q", tt.project, tt.task, got, tt.want)
			}
		})
	}
}

func TestUniqueName(t *testing.T) {
	tests := []struct {
		name  string
		taken []string
		want  string
	}{
		{"app", nil, "app"},
		{"app", []string{"app"}, "app-2"},
		{"app", []string{"app", "app-2", "app-3"}, "app-4"},
		{"app", []string{"app-2"}, "app"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.taken, ","), func(t *testing.T) {
			if got := UniqueName(tt.name, tt.taken); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMeccaKiArgv(t *testing.T) {
	tests := []struct {
		name string
		args MeccaNewArgs
		want []string
	}{
		{"defaults", MeccaNewArgs{Agent: "claude", Task: "t"}, []string{"/bin/ki", "--agent", "claude"}},
		{"mode", MeccaNewArgs{Agent: "claude", Mode: "skip"}, []string{"/bin/ki", "--agent", "claude", "--mode", "skip"}},
		{
			"everything",
			MeccaNewArgs{Agent: "codex", Brains: []string{"voice"}, Model: "o3", Force: true, AgentArgs: []string{"--resume"}},
			[]string{"/bin/ki", "--agent", "codex", "--brain", "voice", "--model", "o3", "--force", "--", "--resume"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MeccaKiArgv("/bin/ki", tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMeccaEnv(t *testing.T) {
	vars := map[string]string{
		"PATH": "/bin", "KI_HOME": "/h", "KI_MODE": "auto", "KI_TOKEN": "secret", "GH_TOKEN": "gh", "KI_BRAINS": "client",
	}
	want := map[string]string{"PATH": "/bin", "KI_HOME": "/h", "KI_MODE": "auto"}
	if got := MeccaEnv(func(k string) string { return vars[k] }); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCleanOption(t *testing.T) {
	if got := CleanOption(" fix\tthe\n login  "); got != "fix the login" {
		t.Fatalf("got %q", got)
	}
}

func TestListFormat(t *testing.T) {
	if n := strings.Count(ListFormat, "\t") + 1; n != len(listFields) {
		t.Fatalf("%d fields in %q", n, ListFormat)
	}
	if !strings.Contains(ListFormat, "#{@ki-project}") {
		t.Fatalf("format %q does not read the project option", ListFormat)
	}
}

func windowLine(fields ...string) string { return strings.Join(fields, "\t") }

func TestParseWindows(t *testing.T) {
	live := windowLine("@1", "0", "home", "", "", "", "", "", "", "", "1700000000", "0", "")
	labelled := windowLine("@4", "2", "app/fix", "app", "claude", "work", "Fix it", "auto", "/code/app", "1699999000", "1700000100", "1", "3")
	tests := []struct {
		name    string
		out     string
		want    []Window
		wantErr string
	}{
		{"empty", "", nil, ""},
		{
			"two windows",
			live + "\n" + labelled + "\n",
			[]Window{
				{ID: "@1", Index: 0, Name: "home", Activity: time.Unix(1700000000, 0)},
				{
					ID: "@4", Index: 2, Name: "app/fix", Project: "app", Agent: "claude", Identity: "work", Task: "Fix it", Mode: "auto",
					Dir: "/code/app", Created: time.Unix(1699999000, 0), Activity: time.Unix(1700000100, 0), Dead: true, ExitStatus: 3,
				},
			},
			"",
		},
		{"short line", "@1\t0\thome\n", nil, "unexpected tmux list-windows line"},
		{"bad index", windowLine("@1", "x", "home", "", "", "", "", "", "", "", "", "0", ""), nil, `window index "x"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWindows(tt.out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	now := time.Unix(1700000000, 0)
	quiet := now.Add(-time.Minute)
	tests := []struct {
		name      string
		w         Window
		tail      string
		status    string
		note      string
		needsTail bool
	}{
		{"dead", Window{Dead: true, ExitStatus: 2}, "", StatusExited, "exit status 2", false},
		{"busy", Window{Activity: now.Add(-time.Second)}, "DONE: old", StatusWorking, "", false},
		{"quiet without a marker", Window{Activity: quiet}, "$ \n", StatusIdle, "", true},
		{"no activity recorded", Window{}, "", StatusIdle, "", true},
		{"needs user", Window{Activity: quiet}, "x\nNEEDS USER: pick a branch  \n\n", StatusNeedsUser, "pick a branch", true},
		{"last marker wins", Window{Activity: quiet}, "DONE: first\nWAITING: on CI\n> ", StatusWaiting, "on CI", true},
		{"done", Window{Activity: quiet}, "DONE: shipped #12", StatusDone, "shipped #12", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsTail(tt.w, now); got != tt.needsTail {
				t.Fatalf("NeedsTail = %v, want %v", got, tt.needsTail)
			}
			got := Classify(tt.w, tt.tail, now)
			if got.Status != tt.status || got.Note != tt.note {
				t.Fatalf("got %q %q, want %q %q", got.Status, got.Note, tt.status, tt.note)
			}
		})
	}
}

func TestFindWindow(t *testing.T) {
	ws := []Window{{ID: "@1", Index: 0, Name: "home"}, {ID: "@2", Index: 1, Name: "app"}, {ID: "@3", Index: 2, Name: "1"}}
	tests := []struct {
		ref, want, wantErr string
	}{
		{"@2", "@2", ""},
		{"0", "@1", ""},
		{"app", "@2", ""},
		{"1", "", "matches @2, @3; use the id"},
		{"nope", "", `"nope" (see`},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			w, err := FindWindow(ws, tt.ref)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrNoWindow) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || w.ID != tt.want {
				t.Fatalf("got %v %v, want %s", w.ID, err, tt.want)
			}
		})
	}
}

func TestAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{3 * time.Hour, "3h"},
		{47 * time.Hour, "47h"},
		{50 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := Age(tt.d); got != tt.want {
				t.Fatalf("Age(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
