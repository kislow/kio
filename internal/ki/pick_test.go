package ki

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func roundRobin(weights ...int) Role {
	r := Role{Strategy: StrategyRoundRobin}
	for i, w := range weights {
		r.Providers = append(r.Providers, Provider{Agent: string(rune('a' + i)), Weight: w})
	}
	return r
}

func pickInput(r Role) PickInput {
	return PickInput{Role: "execution", R: r, Now: time.Now(), Installed: func(string) bool { return true }}
}

func TestPickAndReserveParallelPicksTakeTurns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		weights []int
		picks   int
		want    map[string]int
	}{
		{"1 and 1, 4 picks", []int{1, 1}, 4, map[string]int{"a": 2, "b": 2}},
		{"1 and 1, 8 picks", []int{1, 1}, 8, map[string]int{"a": 4, "b": 4}},
		{"2 and 1, 6 picks", []int{2, 1}, 6, map[string]int{"a": 4, "b": 2}},
		{"1, 1 and 2, 8 picks", []int{1, 1, 2}, 8, map[string]int{"a": 2, "b": 2, "c": 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var (
				mu   sync.Mutex
				got  = map[string]int{}
				wg   sync.WaitGroup
				fail []error
			)
			for i := 0; i < tt.picks; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					try, _, err := PickAndReserve(context.Background(), dir, pickInput(roundRobin(tt.weights...)), NewJobID(time.Now()), 30*time.Second)
					mu.Lock()
					defer mu.Unlock()
					if err != nil || len(try) == 0 {
						fail = append(fail, err)
						return
					}
					got[try[0].Agent]++
				}()
			}
			wg.Wait()
			if len(fail) > 0 {
				t.Fatalf("picks failed: %v", fail)
			}
			for agent, n := range tt.want {
				if got[agent] != n {
					t.Fatalf("distribution %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestPickAndReserveCountsReservationsOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := pickInput(roundRobin(1, 1))
	first, _, err := PickAndReserve(context.Background(), dir, in, "j-20261106-190137-0001", time.Second)
	if err != nil || first[0].Agent != "a" {
		t.Fatalf("first pick: %v %v", first, err)
	}
	// The job reaches the ledger: the turn must be counted once, by the ledger.
	if err := AppendLedger(filepath.Join(dir, LedgerFile), Result{ID: "j-20261106-190137-0001", Provider: "a", Role: "execution", Status: JobDone, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	second, _, err := PickAndReserve(context.Background(), dir, in, "j-20261106-190137-0002", time.Second)
	if err != nil || second[0].Agent != "b" {
		t.Fatalf("second pick after the ledger holds the first: %v %v", second, err)
	}
	third, _, err := PickAndReserve(context.Background(), dir, in, "j-20261106-190137-0003", time.Second)
	if err != nil || third[0].Agent != "a" {
		t.Fatalf("third pick: %v %v", third, err)
	}
}

func TestPickAndReserveReservationCountsAgainstDailyCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := Role{Strategy: StrategyOrdered, Providers: []Provider{{Agent: "a", Weight: 1, DailyJobs: 1}, {Agent: "b", Weight: 1}}}
	for i, want := range []string{"a", "b"} {
		try, skipped, err := PickAndReserve(context.Background(), dir, pickInput(r), NewJobID(time.Now())+string(rune('0'+i)), time.Second)
		if err != nil || try[0].Agent != want {
			t.Fatalf("pick %d: %v %v %v", i, try, skipped, err)
		}
	}
}

func TestPickAndReserveNothingAvailableReservesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := pickInput(roundRobin(1))
	in.Installed = func(string) bool { return false }
	try, skipped, err := PickAndReserve(context.Background(), dir, in, "j-20261106-190137-0001", time.Second)
	if err != nil || len(try) != 0 || len(skipped) != 1 {
		t.Fatalf("got %v %v %v", try, skipped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ReservationFile)); !os.IsNotExist(err) {
		t.Fatalf("a pick with no candidate left a reservation: %v", err)
	}
}

func TestPickAndReserveLockTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	held, err := os.OpenFile(filepath.Join(dir, PickLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err = PickAndReserve(context.Background(), dir, pickInput(roundRobin(1, 1)), "j-20261106-190137-0001", 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out after 150ms waiting for another ki delegate to pick a provider") {
		t.Fatalf("want a timeout error naming the wait, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the wait was not bounded: %s", time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(dir, ReservationFile)); !os.IsNotExist(err) {
		t.Fatal("a timed-out pick must reserve nothing")
	}
	// A lock holder that exits frees the lock: closing the descriptor is what the kernel does.
	_ = held.Close()
	if try, _, err := PickAndReserve(context.Background(), dir, pickInput(roundRobin(1, 1)), "j-20261106-190137-0002", time.Second); err != nil || len(try) == 0 {
		t.Fatalf("pick after the lock was released: %v %v", try, err)
	}
}

func TestPickAndReserveCancelledContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	held, err := os.OpenFile(filepath.Join(dir, PickLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PickAndReserve(ctx, dir, pickInput(roundRobin(1)), "j-20261106-190137-0001", time.Minute); err == nil {
		t.Fatal("a cancelled context must stop the wait")
	}
}

func TestReadReservationsSkipsTornLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ReservationFile)
	data := `{"id":"j-1","role":"execution","provider":"a","at":"2026-11-06T19:01:37Z"}` + "\n" + `{"id":"j-2","rol` + "\n\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readReservations(path)
	if err != nil || len(got) != 1 || got[0].ID != "j-1" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
