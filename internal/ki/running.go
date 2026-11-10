package ki

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// JobLost is shown for a job whose result.json still says RUNNING but whose
// worker is gone: ki was killed before it could record the end.
const JobLost = "LOST"

// ReadResult reads one job's result.json.
func ReadResult(jobsDir, id string) (Result, error) {
	var r Result
	data, err := os.ReadFile(filepath.Join(jobsDir, id, "result.json")) // #nosec G304 -- id is checked by the caller with ValidJobID
	if err != nil {
		return r, fmt.Errorf("reading job %s: %w", id, err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("job %s result.json: %w", id, err)
	}
	return r, nil
}

// ReadSettled reads one job's result.json, a RUNNING one whose worker is gone
// as LOST only when ki's own process is gone too, and unless the record
// changed while the pids were checked.
func ReadSettled(jobsDir, id string) (Result, error) { return readSettled(jobsDir, id, Alive) }

// readSettled calls a job LOST only when no process of it is left. The worker
// exits before ki parses its output and writes the end, so a gone worker with
// ki still running is a job being finished; a record written between the
// first read and the pid checks is picked up by one more read.
func readSettled(jobsDir, id string, alive func(pid int) bool) (Result, error) {
	r, err := ReadResult(jobsDir, id)
	if err != nil || r.Status != JobRunning || alive(r.Pid) || (r.KiPid > 0 && alive(r.KiPid)) {
		return r, err
	}
	again, err := ReadResult(jobsDir, id)
	if err != nil || again.Status != JobRunning {
		return again, err
	}
	again.Status, again.Detail = JobLost, fmt.Sprintf("worker pid %d is gone", again.Pid)
	return again, nil
}

// ReadRunning returns the jobs whose result.json says RUNNING, newest first,
// with a dead worker's job marked LOST. The ledger is append-on-finish, so
// these exist only as job folders.
func ReadRunning(jobsDir string) ([]Result, error) {
	entries, err := os.ReadDir(jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", jobsDir, err)
	}
	var out []Result
	for _, e := range entries {
		if !e.IsDir() || !ValidJobID(e.Name()) {
			continue
		}
		r, err := readSettled(jobsDir, e.Name(), Alive)
		if err != nil || (r.Status != JobRunning && r.Status != JobLost) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Alive reports whether a process with this pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
