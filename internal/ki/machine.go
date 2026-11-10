package ki

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// MachineFile is the per-machine file under the ki config directory. It
// says which identities from projects.yaml this machine carries (whose gh
// logins doctor expects here, and whose repos ki will start sessions in) and
// which model each class of work runs on, per agent. projects.yaml is shared
// by every machine; this file is the one place a laptop differs from the
// others. No file means every identity and the agents' own defaults.
const MachineFile = "machine.yaml"

// Task classes a model policy can name. The session itself is judgement
// work; subagents that apply a described change are execution; read-only
// lookups are search; light prose (summaries, changelogs, commit and PR
// text, status posts) is writing.
const (
	ClassJudgement = "judgement"
	ClassExecution = "execution"
	ClassSearch    = "search"
	ClassWriting   = "writing"
)

// Classes lists the task classes in the order the machine file renders them.
var Classes = []string{ClassJudgement, ClassExecution, ClassSearch, ClassWriting}

// classList names the classes for an error message.
var classList = strings.Join(Classes, ", ")

// Efforts Claude Code accepts; other agents ignore effort.
var efforts = map[string]bool{"low": true, "medium": true, "high": true}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// Machine is the parsed machine file.
type Machine struct {
	Identities []string
	// Models maps a task class to the model per agent: models.execution.claude
	// = sonnet, models.execution.codex = gpt-5-mini. Agent names are whatever
	// `ki --agent` takes (the binary's base name), so any vendor fits.
	Models map[string]map[string]string
	// Effort maps a task class to low, medium or high (Claude Code only).
	Effort map[string]string
	// Roles maps a task class to the providers that may run it headless
	// (ki delegate --role): see policy.go.
	Roles map[string]Role
	// Delegate is the delegate block: what a worker in edits mode may run.
	Delegate Delegate
	// Sections holds the blocks of registered sections (see machineSection),
	// so a rewrite of the file keeps them.
	Sections map[string]any
}

// Delegate is the machine's policy for headless workers.
type Delegate struct {
	// Allow lists the shell commands a worker in edits mode may run, each a
	// binary or `binary subcommand`, in the order the file gives them.
	Allow []string
	// Stall is how long a streaming worker may print nothing before ki ends it
	// as STALLED; zero means DefaultStall.
	Stall time.Duration
	// NotifyStall asks for a macOS notification when a job stalls.
	NotifyStall bool
	// Queue makes --when-available the default: a job whose role has no
	// provider available waits in the queue instead of being refused.
	Queue bool
	// QueueParallel is how many queued jobs may run at once; zero means DefaultQueueParallel.
	QueueParallel int
}

type rawMachine struct {
	Identities []string                     `yaml:"identities"`
	Models     map[string]map[string]string `yaml:"models"`
	Effort     map[string]string            `yaml:"effort"`
	Roles      map[string]rawRole           `yaml:"roles"`
	Delegate   *rawDelegate                 `yaml:"delegate"`
	// sections are the registered blocks, kept out of the strict decode.
	sections map[string]ast.Node
}

type rawDelegate struct {
	Allow         []string `yaml:"allow"`
	Stall         string   `yaml:"stall"`
	NotifyStall   bool     `yaml:"notify_stall"`
	Queue         bool     `yaml:"queue"`
	QueueParallel int      `yaml:"queue_parallel"`
}

// LoadMachine reads the machine file. A missing file returns nil, nil: the
// machine carries every identity and sets no models. Unknown keys, unknown
// identities, unknown classes and malformed names are errors, so a typo
// fails instead of silently widening or narrowing.
func LoadMachine(path string, known map[string]struct{}) (*Machine, error) {
	raw, ok, err := readRawMachine(path)
	if err != nil || !ok {
		return nil, err
	}
	if err := validateIdentities(path, raw.Identities, known); err != nil {
		return nil, err
	}
	return machineFromRaw(path, raw)
}

// LoadPolicy reads the models, effort, roles and delegate blocks and ignores
// whether identities still name something in projects.yaml. setup machine
// uses it when the file does not validate, so a renamed identity can be
// replaced without dropping a policy that still parses. A missing file
// returns nil, nil. A block that does not parse is an error, so a rewrite
// cannot drop it.
func LoadPolicy(path string) (*Machine, error) {
	raw, ok, err := readRawMachine(path)
	if err != nil || !ok {
		return nil, err
	}
	return machineFromRaw(path, raw)
}

