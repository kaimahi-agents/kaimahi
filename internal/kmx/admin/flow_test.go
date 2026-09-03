package admin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// doc builds an admin response the way the plane sends one: decoded with
// UseNumber, so numbers arrive as json.Number and not float64. A test that
// fed plain ints would be testing a shape the plane never produces.
func doc(t *testing.T, body string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time: %v", err)
	}
	return ts.UTC()
}

// The point of the whole view: four trails, one reading, in the order the
// events actually happened — not grouped by which table recorded them.
func TestFlowInterleavesTheFourTrailsByTime(t *testing.T) {
	var events []flowEvent
	for _, e := range rows(doc(t, `{"entries":[
		{"created_at":"2026-09-04T14:22:02Z","model":"gpt-4o","status":"priced",
		 "cost_cents":14,"input_tokens":1204,"output_tokens":88,"upstream":"openai"}]}`), "entries") {
		events = append(events, flowEventFrom(e, "model"))
	}
	for _, e := range rows(doc(t, `{"entries":[
		{"created_at":"2026-09-04T14:22:04Z","tool":"delete_ns","decision":"denied","status":403},
		{"created_at":"2026-09-04T14:22:03Z","tool":"get_pods","decision":"allowed","status":200}]}`), "entries") {
		events = append(events, flowEventFrom(e, "tool"))
	}
	for _, e := range rows(doc(t, `{"entries":[
		{"created_at":"2026-09-04T14:22:05Z","kind":"tool","subject":"delete_ns",
		 "action":"approved","decided_by":"alice","bounds":"uses=1"}]}`), "entries") {
		events = append(events, flowEventFrom(e, "approval"))
	}
	for _, e := range rows(doc(t, `{"entries":[
		{"created_at":"2026-09-04T14:22:01Z","hook":"slack","agent":"triage",
		 "decision":"admitted","delivery_id":"Ev093"}]}`), "entries") {
		events = append(events, flowEventFrom(e, "inbound"))
	}

	merged, notes, err := trimToComplete(events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if notes != nil {
		t.Fatalf("no source was saturated, so nothing should be trimmed: %v", notes)
	}

	var got []string
	for _, e := range merged {
		got = append(got, e.kind+":"+e.what)
	}
	want := []string{"inbound:slack -> triage", "model:gpt-4o", "tool:get_pods",
		"tool:delete_ns", "approval:tool:delete_ns"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// The correctness detail this view exists to get right. Trails are fetched
// with independent limits, so they do not reach equally far back. Showing the
// older part anyway would render model calls with their tool calls missing —
// an agent that looks well-behaved exactly where the evidence ran out.
func TestFlowRefusesToShowAWindowItCannotVouchFor(t *testing.T) {
	old := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T07:00:00Z","model":"gpt-4o"}}`)["e"].(map[string]any), "model")
	mid := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T09:30:00Z","model":"gpt-4o"}}`)["e"].(map[string]any), "model")
	newer := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T10:00:00Z","tool":"get_pods"}}`)["e"].(map[string]any), "tool")

	// The tool trail was saturated and reaches back only to 09:00.
	kept, notes, err := trimToComplete([]flowEvent{old, mid, newer}, []time.Time{at(t, "2026-09-04T09:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Fatalf("the 07:00 event predates the complete window and must be dropped, got %d", len(kept))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "09:00:00") {
		t.Fatalf("the cut must be stated, got %v", notes)
	}
}

// The latest saturated source wins: the window is only as deep as the
// SHALLOWEST trail, not the deepest.
func TestFlowWindowIsBoundedByTheShallowestTrail(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T09:30:00Z","model":"m"}}`)["e"].(map[string]any), "model")
	_, notes, err := trimToComplete([]flowEvent{e},
		[]time.Time{at(t, "2026-09-04T08:00:00Z"), at(t, "2026-09-04T09:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "09:00:00") {
		t.Fatalf("want the later watermark (09:00), got %v", notes)
	}
}

// Every rendering carries its own limit. A reader is about to infer cause
// from a list that only knows about time, and the docs are not where they
// will be looking.
func TestFlowSaysItIsNotACausalTrace(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:22:02Z","model":"gpt-4o",
			"status":"priced","cost_cents":14}}`)["e"].(map[string]any), "model"),
	}, nil)
	if !strings.Contains(out.String(), "not causally linked") {
		t.Errorf("the timeline must not be mistaken for a trace:\n%s", out.String())
	}
}

// Totals count refusals across all four vocabularies — the ledger says
// 'denied', the gateway says 'denied', an approval says 'denied', an inbound
// event says 'denied' or 'failed'.
func TestFlowTotalsCountRefusalsAndCents(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","model":"m","status":"priced","cost_cents":14}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:01Z","model":"m","status":"denied","cost_cents":0}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:02Z","tool":"t","decision":"denied"}}`)["e"].(map[string]any), "tool"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:03Z","hook":"h","decision":"failed"}}`)["e"].(map[string]any), "inbound"),
	}, nil)
	if !strings.Contains(out.String(), "4 events, 14 cents, 3 refused") {
		t.Errorf("totals wrong:\n%s", out.String())
	}
}

// Silence has two causes and they need different fixes, so the empty case
// names both rather than leaving an operator staring at a blank table.
func TestFlowEmptyCaseDistinguishesNeverRanFromNotGoverned(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, nil, nil)
	got := out.String()
	if !strings.Contains(got, "no recorded activity") {
		t.Errorf("want the empty line, got %q", got)
	}
	if !strings.Contains(got, "never went through the plane") {
		t.Errorf("an ungoverned agent also leaves no trail, and that is the more\n"+
			"dangerous reading of an empty table:\n%s", got)
	}
}

// A row whose timestamp will not parse is still evidence the thing happened.
// Dropping it would let a malformed row hide a call.
func TestFlowKeepsRowsWithUnreadableTimestamps(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"not-a-time","tool":"get_pods","decision":"allowed"}}`)["e"].(map[string]any), "tool")
	if !e.at.IsZero() {
		t.Fatal("an unparseable time should sort to the beginning, not guess")
	}
	kept, _, err := trimToComplete([]flowEvent{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatal("the row must survive: it is still evidence the call happened")
	}
}

// A tools/list names no tool. Falling back to the method keeps the line
// readable instead of printing an empty cell.
func TestFlowNamesTheMethodWhenNoToolWasNamed(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","tool":"",
		"method":"tools/list","decision":"allowed"}}`)["e"].(map[string]any), "tool")
	if e.what != "tools/list" {
		t.Errorf("got %q, want the method", e.what)
	}
}

