package cli

import (
	"context"
	"testing"
	"time"
)

func TestLockWorktreesSerialisesPerRepo(t *testing.T) {
	d := newDelegateEnv(t)
	ctx := context.Background()
	first, err := lockWorktrees(ctx, d.repo)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func())
	go func() {
		unlock, err := lockWorktrees(ctx, d.repo)
		if err != nil {
			t.Error(err)
			unlock = func() {}
		}
		acquired <- unlock
	}()
	select {
	case <-acquired:
		t.Fatal("second lock taken while the first is held")
	case <-time.After(4 * worktreeLockPoll):
	}
	first()
	select {
	case unlock := <-acquired:
		unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("second lock not taken after release")
	}
}

func TestLockWorktreesOutsideRepo(t *testing.T) {
	if _, err := lockWorktrees(context.Background(), t.TempDir()); !isUserError(err) {
		t.Fatalf("want a user error, got %v", err)
	}
}