func readRawMachine(path string) (rawMachine, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path is under the ki config dir
	if errors.Is(err, os.ErrNotExist) {
		return rawMachine{}, false, nil
	}
	if err != nil {
		return rawMachine{}, false, fmt.Errorf("reading %s: %w", path, err)
	}
	var raw rawMachine
	if len(machineSections) == 0 {
		if err := yaml.UnmarshalWithOptions(data, &raw, yaml.Strict()); err != nil {
			return rawMachine{}, false, fmt.Errorf("%s: %w", path, err)
		}
		return raw, true, nil
	}
	if raw.sections, err = splitSections(path, data, &raw); err != nil {
		return rawMachine{}, false, err
	}
	return raw, true, nil
}

func validateIdentities(path string, ids []string, known map[string]struct{}) error {
	if len(ids) == 0 {
		return fmt.Errorf("%s: identities must list at least one identity from projects.yaml", path)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%s: identity %q is not in projects.yaml (known: %s)", path, id, strings.Join(sortedKeys(known), ", "))
		}
		if seen[id] {
			return fmt.Errorf("%s: identity %q is listed twice", path, id)
		}
		seen[id] = true
	}
	return nil
}

func machineFromRaw(path string, raw rawMachine) (*Machine, error) {
	for class, perAgent := range raw.Models {
		if !isClass(class) {
			return nil, fmt.Errorf("%s: models.%s: unknown class (%s)", path, class, classList)
		}
		for agent, model := range perAgent {
			if agent == "" || !modelName.MatchString(model) {
				return nil, fmt.Errorf("%s: models.%s.%s: %q is not a model name", path, class, agent, model)
			}
		}
	}
	for class, effort := range raw.Effort {
		if !isClass(class) {
			return nil, fmt.Errorf("%s: effort.%s: unknown class (%s)", path, class, classList)
		}
		if !efforts[effort] {
			return nil, fmt.Errorf("%s: effort.%s: %q is not low, medium or high", path, class, effort)
		}
	}
	roles, err := parseRoles(path, raw.Roles)
	if err != nil {
		return nil, err
	}
	d, err := parseDelegate(path, raw.Delegate)
	if err != nil {
		return nil, err
	}
	sections, err := parseSections(path, raw.sections)
	if err != nil {
		return nil, err
	}
	return &Machine{Identities: raw.Identities, Models: raw.Models, Effort: raw.Effort, Roles: roles, Delegate: d, Sections: sections}, nil
}

