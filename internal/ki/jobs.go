package ki

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Result is one job as written to jobs/<id>/result.json and printed by
// `ki delegate --json`. The ledger holds the same minus Last.
type Result struct {
	ID string `json:"id"`
	// Title is a short human label: --title, else the issue title, else the brief's first sentence.
	Title    string `json:"title,omitempty"`
	Provider string `json:"provider"`
	Role     string `json:"role,omitempty"`
	// Model is the model that ran (see ResolvedModel); ModelRequested is the
	// one ki passed, "" when the agent chose. The latter is always written so
	// a record from before the split, which has only model, can be told apart.
	Model          string `json:"model,omitempty"`
	ModelRequested string `json:"model_requested"`
	Repo           string `json:"repo,omitempty"`
	Issue          string `json:"issue,omitempty"`
	// Kind is "" for a delegated task, "review" for the agent `ki approve --review` runs
	// and "approve" for the approval itself; PR is the pull request (owner/repo#N) of the last two.
	Kind     string `json:"kind,omitempty"`
	PR       string `json:"pr,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	// Note is what the planner must still do or know: a check ki skipped for a sandboxed worker.
	Note       string     `json:"note,omitempty"`
	Session    string     `json:"session,omitempty"`
	Last       string     `json:"last,omitempty"`
	Started    time.Time  `json:"started"`
	DurationS  float64    `json:"duration_s"`
	QuotaUntil *time.Time `json:"quota_until,omitempty"`
	Tokens     Tokens     `json:"tokens"`
	CostUSD    float64    `json:"cost_usd,omitempty"`
	Exit       int        `json:"exit"`
	// Pid is the worker while the job is RUNNING; the final result.json leaves it out.
	Pid int `json:"pid,omitempty"`
	// KiPid is the ki process that owns the job while it is RUNNING. The worker's
	// pid is gone before ki has recorded the end, so only both gone means LOST.
	KiPid int `json:"ki_pid,omitempty"`
	// LastEventAt is when a streaming worker last printed an event (its start
	// before the first one). Absent for an agent that prints once at the end.
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	// StallS is the silence, in seconds, after which ki ends the job as STALLED.
	StallS float64 `json:"stall_s,omitempty"`
	// TimedOut marks a FAILED job ki killed at --timeout, which routing reads as a hung provider.
	TimedOut bool `json:"timed_out,omitempty"`
	// Tail is the last progress lines of a job ended as STALLED, QUOTA or at the timeout.
	Tail []string `json:"tail,omitempty"`
	// Commits and DiffStat are how far a QUOTA job got: its commits on the
	// branch (oldest first, `<sha> <subject>`) and `git diff --stat` against
	// where the branch started, uncommitted changes included.
	Commits  []string `json:"commits,omitempty"`
	DiffStat string   `json:"diff_stat,omitempty"`
	// ResumedFrom is the job this one continues (`ki delegate resume`).
	ResumedFrom string `json:"resumed_from,omitempty"`
	// Continues is the ask job this one continues (`ki ask --continue`), and
	// Resumed how: "session" when the agent reopened the earlier session,
	// "transcript" when a fresh run was given the earlier turns.
	Continues string `json:"continues,omitempty"`
	Resumed   string `json:"resumed,omitempty"`
	// Ask is what a later `ki ask --continue` needs from an ask job.
	Ask *AskState `json:"ask,omitempty"`
	// Queued is when the job entered the queue, absent for one that started at once.
	Queued *time.Time `json:"queued,omitempty"`
	// StartAfter is, for a QUEUED job, the earliest time a provider of its role is expected back.
	StartAfter *time.Time `json:"start_after,omitempty"`
}

// The suffix is 8 hex since jobs started in the same second collided at 4;
// ids from before that stay valid.
var jobID = regexp.MustCompile(`^j-\d{8}-\d{6}-(?:[0-9a-f]{4}){1,2}$`)

// NewJobID is time-sortable and unique enough for one machine: j-<utc stamp>-<8 hex>.
func NewJobID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return "j-" + now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// ValidJobID reports whether s is a job id NewJobID could have made, so a
// path built from it stays under the jobs folder.
func ValidJobID(s string) bool { return jobID.MatchString(s) }

// LedgerFile is the per-machine job history under the jobs folder, one JSON line per job.
const LedgerFile = "ledger.jsonl"

// AppendLedger records a finished job. Last, Tail and the progress of a
// QUOTA job are left out: they live in the job's result.json.
func AppendLedger(path string, r Result) error {
	r.Last, r.Tail, r.Commits, r.DiffStat, r.Ask = "", nil, nil, "", nil
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding job %s: %w", r.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- under the ki config dir
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending to %s: %w", path, err)
	}
	return nil
}

// ReadLedger returns every recorded job, oldest first; no file means none.
func ReadLedger(path string) ([]Result, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- under the ki config dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []Result
	for n, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r Result
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n+1, err)
		}
		var split struct {
			Requested *string `json:"model_requested"`
		}
		if json.Unmarshal(line, &split) == nil && split.Requested == nil {
			// Before the split, model was the model ki passed.
			r.ModelRequested = r.Model
		}
		out = append(out, r)
	}
	return out, nil
}
