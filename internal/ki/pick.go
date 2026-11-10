package ki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// A pick reads the ledger, chooses a provider and records the choice. Run
// in parallel, several processes would read the same ledger and choose the
// same provider, so PickAndReserve does all three under one lock.
const (
	// ReservationFile holds the picks whose job is not in the ledger yet,
	// one JSON line each. It sits beside the ledger, which keeps its format.
	ReservationFile = "reservations.jsonl"
	// PickLockFile is the flock taken around reading, choosing and reserving.
	PickLockFile = "pick.lock"
	// PickLockWait is how long a pick waits for another one before giving up.
	PickLockWait = 10 * time.Second

	pickLockPoll = 20 * time.Millisecond
)

// Reservation is a turn taken by a job that has not reached the ledger.
type Reservation struct {
	ID       string    `json:"id"`
	Role     string    `json:"role"`
	Provider string    `json:"provider"`
	Model    string    `json:"model,omitempty"`
	At       time.Time `json:"at"`
}

// PickAndReserve is Candidates made atomic across processes on one machine:
// under an exclusive lock it reads the ledger and the reservations in dir,
// ranks the providers and reserves the first candidate for job id, so the
// next pick sees this turn taken. The lock is held only for that, never
// while an agent runs, and the kernel drops it if the process dies. in.Ledger
// and in.Reserved are filled here; wait bounds the time spent on the lock.
func PickAndReserve(ctx context.Context, dir string, in PickInput, id string, wait time.Duration) (try []Provider, skipped []Skipped, err error) {
	unlock, err := lockPick(ctx, filepath.Join(dir, PickLockFile), wait)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	if in.Ledger, err = ReadLedger(filepath.Join(dir, LedgerFile)); err != nil {
		return nil, nil, fmt.Errorf("reading the job ledger: %w", err)
	}
	if in.Reserved, err = readReservations(filepath.Join(dir, ReservationFile)); err != nil {
		return nil, nil, err
	}
	try, skipped = Candidates(in)
	if len(try) == 0 {
		return nil, skipped, nil
	}
	first := try[0]
	if err := appendReservation(filepath.Join(dir, ReservationFile), Reservation{ID: id, Role: in.Role, Provider: first.Agent, Model: first.Model, At: in.Now}); err != nil {
		return nil, nil, err
	}
	return try, skipped, nil
}

// lockPick takes the exclusive flock on path. Closing the descriptor
// releases it, and so does the process exiting, so a crash never wedges ki.
func lockPick(ctx context.Context, path string, wait time.Duration) (unlock func(), err error) {
	return lockFile(ctx, path, wait, "another ki delegate to pick a provider")
}

// lockFile takes the exclusive flock on path, waiting at most wait for
// holder, the process the timeout message names.
func lockFile(ctx context.Context, path string, wait time.Duration, holder string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- under the ki config dir
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) // #nosec G115 -- fd fits an int
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("timed out after %s waiting for %s (lock %s); "+
				"the lock is released when that process exits, so check `ki delegate ls` for a stuck one", wait, holder, path)
		case <-time.After(pickLockPoll):
		}
	}
}

func appendReservation(path string, r Reservation) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding reservation %s: %w", r.ID, err)
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

// ReadReservations returns the reservations in the jobs folder dir, oldest first.
func ReadReservations(dir string) ([]Reservation, error) {
	return readReservations(filepath.Join(dir, ReservationFile))
}

// readReservations returns the reservations oldest first; no file means none.
// A line that does not parse is skipped: a torn reservation only costs a turn.
func readReservations(path string) ([]Reservation, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- under the ki config dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []Reservation
	for _, line := range bytes.Split(data, []byte("\n")) {
		var r Reservation
		if json.Unmarshal(line, &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	return out, nil
}
