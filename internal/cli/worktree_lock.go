package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	worktreeLockFile  = "ki-worktree.lock"
	worktreeLockPoll  = 50 * time.Millisecond
	worktreeLockLimit = 2 * time.Minute
)

// lockWorktrees serialises job worktree creation in one repository: git writes
// .git/worktrees/<name>/ in steps, so a sibling's add can read a half-written
// entry. The flock lives under the common git dir, shared by every worktree of
// the repo, and the kernel drops it if ki dies, so a crash never leaves it held.
func lockWorktrees(ctx context.Context, root string) (unlock func(), err error) {
	common := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if common == "" {
		return nil, fail("%s: cannot find the git directory", root)
	}
	f, err := os.OpenFile(filepath.Join(filepath.Clean(common), worktreeLockFile), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- path under the repo's own git dir
	if err != nil {
		return nil, fmt.Errorf("opening worktree lock: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, worktreeLockLimit)
	defer cancel()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) // #nosec G115 -- fd fits an int
		if err == nil {
			return func() { _ = f.Close() }, nil // closing the descriptor releases the lock
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("locking worktree creation: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fail("timed out waiting for another delegation to finish creating its worktree")
		case <-time.After(worktreeLockPoll):
		}
	}
}
