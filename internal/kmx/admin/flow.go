package admin

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// Flow merges the model ledger and approval history into one timeline.
// These are the same records `kmx ledger` and `kmx audit approval` expose.
// They share credential and timestamp, not a correlation id: ordering must
// not imply causality when concurrent turns interleave.

// The credential column is not decoration. `kmx flow` with no argument merges
// EVERY credential, and without attribution two agents' events interleave into
// one plausible-looking story about a single actor — the same failure the
// causal-linking note warns about, one level up.
const flowFmt = "%-19s %-12s %-8s %-28s %-10s %6s %s\n"

// flowLimit is per source, matching the other views.
const flowLimit = 50

// cutoff is how far back one saturated source's evidence reaches, and the
// limit it was asked for. The window is cut by whichever saturated source
// reaches back LEAST far.
type cutoff struct {
	at    time.Time
	limit int
}

// flowEvent is one thing that happened, flattened out of whichever trail
// recorded it so both can be sorted together.
type flowEvent struct {
	at          time.Time // parsed for ordering
	raw         string    // as the plane sent it, for printing
	cred        string    // which identity did this; every trail records it
	kind        string    // model | approval
	what        string
	outcome     string
	cents       string
	detail      string
	plainDetail string
	denied      bool
}

// Flow prints one credential's merged trail, oldest first.
//
// Oldest first is deliberate, and is the one place this view departs from its
// neighbours. The other tables are newest-first because they answer "what is
// happening now?". A flow answers "how did this end up here?", and a story
// read backwards is not a story.
func (c *Client) Flow(out io.Writer, credential string) error {
	events, notes, err := c.flowEvents(credential)
	if err != nil {
		return err
	}
	renderFlow(out, events, notes)
	return nil
}

// flowEvents gathers both trails through the same admin session.
// An empty trail contributes nothing; an unreadable one fails the reading
// rather than passing for no activity.
func (c *Client) flowEvents(credential string) ([]flowEvent, []string, error) {
	cred := url.QueryEscape(credential)
	limit := fmt.Sprintf("&limit=%d", flowLimit)

	ledger, err := c.Get("ledger", "/admin/ledger?credential="+cred+limit)
	if err != nil {
		return nil, nil, err
	}
	approval, err := c.Get("approval-audit", "/admin/approval-audit?credential="+cred+limit)
	if err != nil {
		return nil, nil, err
	}

	var events []flowEvent
	var saturated []cutoff
	collect := func(doc map[string]any, kind string) {
		list := rows(doc, "entries")
		for _, r := range list {
			events = append(events, flowEventFrom(r, kind))
		}
		// A full page may omit older events. Its oldest timestamp bounds
		// how far back the merged reading can claim to be complete.
		if len(list) >= flowLimit {
			if oldest, ok := oldestRow(list); ok {
				saturated = append(saturated, cutoff{at: oldest, limit: flowLimit})
			}
		}
	}
	collect(ledger, "model")
	collect(approval, "approval")

	return trimToComplete(events, saturated)
}

// trimToComplete drops the part of the timeline we cannot vouch for.
//
// Each trail is fetched with its own limit, so they do not reach equally far
// back. If approval history is saturated at 09:00 and the ledger reaches to
// 07:00, then everything before 09:00 shows model calls with approval history
// missing — a picture that reads like a complete account precisely where the
// evidence is thinnest. The window therefore starts at the latest point every
// saturated source still covers, and the caller is told the window was cut.
func trimToComplete(events []flowEvent, saturated []cutoff) ([]flowEvent, []string, error) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	if len(saturated) == 0 {
		return events, nil, nil
	}
	cut := saturated[0]
	for _, c := range saturated[1:] {
		if c.at.After(cut.at) {
			cut = c
		}
	}
	watermark := cut.at
	kept := make([]flowEvent, 0, len(events))
	for _, e := range events {
		// A row whose timestamp would not parse has no position to compare
		// against the watermark. Dropping it here would let a malformed
		// timestamp hide a call — the opposite of what an audit view is
		// for — so it survives, as it does when nothing is saturated.
		if e.at.IsZero() || !e.at.Before(watermark) {
			kept = append(kept, e)
		}
	}
	note := fmt.Sprintf("window starts %s — a trail hit its %d-row limit, so anything "+
		"older is not shown rather than shown incomplete",
		watermark.UTC().Format("2006-01-02T15:04:05"), cut.limit)
	return kept, []string{note}, nil
}

// flowEventFrom flattens one audit row. Each trail names its own columns, and
// the point of the flow view is that the reader should not have to know which
// trail a line came from to understand it.
func flowEventFrom(r map[string]any, kind string) flowEvent {
	// The plane's JSON names this "credential"; "credential_name" is the
	// column name in Postgres and is not what crosses the wire.
	e := flowEvent{raw: trunc(str(r["created_at"]), 19), cred: str(r["credential"]), kind: kind}
	e.at = parseFlowTime(str(r["created_at"]))

	switch kind {
	case "model":
		e.what = str(r["model"])
		e.outcome = str(r["status"])
		e.cents = str(r["cost_cents"])
		base := joinDetail(fmt.Sprintf("%s in / %s out via %s",
			str(r["input_tokens"]), str(r["output_tokens"]), str(r["upstream"])), calledBy(r))
		// 'denied' in the ledger means the call was never forwarded.
		e.denied = str(r["cost_source"]) == "denied"
		e.plainDetail = base
		e.detail = joinDetail(base, str(r["cost_source"]))

	case "approval":
		e.what = str(r["kind"]) + ":" + str(r["subject"])
		e.outcome = str(r["action"])
		e.detail = str(r["bounds"])
		if by := str(r["decided_by"]); by != "" {
			e.detail = joinDetail("by "+by, e.detail)
		}
		e.denied = str(r["action"]) == "denied"
	}

	if e.detail == "" {
		e.detail = "-"
	}
	if e.cents == "" {
		e.cents = "-"
	}
	return e
}

