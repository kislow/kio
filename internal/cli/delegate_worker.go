package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// killedWorkerGrace is how long ki waits for the output of a killed worker's children.
var killedWorkerGrace = 3 * time.Second

// progressOut is where progress lines go: stderr, or nowhere under --quiet.
func (a *App) progressOut(quiet bool) io.Writer {
	if quiet {
		return nil
	}
	return a.Stderr
}

// workerRun is one worker process: what to start, where its output goes and
// what ki does once it is alive.
type workerRun struct {
	bin     string
	launch  ki.Launch
	dir     string
	logPath string
	timeout time.Duration
	// stall is how long a streaming worker may print nothing before it is killed.
	stall time.Duration
	// events follows the worker's stdout while it runs; nil for an agent that prints once at the end.
	events *eventStream
	// started is told the worker's pid once it exists.
	started func(pid int) error
}

// workerHang is why ki killed a worker that was still running.
func workerHang(ctx context.Context, timeout time.Duration) error {
	if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
		return cause //nolint:wrapcheck // built in runWorker: errStalled and the silence
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: killed after %s", errTimeout, timeout)
	}
	return nil
}

// runWorker runs the agent with stdin closed, stdout captured and both
// streams appended to the job log as they arrive. It returns the exit code,
// -1 when the process did not run at all.
func (a *App) runWorker(ctx context.Context, w workerRun) ([]byte, int, error) {
	logFile, err := os.OpenFile(w.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- under the jobs folder
	if err != nil {
		return nil, -1, fmt.Errorf("opening %s: %w", w.logPath, err)
	}
	defer logFile.Close()
	timeout := w.timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx, stall := context.WithCancelCause(ctx)
	defer stall(nil)
	cmd := exec.CommandContext(ctx, w.bin, w.launch.Argv[1:]...) // #nosec G204 -- the agent ki was asked to run
	cmd.Dir = w.dir
	// A killed agent's children can hold the stdout pipe open; Wait gives up on them after this.
	cmd.WaitDelay = killedWorkerGrace
	cmd.Env = ki.MergeEnv(a.Environ(), w.launch.Env)
	var stdout bytes.Buffer
	sinks := []io.Writer{&stdout, logFile}
	if w.events != nil {
		sinks = append(sinks, w.events)
	}
	cmd.Stdout = io.MultiWriter(sinks...)
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, -1, fmt.Errorf("starting %s: %w", w.bin, err)
	}
	if err := w.started(cmd.Process.Pid); err != nil {
		cancel()
		_ = cmd.Wait() // the worker was killed by cancel; its exit says nothing
		return nil, -1, fmt.Errorf("recording the running job: %w", err)
	}
	if w.events != nil {
		var wg sync.WaitGroup
		stallCtx, stop := context.WithCancel(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.events.watch(stallCtx, a.Stderr, a.stallAfter(), w.stall, a.resultGrace(), func(quiet time.Duration) {
				stall(fmt.Errorf("%w: no event for %s", errStalled, ki.Clock(quiet)))
			}, func() { stall(errLingered) })
		}()
		defer func() { stop(); wg.Wait() }()
	}
	runErr := cmd.Wait()
	if runErr == nil {
		return stdout.Bytes(), 0, nil
	}
	if errors.Is(context.Cause(ctx), errLingered) {
		// The final result is in: what ki ended was a hung child, not a failure.
		return stdout.Bytes(), 0, nil
	}
	if err := workerHang(ctx, timeout); err != nil {
		return stdout.Bytes(), -1, err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		// Interrupted by the user, not a provider failure: never a LAUNCH_FAILED
		// for the breaker to count, and the caller does not try another provider.
		return stdout.Bytes(), -1, fmt.Errorf("%w: interrupted", errCancelled)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout.Bytes(), exitErr.ExitCode(), runErr
	}
	return stdout.Bytes(), -1, runErr
}