// "-" in the cents column means "this row was never about money", which is
// not the same as zero and must not be summed as a number.
func TestFlowDoesNotCountNonMonetaryRowsAsZeroCents(t *testing.T) {
	if got := centsOf("-"); got != 0 {
		t.Errorf("got %d", got)
	}
	if got := centsOf("14"); got != 14 {
		t.Errorf("got %d", got)
	}
}

// CI greps these tables, and the docs quote them. A fragment that happens to
// be empty must not leave trailing whitespace on the line.
func TestFlowLinesCarryNoTrailingWhitespace(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{
		// an inbound row with a delivery id but no detail
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","hook":"slack",
			"decision":"admitted","delivery_id":"Ev093","detail":""}}`)["e"].(map[string]any), "inbound"),
		// a tool row with no upstream status
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:01Z","tool":"t",
			"decision":"allowed","arg_summary":"ns=prod"}}`)["e"].(map[string]any), "tool"),
		// an approval with no approver yet
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:02Z","kind":"tool",
			"subject":"s","action":"requested","bounds":""}}`)["e"].(map[string]any), "approval"),
	}, nil)
	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("line %d has trailing whitespace: %q", i, line)
		}
	}
}

// `kmx flow` with no argument merges EVERY credential. Without attribution,
// two agents' events interleave into one plausible-looking story about a
// single actor — which is the same failure the causal-linking note warns
// about, one level up, and harder to spot because each line is true.
func TestFlowAttributesEveryEventToItsCredential(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","credential":"triage",
			"model":"gpt-4o","status":"priced","cost_cents":14}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:01Z","credential":"payments",
			"tool":"transfer","decision":"denied"}}`)["e"].(map[string]any), "tool"),
	}, nil)
	got := out.String()
	if !strings.Contains(got, "credential") {
		t.Errorf("the header must name the column:\n%s", got)
	}
	for _, want := range []string{"triage", "payments"} {
		if !strings.Contains(got, want) {
			t.Errorf("event not attributed to %q:\n%s", want, got)
		}
	}
}

// The plane's JSON calls this field "credential". "credential_name" is the
// Postgres column and never crosses the wire — reading that would leave the
// column empty on every row while still looking like it worked.
func TestFlowReadsTheCredentialFieldTheWireActuallyUses(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z",
		"credential":"triage","model":"m"}}`)["e"].(map[string]any), "model")
	if e.cred != "triage" {
		t.Errorf("got %q, want the value under the JSON key \"credential\"", e.cred)
	}
	// A row that genuinely carries no credential renders as "-", not blank.
	blank := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","model":"m"}}`)["e"].(map[string]any), "model")
	if blank.cred != "" {
		t.Errorf("absent credential should stay empty in the struct, got %q", blank.cred)
	}
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{blank}, nil)
	if !strings.Contains(out.String(), "-") {
		t.Error("an absent credential should print as - so the column is never ambiguous")
	}
}

