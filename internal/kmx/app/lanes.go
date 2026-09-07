package app

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// Lanes are how `kmx up` overlaps the parts of a bring-up that do not
// depend on each other.
//
// In practice that is the two agents, once the controller is up: their
// images are already on the node, so running them together is real time
// saved. The bring-up's earlier steps stay a chain even where they look
// independent — Ollama's model pull and kagent's five pods contend for
// the same bandwidth, and upOverlapped carries the measurement that
// settled it.
//
// What a lane must NOT do is make the output unreadable. Each lane writes
// through a prefixWriter, so a developer watching `kmx up` sees live progress
// attributed to the lane it came from ("[kagent] pod/... condition met")
// rather than two commands' output shuffled together.

// lane is one branch of an overlapped step: a label for its output, and the
// work to run against a lane-local App.
type lane struct {
	name string
	fn   func(*App) error
}

// prefixWriter tags every line it forwards with a lane's name, under a lock
// shared by all lanes writing to the same stream, so a line from one lane
// never lands inside a line from another.
//
// Both '\n' and '\r' end a line: `ollama pull` redraws its progress with
// carriage returns, and buffering all of that into one enormous line would
// hide the download while it is happening. A partial line left over when the
// lane ends is flushed with a newline of its own.
type prefixWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    []byte
	// err latches the FIRST failure writing to the underlying stream.
	// Losing a lane's output must not read as a lane that ran quietly:
	// the failure is reported to the command writing through this
	// writer (os/exec surfaces it from Wait) and again by runLanes, so
	// a bring-up whose output went nowhere cannot report success.
	err error
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	for i, b := range p {
		if b == '\n' || b == '\r' {
			if err := w.emit(); err != nil {
				// io.Writer's contract: report how much was consumed
				// before the failure, and the failure.
				return i, err
			}
			continue
		}
		w.buf = append(w.buf, b)
	}
	return len(p), nil
}

// emit writes the buffered line, prefix and all. An empty line is still a
// line — dropping it would silently reflow output that was deliberately
// spaced — but a line that is only a line ending prints as the prefix alone,
// which is noise, so those are dropped.
//
// The write error is latched and returned rather than discarded: fmt.Fprintf
// to a closed pipe or a full disk fails, and a lane that swallowed that would
// finish "successfully" having printed nothing.
func (w *prefixWriter) emit() error {
	if len(w.buf) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w.out, "%s%s\n", w.prefix, w.buf)
	w.buf = w.buf[:0]
	if err != nil && w.err == nil {
		w.err = err
	}
	return err
}

// flush emits whatever partial line is left and reports the latched failure,
// if any — including one from an earlier Write.
func (w *prefixWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.emit(); err != nil {
		return err
	}
	return w.err
}

// runLanes runs every lane at once and returns after all of them have
// finished, joining whatever they failed with.
//
// Every lane is run, even after one fails: they are already in flight
// against the same cluster, and abandoning a half-finished helm install or
// model pull would leave the cluster in a state the next command cannot
// explain. The joined error names every lane that failed.
//
// A single lane runs in place, unprefixed — that is the ordinary path for
// `kmx up --step <one>` and keeps its output byte-identical to the serial
// version.
func (a *App) runLanes(lanes []lane) error {
	if len(lanes) == 0 {
		return nil
	}
	if len(lanes) == 1 {
		return lanes[0].fn(a)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(lanes))
	writers := make([]*prefixWriter, len(lanes))

	for i, l := range lanes {
		w := &prefixWriter{mu: &mu, out: a.Err, prefix: "[" + l.name + "] "}
		writers[i] = w

		// A lane-local App: same configuration, same cluster, its own
		// streams. The guard has already run for the whole invocation
		// (Up guards before any lane starts), and a lane must never
		// prompt — nothing is reading its stdin.
		b := *a
		r := *a.Run
		r.Stdout, r.Stderr = w, w
		b.Run = &r
		b.Out, b.Err = w, w
		b.guarded = true

		wg.Add(1)
		go func(i int, l lane, b App, w *prefixWriter) {
			defer wg.Done()
			started := b.timeNow()
			fmt.Fprintln(b.Err, "START")
			err := l.fn(&b)
			if flushErr := w.flush(); flushErr != nil {
				err = errors.Join(err, fmt.Errorf("output could not be written: %w", flushErr))
			}
			if err != nil {
				fmt.Fprintf(b.Err, "FAILED (%s)\n", formatElapsed(b.timeNow().Sub(started)))
				errs[i] = fmt.Errorf("%s: %w", l.name, err)
			} else {
				fmt.Fprintf(b.Err, "DONE (%s)\n", formatElapsed(b.timeNow().Sub(started)))
			}
		}(i, l, b, w)
	}
	wg.Wait()
	for i, w := range writers {
		if err := w.flush(); err != nil {
			// A lane can only be called successful if what it said was
			// actually written: an unreported bring-up is not a quiet one.
			errs[i] = errors.Join(errs[i], fmt.Errorf("%s: output could not be written: %w", lanes[i].name, err))
		}
	}
	return errors.Join(errs...)
}
