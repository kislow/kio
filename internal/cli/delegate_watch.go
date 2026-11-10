package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const watchPoll = 250 * time.Millisecond

// delegateWatch prints a job's log as progress lines, then follows it until
// the job ends and prints its final status. Lines already in the log carry
// no time: only the elapsed time of the moment they are read is known.
func (a *App) delegateWatch(ctx context.Context, args []string) error {
	if len(args) != 1 || !ki.ValidJobID(args[0]) {
		return fail("usage: ki delegate watch <job id> (ids are in ki delegate ls)")
	}
	r, err := ki.ReadResult(a.jobsDir(), args[0])
	if err != nil {
		return fmt.Errorf("watching: %w", err)
	}
	logPath := filepath.Join(a.jobsDir(), r.ID, "output.log")
	f, err := os.Open(logPath) // #nosec G304 -- r.ID passed ValidJobID
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer f.Close()
	rd := bufio.NewReader(f)
	stamp := "--:--"
	var partial []byte
	ended := false
	for {
		line, err := rd.ReadBytes('\n')
		partial = append(partial, line...)
		if err == nil {
			a.printProgress(stamp, r.Provider, partial)
			partial = nil
			continue
		}
		if !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading %s: %w", logPath, err)
		}
		cur, rerr := ki.ReadSettled(a.jobsDir(), r.ID)
		if rerr != nil {
			return fmt.Errorf("watching: %w", rerr)
		}
		if ended {
			fmt.Fprintf(a.Stdout, "%s %s %s: %s\n", cur.ID, cur.Provider, cur.Status, cur.Detail)
			return nil
		}
		if cur.Status != ki.JobRunning {
			// One more pass picks up what the worker wrote between the last read and its end.
			ended = true
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchPoll):
		}
		stamp = ki.Clock(time.Since(r.Started))
	}
}

func (a *App) printProgress(stamp, agent string, line []byte) {
	for _, text := range ki.Progress(line) {
		fmt.Fprint(a.Stdout, progressLine(stamp, agent, text))
	}
}
