package admin

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"time"
)

// The flow view: one credential's four audit trails merged into a single
// chronological reading.
//
// Nothing here is new evidence. The plane already records every one of these
// rows, and `kmx ledger`, `kmx audit tool`, `kmx audit approval` already print
// them. What was missing was the reading an operator actually wants — "what
// did this thing DO?" — which today means running three commands and
// interleaving them by eye.
//
// It is a TIMELINE, NOT A TRACE, and the distinction is the whole reason this
// file is careful. The four tables share exactly two columns: credential_name
// and created_at. There is no correlation id, so nothing links a model call to
// the tool calls that followed from it. Drawing that link from timestamp
// adjacency would be right for a single sequential agent and confidently wrong
// the moment two turns overlap — and a governance view that is subtly wrong is
// worse than one that is honestly incomplete. So these rows are ordered, and
// the footer says they are only ordered.

// The credential column is not decoration. `kmx flow` with no argument merges
// EVERY credential, and without attribution two agents' events interleave into
// one plausible-looking story about a single actor — the same failure the
// causal-linking note warns about, one level up.
const flowFmt = "%-19s %-12s %-8s %-28s %-10s %6s %s\n"

// flowLimit is per source, matching the other views. Four sources at fifty
// rows each is a generous window for one agent's recent life.
const flowLimit = 50

// inboundFlowLimit is larger because the inbound endpoint filters by HOOK, not
// by credential, so a page of it may be mostly other credentials' events. It
// is a named constant because saturation is judged against the limit a source
// was ACTUALLY asked for — comparing a 200-row page against flowLimit would
// call any page of 50 or more "full" and cut the window short on evidence that
// was never missing.
const inboundFlowLimit = 200

// flowEvent is one thing that happened, flattened out of whichever trail
// recorded it so the four can be sorted together.
type flowEvent struct {
	at      time.Time // parsed for ordering
	raw     string    // as the plane sent it, for printing
	cred    string    // which identity did this; every trail records it
	kind    string    // inbound | model | tool | approval
	what    string
	outcome string
	cents   string
	detail  string
	denied  bool
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

// flowEvents gathers the four trails. Each is fetched through the same admin
// session; a missing or empty trail contributes nothing rather than failing
// the whole reading, because a credential that has never been approved for
// anything is a perfectly ordinary credential.
func (c *Client) flowEvents(credential string) ([]flowEvent, []string, error) {
	cred := url.QueryEscape(credential)
	limit := fmt.Sprintf("&limit=%d", flowLimit)

	ledger, err := c.Get("ledger", "/admin/ledger?credential="+cred+limit)
	if err != nil {
		return nil, nil, err
	}
	tool, err := c.Get("tool-audit", "/admin/tool-audit?credential="+cred+limit)
	if err != nil {
		return nil, nil, err
	}
	approval, err := c.Get("approval-audit", "/admin/approval-audit?credential="+cred+limit)
	if err != nil {
		return nil, nil, err
	}
	// The inbound trail filters by HOOK, not by credential — a hook's
	// credential is config-bound, so the plane never needed the other index.
	// Filtering here keeps the view honest without asking the plane to grow
	// a query for one caller.
	inbound, err := c.Get("inbound-audit", fmt.Sprintf("/admin/inbound-audit?limit=%d", inboundFlowLimit))
	if err != nil {
		return nil, nil, err
	}

	var events []flowEvent
	var saturated []time.Time
	collect := func(doc map[string]any, kind string, limit int, filter bool) {
		list := rows(doc, "entries")
		for _, r := range list {
			if filter && credential != "" && str(r["credential"]) != credential {
				continue
			}
			events = append(events, flowEventFrom(r, kind))
		}
		// A source that returned a full page is a source we may have cut
		// off. How far back its evidence reaches is a property of the PAGE,
		// not of the rows that survived the credential filter: a full
		// inbound page containing no rows for the selected credential still
		// means older inbound rows for that credential were never fetched.
		// Measuring the filtered batch would find nothing to bound, and the
		// window would silently keep older events from the other trails.
		if len(list) >= limit {
			if oldest, ok := oldestRow(list); ok {
				saturated = append(saturated, oldest)
			}
		}
	}
	collect(ledger, "model", flowLimit, false)
	collect(tool, "tool", flowLimit, false)
	collect(approval, "approval", flowLimit, false)
	collect(inbound, "inbound", inboundFlowLimit, true)

	return trimToComplete(events, saturated)
}

// trimToComplete drops the part of the timeline we cannot vouch for.
//
// Each trail is fetched with its own limit, so they do not reach equally far
// back. If the tool trail is saturated at 09:00 and the ledger reaches to
// 07:00, then everything before 09:00 shows model calls with the tool calls
// missing — a picture that reads like a well-behaved agent precisely where the
// evidence is thinnest. The window therefore starts at the latest point every
// saturated source still covers, and the caller is told the window was cut.
func trimToComplete(events []flowEvent, saturated []time.Time) ([]flowEvent, []string, error) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	if len(saturated) == 0 {
		return events, nil, nil
	}
	watermark := saturated[0]
	for _, t := range saturated[1:] {
		if t.After(watermark) {
			watermark = t
		}
	}
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
		watermark.UTC().Format("2006-01-02T15:04:05"), flowLimit)
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
		e.what = trunc(str(r["model"]), 28)
		e.outcome = str(r["status"])
		e.cents = str(r["cost_cents"])
		e.detail = fmt.Sprintf("%s in / %s out via %s",
			str(r["input_tokens"]), str(r["output_tokens"]), str(r["upstream"]))
		// 'denied' in the ledger means the call was never forwarded.
		e.denied = str(r["status"]) == "denied"

	case "tool":
		name := str(r["tool"])
		if name == "" {
			name = str(r["method"]) // tools/list and friends name no tool
		}
		e.what = trunc(name, 28)
		e.outcome = str(r["decision"])
		e.detail = joinDetail("upstream "+str(r["status"]), call(r))
		if str(r["status"]) == "" || str(r["status"]) == "0" {
			e.detail = call(r)
		}
		e.denied = str(r["decision"]) == "denied"

	case "approval":
		e.what = trunc(str(r["kind"])+":"+str(r["subject"]), 28)
		e.outcome = str(r["action"])
		e.detail = str(r["bounds"])
		if by := str(r["decided_by"]); by != "" {
			e.detail = joinDetail("by "+by, e.detail)
		}
		e.denied = str(r["action"]) == "denied"

	case "inbound":
		what := str(r["hook"])
		if agent := str(r["agent"]); agent != "" {
			what += " -> " + agent
		}
		e.what = trunc(what, 28)
		e.outcome = str(r["decision"])
		e.detail = joinDetail(trunc(str(r["delivery_id"]), 24), str(r["detail"]))
		e.denied = str(r["decision"]) == "denied" || str(r["decision"]) == "failed"
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
	fmt.Fprintf(out, flowFmt, "created (UTC)", "credential", "kind", "what", "outcome", "cents", "detail")
	var cents, denials int64
	for _, e := range events {
		fmt.Fprintf(out, flowFmt, e.raw, dash(e.cred), e.kind, e.what, e.outcome, e.cents, e.detail)
		if e.denied {
			denials++
		}
		cents += centsOf(e.cents)
	}
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

// oldestRow finds how far back a raw page of audit rows reaches, before any
// credential filtering has been applied.
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
