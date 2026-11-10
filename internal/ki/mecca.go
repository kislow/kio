package ki

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MeccaSession is the tmux session that holds one window per agent session.
const MeccaSession = "ki-mecca"

// MeccaIdleAfter is how long a window must be quiet before ls calls it idle:
// agents redraw a spinner while they work, so silence means they are waiting.
const MeccaIdleAfter = 15 * time.Second

// ErrMeccaArgs wraps every argument error of `ki mecca`.
var ErrMeccaArgs = errors.New("invalid ki mecca arguments")

// MeccaNewArgs are the parsed arguments of `ki mecca new`.
type MeccaNewArgs struct {
	Target    string
	Agent     string
	Task      string
	Mode      string
	Name      string
	Brains    []string
	Model     string
	Force     bool
	Attach    bool
	JSON      bool
	AgentArgs []string
}

// ParseMeccaNew parses `ki mecca new <project|path> [flags] [-- agent args]`.
// Unlike `ki` itself, unknown flags are errors: they would otherwise reach the
// agent inside a window where the mistake is easy to miss.
func ParseMeccaNew(args []string, defaultAgent string) (MeccaNewArgs, error) {
	r := MeccaNewArgs{Agent: defaultAgent}
	values := map[string]*string{"--agent": &r.Agent, "--task": &r.Task, "--mode": &r.Mode, "--name": &r.Name, "--model": &r.Model}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if dst, ok := values[a]; ok {
			if i+1 >= len(args) {
				return r, fmt.Errorf("%w: %s needs a value", ErrMeccaArgs, a)
			}
			*dst = args[i+1]
			i++
			continue
		}
		switch {
		case a == "--brain":
			if i+1 >= len(args) {
				return r, fmt.Errorf("%w: --brain needs a value", ErrMeccaArgs)
			}
			r.Brains = append(r.Brains, args[i+1])
			i++
		case a == "--force":
			r.Force = true
		case a == "--attach":
			r.Attach = true
		case a == "--json":
			r.JSON = true
		case a == "--":
			r.AgentArgs = append(r.AgentArgs, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-"):
			return r, fmt.Errorf("%w: unknown flag %s (agent flags go after --)", ErrMeccaArgs, a)
		case r.Target == "":
			r.Target = a
		default:
			return r, fmt.Errorf("%w: unexpected argument %q", ErrMeccaArgs, a)
		}
	}
	return r, r.validate()
}

