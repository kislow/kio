package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const (
	// stallWarning is how long a streaming worker may stay silent before ki warns.
	stallWarning = 5 * time.Minute
	// heartbeatEvery bounds how often events rewrite result.json.
	heartbeatEvery = time.Second
	// tailLines is how many progress lines a stalled job keeps.
	tailLines = 10
	// resultGrace is how long a worker may keep running after its final result
	// before ki ends it: the answer is complete, what is left is a hung child.
	resultGrace = time.Minute
)

func (a *App) resultGrace() time.Duration {
	if a.ResultGrace > 0 {
		return a.ResultGrace
	}
	return resultGrace
}

func (a *App) stallAfter() time.Duration {
	if a.StallAfter > 0 {
		return a.StallAfter
	}
	return stallWarning
}

// progressLine is how a job's event shows on screen: `ki: 02:14 claude  edit docs/x.md`.
func progressLine(elapsed string, agent, text string) string {
	return fmt.Sprintf("ki: %s %s  %s\n", elapsed, agent, text)
}

// eventStream reads a worker's stdout line by line: it prints one progress
// line per event to out (nil when --quiet), keeps the last few, and
// remembers when the last arrived.
type eventStream struct {
	agent string
	start time.Time
	out   io.Writer
	// now is the clock every silence is measured on.
	now func() time.Time
	// onEvent is told the time of each write; nil for none.
	onEvent func(time.Time)

	lock sync.Mutex
	buf  []byte
	last time.Time
	tail []string
	// resultAt is when the worker's final result event arrived, zero before it.
	resultAt time.Time
}

func newEventStream(agent string, start time.Time, out io.Writer, now func() time.Time) *eventStream {
	if now == nil {
		now = time.Now
	}
	return &eventStream{agent: agent, start: start, out: out, now: now, last: start}
}

func (s *eventStream) Write(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.last = s.now()
	if s.onEvent != nil {
		s.onEvent(s.last)
	}
	s.buf = append(s.buf, b...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		s.print(s.buf[:i])
		s.buf = s.buf[i+1:]
	}
}

// noteResult remembers the first result event, the one that ends a Claude Code
// or Cursor stream.
func (s *eventStream) noteResult(line []byte) {
	if !s.resultAt.IsZero() || !bytes.Contains(line, []byte(`"result"`)) {
		return
	}
	var ev struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &ev) == nil && ev.Type == "result" {
		s.resultAt = s.now()
	}
}

// lingering is how long the worker has run on since its final result, 0 before one.
func (s *eventStream) lingering() time.Duration {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.resultAt.IsZero() {
		return 0
	}
	return s.now().Sub(s.resultAt)
}

func (s *eventStream) print(line []byte) {
	s.noteResult(line)
	for _, text := range ki.Progress(line) {
		l := progressLine(ki.Clock(s.now().Sub(s.start)), s.agent, text)
		if s.tail = append(s.tail, l); len(s.tail) > tailLines {
			s.tail = s.tail[1:]
		}
		if s.out != nil {
			fmt.Fprint(s.out, l)
		}
	}
}

// lastEvent is when the worker last wrote.
func (s *eventStream) lastEvent() time.Time {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.last
}

// lastLines are the latest progress lines, oldest first, without the newline.
func (s *eventStream) lastLines() []string {
	s.lock.Lock()
	defer s.lock.Unlock()
	out := make([]string, len(s.tail))
	for i, l := range s.tail {
		out[i] = l[:len(l)-1]
	}
	return out
}

func (s *eventStream) silence() time.Duration { return s.now().Sub(s.lastEvent()) }

// watch warns on out each time the worker has been silent for another
// warnEvery, calls onStall once it has been silent for limit and onLinger once
// it has run on for grace after its final result, until ctx ends.
func (s *eventStream) watch(ctx context.Context, out io.Writer, warnEvery, limit, grace time.Duration, onStall func(quiet time.Duration), onLinger func()) {
	tick := time.NewTicker(max(min(warnEvery, limit, grace)/10, time.Millisecond))
	defer tick.Stop()
	warned := s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if l := s.lingering(); l > 0 && l >= grace {
				onLinger()
				return
			}
			quiet := s.silence()
			if quiet >= limit {
				onStall(quiet)
				return
			}
			if quiet >= warnEvery && s.now().Sub(warned) >= warnEvery {
				warned = s.now()
				s.lock.Lock() // the progress lines go to the same writer
				fmt.Fprintf(out, "ki: %s %s  no event for %s; still running, ended as STALLED at %s\n",
					ki.Clock(s.now().Sub(s.start)), s.agent, ki.Clock(quiet), ki.Clock(limit))
				s.lock.Unlock()
			}
		}
	}
}

// heartbeat keeps the job's result.json current while the worker runs: the
// RUNNING record with its pid, and the time of the latest event, so
// `ki delegate ls` can show how long a worker has been silent.
type heartbeat struct {
	path string
	// streams is whether the agent prints events at all; only then is there a last event to record.
	streams bool

	lock  sync.Mutex
	rec   ki.Result
	live  bool
	wrote time.Time
}

func newHeartbeat(path string, rec ki.Result, streams bool, stall time.Duration) *heartbeat {
	rec.Status = ki.JobRunning
	if streams {
		rec.StallS = stall.Seconds()
	}
	return &heartbeat{path: path, rec: rec, streams: streams}
}

// begin records the worker as running; its start counts as the first event.
func (h *heartbeat) begin(pid int) error {
	h.lock.Lock()
	defer h.lock.Unlock()
	h.rec.Pid, h.rec.KiPid, h.live = pid, os.Getpid(), true
	if h.streams {
		at := h.rec.Started
		h.rec.LastEventAt = &at
	}
	return ki.WritePrivate(h.path, h.rec) //nolint:wrapcheck // runWorker wraps it
}

// beat records an event, at most once per heartbeatEvery.
func (h *heartbeat) beat(at time.Time) {
	h.lock.Lock()
	defer h.lock.Unlock()
	if !h.live || at.Sub(h.wrote) < heartbeatEvery {
		return
	}
	h.wrote, h.rec.LastEventAt = at, &at
	// A missed beat only makes the next one carry the time; the final result.json holds the truth.
	_ = ki.WritePrivate(h.path, h.rec)
}