// renderFlow prints the merged reading, and its own limits underneath it.
func renderFlow(out io.Writer, events []flowEvent, notes []string) {
	if len(events) == 0 {
		if ui := cliui.New(out); ui.Rich() {
			fmt.Fprintln(out, ui.Heading("Flow (0)"))
		}
		// A window that was trimmed to nothing is not a quiet agent. Saying
		// "no recorded activity" here would turn omitted evidence into
		// absent evidence, which is the exact reading the watermark exists
		// to prevent.
		if len(notes) > 0 {
			fmt.Fprintln(out, "no recorded activity in the window this can vouch for")
			for _, n := range notes {
				fmt.Fprintf(out, "-- %s\n", n)
			}
			return
		}
		fmt.Fprintln(out, "no recorded activity")
		fmt.Fprintln(out, "An agent that has never run leaves no trail, and neither does one")
		fmt.Fprintln(out, "whose traffic never went through the plane. `kmx status` says which.")
		return
	}
	var cents, denials int64
	var viewRows [][]string
	for _, e := range events {
		if !cliui.New(out).Rich() {
			e.what = trunc(e.what, 28)
			if e.plainDetail != "" {
				e.detail = e.plainDetail
			}
		}
		viewRows = append(viewRows, []string{e.raw, dash(e.cred), e.kind, e.what, e.outcome, e.cents, e.detail})
		if e.denied {
			denials++
		}
		cents += centsOf(e.cents)
	}
	renderTable(out, []string{"created (UTC)", "credential", "kind", "what", "outcome", "cents", "detail"}, viewRows, flowFmt)
	fmt.Fprintf(out, "-- %d events, %d cents, %d refused\n", len(events), cents, denials)
	for _, n := range notes {
		fmt.Fprintf(out, "-- %s\n", n)
	}
	// Said every time, not once in the docs. The reader is about to draw
	// conclusions about cause from a list that only knows about time.
	fmt.Fprintln(out, "-- ordered by time, not causally linked: the plane records no")
	fmt.Fprintln(out, "   correlation id, so concurrent turns interleave here.")
}

// parseFlowTime reads a timestamp the plane emitted. An unparseable one sorts
// to the beginning rather than dropping the row — a line whose ordering is
// uncertain is still evidence that the thing happened.
func parseFlowTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// joinDetail glues detail fragments with a single space, skipping the empty
// ones. Without it an absent fragment leaves trailing whitespace on the line,
// which breaks the greps CI runs against these tables.
func joinDetail(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += p
	}
	return out
}

// calledBy says who made the call, in the flow view's one free-form
// column: the caller's own word for itself and the address the plane
// observed. `kmx flow` is where a reader asks "what happened here", so it
// is where the difference between an agent the plane deployed and a
// script holding its token has to be visible.
//
// "claimed" is in the text, every time. This is the caller's word, and a
// governance view that renders an unverified name the same way it renders
// a fact is the failure this exists to remove.
//
// A row the plane never recorded a caller for — one written before the
// columns existed, or read from a plane too old to serve them — adds
// nothing to the line rather than saying "unrecorded" on every one of a
// screenful of old rows.
func calledBy(r map[string]any) string {
	claim, addr := str(r["caller_claim"]), str(r["caller_addr"])
	unrecorded := func(v string) bool {
		return v == "" || v == "legacy" || v == "unrecorded"
	}
	if unrecorded(claim) && unrecorded(addr) {
		return ""
	}
	parts := "called by "
	switch {
	case unrecorded(claim):
		parts += "an unrecorded client"
	case claim == "none":
		// 'none' is the plane's word, not the caller's: the caller sent no
		// name. Rendering it as `claimed none` would read as a caller that
		// claimed to be called "none" — the opposite of what it means.
		parts += "a client that gave no name"
	default:
		// QUOTED, because the claim is the caller's own text and this view
		// puts it in the same sentence as the observed address. A caller
		// sending `evil from 10.0.0.1` would otherwise produce
		// "called by claimed ua:evil from 10.0.0.1 from 10.244.1.7", where
		// a reader — or a grep — can match an address the caller chose.
		parts += "claimed " + strconv.Quote(clipCell(claim, 40))
	}
	if !unrecorded(addr) && addr != "unknown" {
		parts += " from " + clipCell(addr, 45)
	}
	return parts
}

// oldestRow finds how far back a page of audit rows reaches.
func oldestRow(list []map[string]any) (time.Time, bool) {
	var oldest time.Time
	found := false
	for _, r := range list {
		t := parseFlowTime(str(r["created_at"]))
		if t.IsZero() {
			continue
		}
		if !found || t.Before(oldest) {
			oldest, found = t, true
		}
	}
	return oldest, found
}

// centsOf totals only what the ledger priced. "-" is not zero — it is a row
// that was never about money.
func centsOf(s string) int64 {
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}