func (r MeccaNewArgs) validate() error {
	var problems []string
	if r.Target == "" {
		problems = append(problems, "missing project name or path")
	}
	if r.Agent == "" || strings.ContainsAny(r.Agent, " \t\n") {
		problems = append(problems, fmt.Sprintf("invalid agent %q", r.Agent))
	}
	if r.Mode != "" {
		// The window's ki checks this again; checking here keeps a typo from
		// opening a window that only shows the refusal.
		m, err := ParseMode(r.Mode)
		if err == nil {
			_, _, err = ModeArgs(r.Agent, m, r.AgentArgs)
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if r.Name != "" && !validWindowName(r.Name) {
		problems = append(problems, fmt.Sprintf("invalid window name %q (no spaces, dots or colons)", r.Name))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrMeccaArgs, strings.Join(problems, "; "))
	}
	return nil
}

// Dots and colons separate session, window and pane in tmux targets.
func validWindowName(n string) bool {
	return n != "" && !strings.ContainsAny(n, " \t\n.:")
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// maxSlug keeps window names short enough for the tmux status bar.
const maxSlug = 24

// Slug turns a task description into a short window-name part.
func Slug(s string) string {
	s = strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	return s
}

// WindowName is `<project>/<task-slug>`, or the project alone without a task.
func WindowName(project, task string) string {
	name := strings.NewReplacer(".", "-", ":", "-", " ", "-").Replace(project)
	if slug := Slug(task); slug != "" {
		name += "/" + slug
	}
	return name
}

// UniqueName appends -2, -3, ... until name is not taken.
func UniqueName(name string, taken []string) string {
	if !slices.Contains(taken, name) {
		return name
	}
	for n := 2; ; n++ {
		if c := name + "-" + strconv.Itoa(n); !slices.Contains(taken, c) {
			return c
		}
	}
}

// MeccaKiArgv is the command a window runs: this ki binary with the session
// flags, so the mode is visible in the window's command line.
func MeccaKiArgv(kiBinary string, a MeccaNewArgs) []string {
	argv := []string{kiBinary, "--agent", a.Agent}
	if a.Mode != "" {
		argv = append(argv, "--mode", a.Mode)
	}
	for _, b := range a.Brains {
		argv = append(argv, "--brain", b)
	}
	if a.Model != "" {
		argv = append(argv, "--model", a.Model)
	}
	if a.Force {
		argv = append(argv, "--force")
	}
	if len(a.AgentArgs) > 0 {
		argv = append(append(argv, "--"), a.AgentArgs...)
	}
	return argv
}

// meccaForwardEnv is what a window needs from the caller. tmux windows inherit
// the tmux server's environment, which can be days older than the caller's.
// Session secrets (KI_TOKEN, KI_BRAINS, GH_TOKEN) are never forwarded: the
// window's ki sets them for its own repo.
var meccaForwardEnv = []string{"PATH", "KI_HOME", "KI_CONFIG_DIR", "KI_CONFIG", "KI_VAULT", "KI_MCP_URL", "KI_MODEL", "KI_MODE"}

// MeccaEnv is the environment passed to a new window with `tmux -e`.
func MeccaEnv(getenv func(string) string) map[string]string {
	env := map[string]string{}
	for _, k := range meccaForwardEnv {
		if v := getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// Window is one window in the ki-mecca session.
type Window struct {
	ID         string    `json:"id"`
	Index      int       `json:"index"`
	Name       string    `json:"name"`
	Project    string    `json:"project,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Identity   string    `json:"identity,omitempty"`
	Task       string    `json:"task,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Dir        string    `json:"dir,omitempty"`
	Created    time.Time `json:"created,omitzero"`
	Activity   time.Time `json:"activity,omitzero"`
	Dead       bool      `json:"dead"`
	ExitStatus int       `json:"exit_status,omitempty"`
	Status     string    `json:"status"`
	Note       string    `json:"note,omitempty"`
}

// Window user options, set when ki mecca opens a window.
const (
	OptProject  = "@ki-project"
	OptAgent    = "@ki-agent"
	OptIdentity = "@ki-identity"
	OptTask     = "@ki-task"
	OptMode     = "@ki-mode"
	OptDir      = "@ki-dir"
	OptCreated  = "@ki-created"
)

var listFields = []string{
	"window_id", "window_index", "window_name", OptProject, OptAgent, OptIdentity, OptTask,
	OptMode, OptDir, OptCreated, "window_activity", "pane_dead", "pane_dead_status",
}

// ListFormat is the -F format for `tmux list-windows`, one tab-separated field
// per listFields entry.
var ListFormat = func() string {
	parts := make([]string, len(listFields))
	for i, f := range listFields {
		parts[i] = "#{" + f + "}"
	}
	return strings.Join(parts, "\t")
}()

// CleanOption makes a value safe to store in a window option and read back
// through ListFormat: tabs and newlines would split the record.
func CleanOption(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ParseWindows reads `tmux list-windows -F ListFormat` output.
func ParseWindows(out string) ([]Window, error) {
	var ws []Window
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != len(listFields) {
			return nil, fmt.Errorf("unexpected tmux list-windows line %q", line)
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("window index %q: %w", f[1], err)
		}
		w := Window{
			ID: f[0], Index: idx, Name: f[2], Project: f[3], Agent: f[4], Identity: f[5],
			Task: f[6], Mode: f[7], Dir: f[8], Created: unix(f[9]), Activity: unix(f[10]), Dead: f[11] == "1",
		}
		if w.Dead {
			w.ExitStatus, _ = strconv.Atoi(f[12])
		}
		ws = append(ws, w)
	}
	return ws, nil
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Window statuses reported by ls.
const (
	StatusWorking   = "working"
	StatusIdle      = "idle"
	StatusNeedsUser = "needs-user"
	StatusWaiting   = "waiting"
	StatusDone      = "done"
	StatusExited    = "exited"
)

// statusLine matches the one-line turn summary agents end a turn with, the
// convention other session readers use too.
var statusLine = regexp.MustCompile(`(NEEDS USER|WAITING|DONE):\s*(.*?)\s*$`)

var statusByMarker = map[string]string{"NEEDS USER": StatusNeedsUser, "WAITING": StatusWaiting, "DONE": StatusDone}

// NeedsTail reports whether Classify needs the pane's visible text: only quiet,
// live windows do, so ls skips capture-pane for the rest.
func NeedsTail(w Window, now time.Time) bool {
	return !w.Dead && !recent(w, now)
}

func recent(w Window, now time.Time) bool {
	return !w.Activity.IsZero() && now.Sub(w.Activity) < MeccaIdleAfter
}

// Classify sets w.Status and w.Note from the window state and, for a quiet
// window, the last lines on its screen.
func Classify(w Window, tail string, now time.Time) Window {
	switch {
	case w.Dead:
		w.Status, w.Note = StatusExited, fmt.Sprintf("exit status %d", w.ExitStatus)
	case recent(w, now):
		w.Status = StatusWorking
	default:
		w.Status = StatusIdle
		lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
		for _, l := range slices.Backward(lines) {
			if m := statusLine.FindStringSubmatch(l); m != nil {
				w.Status, w.Note = statusByMarker[m[1]], m[2]
				break
			}
		}
	}
	return w
}

// ErrNoWindow means no window matched a reference.
var ErrNoWindow = errors.New("no such ki-mecca window")

// FindWindow matches ref against a window id (@3), index (3) or exact name.
func FindWindow(ws []Window, ref string) (Window, error) {
	var hits []Window
	for _, w := range ws {
		if ref == w.ID || ref == strconv.Itoa(w.Index) || ref == w.Name {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 0:
		return Window{}, fmt.Errorf("%w: %q (see `ki mecca ls`)", ErrNoWindow, ref)
	case 1:
		return hits[0], nil
	}
	ids := make([]string, len(hits))
	for i, w := range hits {
		ids[i] = w.ID
	}
	return Window{}, fmt.Errorf("%w: %q matches %s; use the id", ErrNoWindow, ref, strings.Join(ids, ", "))
}

// Age renders a duration the way ls shows it: 45s, 12m, 3h, 2d.
func Age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