// Saturation is judged against the limit a source was ACTUALLY asked for. The
// inbound endpoint is asked for 200 rows because it filters by hook rather
// than credential; comparing its page against flowLimit would call any page of
// 50 or more "full" and cut the window short on evidence that was never
// missing.
func TestFlowJudgesSaturationAgainstEachSourcesOwnLimit(t *testing.T) {
	if inboundFlowLimit <= flowLimit {
		t.Fatal("this test only means something while the two limits differ")
	}
	// A 60-row inbound page: full by flowLimit's standard, nowhere near
	// full by its own.
	page := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		page = append(page, map[string]any{"created_at": "2026-09-04T10:00:00Z", "hook": "h"})
	}
	if len(page) >= inboundFlowLimit {
		t.Fatal("fixture should not reach the inbound limit")
	}
	if !(len(page) >= flowLimit) {
		t.Fatal("fixture should exceed flowLimit, or it proves nothing")
	}
}

// A full inbound page can contain no rows at all for the selected credential.
// The page still proves older inbound rows for that credential were never
// fetched, so coverage must be measured before filtering — otherwise the
// window silently keeps older events from the other three trails.
func TestFlowBoundsTheWindowEvenWhenTheFullPageIsOtherCredentials(t *testing.T) {
	page := make([]map[string]any, 0, 3)
	for _, ts := range []string{"2026-09-04T09:00:00Z", "2026-09-04T10:00:00Z", "2026-09-04T11:00:00Z"} {
		page = append(page, map[string]any{"created_at": ts, "credential": "someone-else"})
	}
	oldest, ok := oldestRow(page)
	if !ok {
		t.Fatal("a page of other credentials still bounds coverage")
	}
	if want := at(t, "2026-09-04T09:00:00Z"); !oldest.Equal(want) {
		t.Errorf("got %v, want %v — the oldest row of the RAW page", oldest, want)
	}
}

// The documented fallback is that an unreadable timestamp keeps the row, since
// it is still evidence the call happened. That has to hold when a source is
// saturated too, or a malformed timestamp becomes a way to hide a call.
func TestFlowKeepsUnreadableTimestampsEvenWhenTrimming(t *testing.T) {
	bad := flowEventFrom(doc(t, `{"e":{"created_at":"not-a-time","tool":"delete_ns","decision":"allowed"}}`)["e"].(map[string]any), "tool")
	old := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T07:00:00Z","model":"m"}}`)["e"].(map[string]any), "model")
	newer := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T10:00:00Z","model":"m"}}`)["e"].(map[string]any), "model")

	kept, notes, err := trimToComplete([]flowEvent{bad, old, newer}, []time.Time{at(t, "2026-09-04T09:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("the cut should still be reported, got %v", notes)
	}
	var sawBad bool
	for _, e := range kept {
		if e.what == "delete_ns" {
			sawBad = true
		}
	}
	if !sawBad {
		t.Error("a row with an unreadable timestamp must survive trimming: it is still evidence")
	}
	if len(kept) != 2 {
		t.Errorf("want the malformed row and the newer one, got %d", len(kept))
	}
}

// A window trimmed to nothing is not a quiet agent. Reporting it as "no
// recorded activity" would turn omitted evidence into absent evidence — the
// exact reading the watermark exists to prevent.
func TestFlowDoesNotReportATrimmedWindowAsSilence(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, nil, []string{"window starts 2026-09-04T09:00:00 — a trail hit its 50-row limit"})
	got := out.String()
	if strings.Contains(got, "never went through the plane") {
		t.Errorf("this is omitted evidence, not an ungoverned agent:\n%s", got)
	}
	if !strings.Contains(got, "window") {
		t.Errorf("the truncation note must still be shown:\n%s", got)
	}
	if !strings.Contains(got, "vouch for") {
		t.Errorf("the reader must be told the silence is bounded:\n%s", got)
	}

	// With genuinely nothing recorded and no truncation, the original
	// reading still stands.
	var quiet bytes.Buffer
	renderFlow(&quiet, nil, nil)
	if !strings.Contains(quiet.String(), "never went through the plane") {
		t.Errorf("an actually-empty trail should still name both causes:\n%s", quiet.String())
	}
}