// allowEntry is one delegate.allow entry: a binary, optionally one
// subcommand, each starting with a letter or digit.
var allowEntry = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*( [A-Za-z0-9][A-Za-z0-9._-]*)?$`)

// parseDelegate validates the delegate block. The allow-list becomes agent
// permission rules, so anything wider than one named command is refused.
func parseDelegate(path string, raw *rawDelegate) (Delegate, error) {
	if raw == nil {
		return Delegate{}, nil
	}
	if len(raw.Allow) == 0 && raw.Stall == "" && !raw.NotifyStall && !raw.Queue && raw.QueueParallel == 0 {
		return Delegate{}, fmt.Errorf("%s: delegate.allow must list at least one command (a binary like git, or `binary subcommand`)", path)
	}
	if raw.QueueParallel < 0 {
		return Delegate{}, fmt.Errorf("%s: delegate.queue_parallel: %d cannot be negative", path, raw.QueueParallel)
	}
	d := Delegate{NotifyStall: raw.NotifyStall, Queue: raw.Queue, QueueParallel: raw.QueueParallel}
	if raw.Stall != "" {
		stall, err := time.ParseDuration(raw.Stall)
		if err != nil || stall <= 0 {
			return Delegate{}, fmt.Errorf("%s: delegate.stall: %q is not a positive duration like 15m", path, raw.Stall)
		}
		d.Stall = stall
	}
	seen := map[string]bool{}
	for i, e := range raw.Allow {
		switch {
		case strings.Contains(e, "*"):
			return Delegate{}, fmt.Errorf("%s: delegate.allow[%d]: %q: wildcards are refused, list each command", path, i, e)
		case !allowEntry.MatchString(e):
			return Delegate{}, fmt.Errorf("%s: delegate.allow[%d]: %q is not a command: use a binary name or `binary subcommand` (letters, digits, '.', '_', '-', one space)", path, i, e)
		case seen[e]:
			return Delegate{}, fmt.Errorf("%s: delegate.allow: %q is listed twice", path, e)
		}
		seen[e] = true
	}
	d.Allow = raw.Allow
	return d, nil
}

// StallLimit is how long a streaming worker may be silent before ki ends it.
func (m *Machine) StallLimit() time.Duration {
	if m == nil || m.Delegate.Stall == 0 {
		return DefaultStall
	}
	return m.Delegate.Stall
}

// NotifyStall reports whether a stalled job should raise a notification.
func (m *Machine) NotifyStall() bool { return m != nil && m.Delegate.NotifyStall }

// DefaultQueueParallel is how many queued jobs run at once unless delegate.queue_parallel says otherwise.
const DefaultQueueParallel = 2

// QueueByDefault reports whether a job with no provider available is queued without --when-available.
func (m *Machine) QueueByDefault() bool { return m != nil && m.Delegate.Queue }

// QueueParallel is how many queued jobs may run at once.
func (m *Machine) QueueParallel() int {
	if m == nil || m.Delegate.QueueParallel == 0 {
		return DefaultQueueParallel
	}
	return m.Delegate.QueueParallel
}

func (d Delegate) isZero() bool {
	return len(d.Allow) == 0 && d.Stall == 0 && !d.NotifyStall && !d.Queue && d.QueueParallel == 0
}

// DelegateAllow is the edits mode's allow-list, nil when the machine has none.
func (m *Machine) DelegateAllow() []string {
	if m == nil {
		return nil
	}
	return m.Delegate.Allow
}

func isClass(s string) bool {
	for _, c := range Classes {
		if c == s {
			return true
		}
	}
	return false
}

// Carries reports whether this machine has the identity. A nil Machine (no
// file) carries every identity.
func (m *Machine) Carries(identity string) bool {
	if m == nil {
		return true
	}
	for _, id := range m.Identities {
		if id == identity {
			return true
		}
	}
	return false
}

// ModelFor is the model the policy sets for a class and an agent, or "" when
// it sets none (the agent's own default then applies).
func (m *Machine) ModelFor(class, agent string) string {
	if m == nil {
		return ""
	}
	return m.Models[class][agent]
}

// EffortFor is the effort the policy sets for a class, or "".
func (m *Machine) EffortFor(class string) string {
	if m == nil {
		return ""
	}
	return m.Effort[class]
}

// RenderMachine is the file content for a machine: its identities and, when
// set, its model policy. With no policy the models block is written as a
// commented example, so the file documents itself.
func RenderMachine(m Machine) string {
	ids := append([]string(nil), m.Identities...)
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("# Identities from projects.yaml that this machine carries: ki doctor expects\n")
	b.WriteString("# their gh logins here, and ki starts sessions only in their repos.\n")
	b.WriteString("# Written by `ki setup machine <identity>...`; the models block is yours to edit.\n")
	b.WriteString("identities:\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "  - %s\n", id)
	}
	b.WriteString("\n# Model policy, per class of work and per agent (the `ki --agent` name, so any\n")
	b.WriteString("# vendor fits): judgement is the session itself (issues, reviews, decisions),\n")
	b.WriteString("# execution is a subagent applying a described change, search is read-only\n")
	b.WriteString("# lookup, writing is light prose (summaries, changelogs, commit and PR text,\n")
	b.WriteString("# status posts). An agent not listed under a class keeps its own default;\n")
	b.WriteString("# ki --model overrides the session for one run. Effort is Claude Code only.\n")
	if len(m.Models) == 0 && len(m.Effort) == 0 && len(m.Roles) == 0 && m.Delegate.isZero() && len(m.Sections) == 0 {
		b.WriteString("# models:\n#   judgement: {claude: opus, codex: gpt-5}\n#   execution: {claude: sonnet, codex: gpt-5-mini}\n#   search: {claude: haiku}\n#   writing: {claude: haiku}\n# effort:\n#   execution: medium\n")
		b.WriteString("#\n# Roles let `ki delegate --role <class>` pick the worker: ordered keeps the\n")
		b.WriteString("# first provider busy with the rest as failover, round-robin takes weighted\n")
		b.WriteString("# turns. A provider is `agent:model` or {agent, model, weight, daily_jobs}.\n")
		b.WriteString("# The planner role is the same shape and starts a bare `ki` on its first eligible\n")
		b.WriteString("# provider (--agent and KI_AGENT win; without it `ki` starts claude).\n")
		b.WriteString("# roles:\n#   execution:\n#     strategy: ordered\n#     providers: [codex:gpt-5-mini, claude:sonnet, agy:gemini-3.8-flash-low]\n#   writing:\n#     providers: [claude:haiku]\n#   planner:\n#     providers: [claude:opus, codex:gpt-5]\n")
		b.WriteString("#\n" + delegateComment("# "))
		b.WriteString("# delegate:\n#   allow: [git, gh, go, make]\n#   stall: 15m\n#   notify_stall: true\n#   queue: true\n#   queue_parallel: 2\n")
		b.WriteString("#\n" + sessionsComment("# "))
		b.WriteString("# sessions:\n#   subagents:\n#     allow: [Explore]\n")
		return b.String()
	}
	if len(m.Models) > 0 {
		b.WriteString("models:\n")
		for _, class := range Classes {
			perAgent := m.Models[class]
			if len(perAgent) == 0 {
				continue
			}
			agents := make([]string, 0, len(perAgent))
			for a := range perAgent {
				agents = append(agents, a)
			}
			sort.Strings(agents)
			fmt.Fprintf(&b, "  %s:\n", class)
			for _, a := range agents {
				fmt.Fprintf(&b, "    %s: %s\n", a, perAgent[a])
			}
		}
	}
	if len(m.Effort) > 0 {
		b.WriteString("effort:\n")
		for _, class := range Classes {
			if e := m.Effort[class]; e != "" {
				fmt.Fprintf(&b, "  %s: %s\n", class, e)
			}
		}
	}
	renderRoles(&b, m.Roles)
	renderDelegate(&b, m.Delegate)
	renderSections(&b, m.Sections)
	return b.String()
}

// delegateComment explains the delegate block, every line starting with prefix.
func delegateComment(prefix string) string {
	return prefix + "Commands a worker in edits mode may run (ki delegate --mode edits): a binary\n" +
		prefix + "or `binary subcommand`, never a wildcard. ki passes them to Claude Code as\n" +
		prefix + "--allowedTools and adds them to agy's settings; anything else is refused.\n" +
		prefix + "stall is how long a streaming worker may print nothing before ki ends it as\n" +
		prefix + "STALLED (default 15m, --stall per job); notify_stall shows a macOS notification.\n" +
		prefix + "queue: true queues a job whose role has no provider available (as --when-available);\n" +
		prefix + "queue_parallel is how many queued jobs run at once (default 2).\n"
}

// renderDelegate writes the delegate block the way LoadMachine reads it, the
// allow-list in the file's order, each entry quoted where YAML needs it.
func renderDelegate(b *strings.Builder, d Delegate) {
	if d.isZero() {
		return
	}
	b.WriteString(delegateComment("# "))
	b.WriteString("delegate:\n")
	if len(d.Allow) > 0 {
		entries := make([]string, len(d.Allow))
		for i, e := range d.Allow {
			entries[i] = yamlScalar(e)
		}
		fmt.Fprintf(b, "  allow: [%s]\n", strings.Join(entries, ", "))
	}
	if d.Stall > 0 {
		fmt.Fprintf(b, "  stall: %s\n", d.Stall)
	}
	if d.NotifyStall {
		b.WriteString("  notify_stall: true\n")
	}
	if d.Queue {
		b.WriteString("  queue: true\n")
	}
	if d.QueueParallel > 0 {
		fmt.Fprintf(b, "  queue_parallel: %d\n", d.QueueParallel)
	}
}

// yamlScalar is s as a YAML scalar in a flow list that reads back as the same
// string: plain when YAML reads it as that string, else double-quoted (true,
// null, 1.5 and the like).
func yamlScalar(s string) string {
	var typed []any
	if yaml.Unmarshal([]byte("["+s+"]"), &typed) == nil && len(typed) == 1 && typed[0] == s {
		return s
	}
	return strconv.Quote(s)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
