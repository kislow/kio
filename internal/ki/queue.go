package ki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// A job whose role has no provider available can wait in the queue instead
// of being refused (ki delegate --when-available). The queue is one
// queue.json per job folder, so a queued job already has the id it will run
// under; `ki queue run` starts the oldest ones once a provider is back.
const (
	// QueueEntryFile is the queued job's request in its job folder.
	QueueEntryFile = "queue.json"
	// QueueLockFile is the flock taken while the queue is read and changed.
	QueueLockFile = "queue.lock"
	// QueueStartGrace is how long an entry may be marked starting without the
	// ki process that starts it recording its pid, before it goes back to waiting.
	QueueStartGrace = 2 * time.Minute
)

// QueueEntry is one queued job: what to run, where, and why it waits.
type QueueEntry struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Role  string `json:"role"`
	// Repo is the checkout the job runs in.
	Repo  string `json:"repo"`
	Issue string `json:"issue,omitempty"`
	// Args are the `ki delegate` arguments to run, without --when-available.
	Args []string `json:"args"`
	// ResumeOf is the job a queued `ki delegate resume` continues.
	ResumeOf string    `json:"resume_of,omitempty"`
	Queued   time.Time `json:"queued"`
	// Reason is why no provider was available at the last look.
	Reason     string     `json:"reason"`
	StartAfter *time.Time `json:"start_after,omitempty"`
	// Starting is set when `ki queue run` hands the job to a ki process;
	// Pid is that process once it is running.
	Starting *time.Time `json:"starting,omitempty"`
	Pid      int        `json:"pid,omitempty"`
}

// Waiting reports whether the entry is still waiting for a start.
func (e QueueEntry) Waiting() bool { return e.Starting == nil }

// Started reports whether a ki process holds the entry: marked starting and
// either running or still inside the grace for recording its pid.
func (e QueueEntry) Started(now time.Time) bool {
	if e.Starting == nil {
		return false
	}
	if e.Pid != 0 {
		return Alive(e.Pid)
	}
	return now.Sub(*e.Starting) < QueueStartGrace
}

// Result is the entry as `ki delegate ls` shows it.
func (e QueueEntry) Result() Result {
	q := e.Queued
	detail := e.Reason
	if e.Starting != nil {
		detail = "starting"
	}
	return Result{
		ID: e.ID, Title: e.Title, Role: e.Role, Repo: e.Repo, Issue: e.Issue, Status: JobQueued, Detail: detail,
		Started: e.Queued, Queued: &q, StartAfter: e.StartAfter, ResumedFrom: e.ResumeOf,
	}
}

// WriteQueueEntry writes the entry into its job folder, atomically.
func WriteQueueEntry(jobsDir string, e QueueEntry) error {
	if !ValidJobID(e.ID) {
		return fmt.Errorf("queue: %q is not a job id", e.ID)
	}
	return WritePrivate(filepath.Join(jobsDir, e.ID, QueueEntryFile), e)
}

// ReadQueueEntry reads one job's queue entry.
func ReadQueueEntry(jobsDir, id string) (QueueEntry, error) {
	var e QueueEntry
	if !ValidJobID(id) {
		return e, fmt.Errorf("%q is not a job id", id)
	}
	data, err := os.ReadFile(filepath.Join(jobsDir, id, QueueEntryFile)) // #nosec G304 -- id is a checked job id
	if err != nil {
		return e, fmt.Errorf("reading queued job %s: %w", id, err)
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("queued job %s: %w", id, err)
	}
	return e, nil
}

// RemoveQueueEntry drops a job from the queue, and its folder when nothing else is in it.
func RemoveQueueEntry(jobsDir, id string) error {
	if !ValidJobID(id) {
		return fmt.Errorf("%q is not a job id", id)
	}
	if err := os.Remove(filepath.Join(jobsDir, id, QueueEntryFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing queued job %s: %w", id, err)
	}
	_ = os.Remove(filepath.Join(jobsDir, id)) // only succeeds when empty: a job that ran keeps its files
	return nil
}

// ReadQueue returns every queue entry, oldest first; no jobs folder means none.
func ReadQueue(jobsDir string) ([]QueueEntry, error) {
	dirs, err := os.ReadDir(jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", jobsDir, err)
	}
	var out []QueueEntry
	for _, d := range dirs {
		if !d.IsDir() || !ValidJobID(d.Name()) {
			continue
		}
		e, err := ReadQueueEntry(jobsDir, d.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Queued.Before(out[j].Queued) })
	return out, nil
}

// LockQueue takes the queue's flock, so a run of the queue, a cancel and a
// queued job recording its start never interleave.
func LockQueue(ctx context.Context, jobsDir string, wait time.Duration) (unlock func(), err error) {
	return lockFile(ctx, filepath.Join(jobsDir, QueueLockFile), wait, "another ki to change the queue")
}
