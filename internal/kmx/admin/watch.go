package admin

// `kmx watch` — the trail as it happens, rather than after the fact.
//
// Every other view here answers a question about the past: `flow` reads a
// story backwards from a cluster that has already finished doing the thing.
// This one answers "what is happening right now", which is a different job
// with different rules.
//
// It is append-only, not a full-screen dashboard, and that is a decision
// rather than a shortcut. A feed that repaints the terminal loses the
// scrollback — and the scrollback is the evidence. Append-only also survives
// being piped into `grep`, recorded by `script`, or watched next to a demo
// where the interesting line must still be on screen a minute later.
//
// What it is NOT: a correlation view. The plane records no correlation id,
// so two agents working at once interleave here exactly as they do in
// `flow`. This says so once at the top rather than implying causality by
// putting lines next to each other.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// WatchOptions are the knobs the CLI passes down.
type WatchOptions struct {
	// Credential filters the trail. Empty follows every identity.
	Credential string
	// Interval between polls.
	Interval time.Duration
	// Limit stops after this many events. Zero follows until interrupted,
	// which is what a person wants and what a test cannot use.
	Limit int
	// For stops after this long. Zero means no deadline.
	For time.Duration
	// JSON emits one object per line instead of a formatted row.
	JSON bool
	// Replay prints this many already-recorded events before following.
	// Zero starts from now, the way `tail -f` does.
	Replay int
}

// seenWindow bounds how many event keys are remembered. The plane's
// timestamps are second-resolution, so two events in the same second are
// ordinary and a high-water mark alone would drop one of them. Keys are kept
// instead, and the window is bounded so a long watch does not grow without
// limit.
const seenWindow = 2000

// Watch follows the merged trail until interrupted, or until a bound is hit.
//
// The first poll establishes what was already there. It is NOT printed unless
// --replay asked for it: a feed that dumps history on startup buries the
// thing the operator started it to see.
func (c *Client) Watch(out io.Writer, stop <-chan struct{}, opt WatchOptions) error {
	if opt.Interval <= 0 {
		opt.Interval = 2 * time.Second
	}
	ui := cliui.New(out)
	seen := newSeenSet(seenWindow)

	first, _, err := c.flowEvents(opt.Credential)
	if err != nil {
		return err
	}
	for _, e := range first {
		seen.add(watchKey(e))
	}
	if opt.Replay > 0 {
		replay := first
		if len(replay) > opt.Replay {
			replay = replay[len(replay)-opt.Replay:]
		}
		for _, e := range replay {
			emit(out, ui, e, opt.JSON)
		}
	}
	if !opt.JSON {
		header := fmt.Sprintf("watching %s, polling every %s — ordered by time, not causally linked",
			credentialLabel(opt.Credential), opt.Interval)
		if ui.Rich() {
			header = ui.Muted(header)
		}
		fmt.Fprintln(out, header)
	}

	var deadline <-chan time.Time
	if opt.For > 0 {
		timer := time.NewTimer(opt.For)
		defer timer.Stop()
		deadline = timer.C
	}
	ticker := time.NewTicker(opt.Interval)
	defer ticker.Stop()

	// Consecutive read failures are tolerated, then fatal. A watch that
	// silently stopped watching would be the worst outcome available here:
	// an operator reading an empty feed would conclude nothing is happening,
	// when in fact nothing is being read.
	const tolerated = 5
	failures := 0
	emitted := 0

	for {
		select {
		case <-stop:
			return nil
		case <-deadline:
			return nil
		case <-ticker.C:
		}

		events, _, err := c.flowEvents(opt.Credential)
		if err != nil {
			failures++
			if failures > tolerated {
				return fmt.Errorf("the plane stopped answering after %d consecutive attempts, so this "+
					"is no longer a reading of anything: %w", failures, err)
			}
			if !opt.JSON {
				fmt.Fprintln(out, ui.Warning(fmt.Sprintf(
					"-- read %d/%d failed (%v); still trying, and this gap is NOT an absence of activity",
					failures, tolerated, err)))
			}
			continue
		}
		failures = 0

		fresh := make([]flowEvent, 0, len(events))
		for _, e := range events {
			if key := watchKey(e); !seen.has(key) {
				seen.add(key)
				fresh = append(fresh, e)
			}
		}
		sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].at.Before(fresh[j].at) })
		for _, e := range fresh {
			emit(out, ui, e, opt.JSON)
			emitted++
			if opt.Limit > 0 && emitted >= opt.Limit {
				return nil
			}
		}
	}
}

// watchKey identifies one event well enough to notice it twice.
//
// The plane exposes no row id on these endpoints, so the key is the content.
// Timestamp alone is not enough (second resolution), and timestamp plus
// credential is not enough either (a retry looks identical until the outcome
// differs), so the outcome and detail are in it too.
func watchKey(e flowEvent) string {
	return strings.Join([]string{
		e.at.UTC().Format(time.RFC3339), e.kind, e.cred, e.what, e.outcome, e.cents, e.detail,
	}, "\x1f")
}

func credentialLabel(credential string) string {
	if strings.TrimSpace(credential) == "" {
		return "every credential"
	}
	return credential
}

// emit writes one event. The denial is the line an operator is watching for,
// so it is the one that is styled — everything else stays quiet.
func emit(out io.Writer, ui cliui.Output, e flowEvent, asJSON bool) {
	if asJSON {
		fmt.Fprintf(out,
			`{"at":%q,"credential":%q,"kind":%q,"what":%q,"outcome":%q,"cents":%q,"denied":%t,"detail":%q}`+"\n",
			e.at.UTC().Format(time.RFC3339), e.cred, e.kind, e.what, e.outcome, e.cents, e.denied, plainOrDetail(e))
		return
	}
	stamp := e.at.UTC().Format("15:04:05")
	outcome := e.outcome
	if e.denied {
		outcome = strings.ToUpper(outcome)
	}
	line := fmt.Sprintf("%s  %-8s %-14s %-22s %-9s %s",
		stamp, e.kind, trunc(dash(e.cred), 14), trunc(e.what, 22), outcome, plainOrDetail(e))
	switch {
	case !ui.Rich():
	case e.denied:
		line = ui.Failure(line)
	default:
		line = ui.Muted(line)
	}
	fmt.Fprintln(out, line)
}

// plainOrDetail prefers the unstyled detail, because this view prints one
// line per event and the rich variant is built for a table cell.
func plainOrDetail(e flowEvent) string {
	if e.plainDetail != "" {
		return e.plainDetail
	}
	return e.detail
}

// seenSet is a bounded set that remembers insertion order, so the oldest
// keys fall out once the window is full.
type seenSet struct {
	keys  map[string]struct{}
	order []string
	limit int
}

func newSeenSet(limit int) *seenSet {
	return &seenSet{keys: make(map[string]struct{}, limit), limit: limit}
}

func (s *seenSet) has(key string) bool {
	_, ok := s.keys[key]
	return ok
}

func (s *seenSet) add(key string) {
	if _, ok := s.keys[key]; ok {
		return
	}
	s.keys[key] = struct{}{}
	s.order = append(s.order, key)
	if len(s.order) > s.limit {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.keys, drop)
	}
}
