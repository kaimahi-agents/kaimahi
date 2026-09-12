package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
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

// The ledger is ordered by time. Removed endpoints must not prevent a reading.
func TestFlowOrdersModelLedgerWithoutRetiredEndpoints(t *testing.T) {
	replies := map[string]string{
		"/admin/ledger": `{"entries":[
			{"created_at":"2026-09-04T14:22:03Z","credential":"agent-1","model":"gpt-4o","status":200,
			 "cost_source":"priced","cost_cents":14,"input_tokens":1204,"output_tokens":88,"upstream":"openai"},
			{"created_at":"2026-09-04T14:22:02Z","credential":"agent-1","model":"gpt-4o-mini","status":429,
			 "cost_source":"denied","cost_cents":0,"input_tokens":0,"output_tokens":0,"upstream":"openai"}]}`,
	}
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		body, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if q := r.URL.Query(); q.Get("credential") != "agent-1" || q.Get("limit") != "50" {
			t.Errorf("%s lost its credential filter or page limit: %v", r.URL.Path, q)
		}
		io.WriteString(w, body)
	}))

	merged, notes, err := c.flowEvents("agent-1")
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
	want := []string{"model:gpt-4o-mini", "model:gpt-4o"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// A cutoff is a limit on evidence, not proof that older calls did not happen.
func TestFlowRefusesToShowAWindowItCannotVouchFor(t *testing.T) {
	old := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T07:00:00Z","model":"gpt-4o"}}`)["e"].(map[string]any), "model")
	mid := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T09:30:00Z","model":"gpt-4o"}}`)["e"].(map[string]any), "model")
	newer := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T10:00:00Z","model":"gpt-4o"}}`)["e"].(map[string]any), "model")

	// The recorded cutoff reaches back only to 09:00.
	kept, notes, err := trimToComplete([]flowEvent{old, mid, newer}, []cutoff{{at: at(t, "2026-09-04T09:00:00Z"), limit: flowLimit}})
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
		[]cutoff{{at: at(t, "2026-09-04T08:00:00Z"), limit: flowLimit},
			{at: at(t, "2026-09-04T09:00:00Z"), limit: flowLimit}})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "09:00:00") {
		t.Fatalf("want the later watermark (09:00), got %v", notes)
	}
	if !strings.Contains(notes[0], "50-row") {
		t.Fatalf("the note must name the fetched page limit: %v", notes)
	}
}

// A trail's position in the fetch order must not decide the watermark.
func TestFlowWatermarkDoesNotDependOnSourceOrder(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T08:30:00Z","model":"m"}}`)["e"].(map[string]any), "model")
	for _, times := range [][]string{
		{"2026-09-04T08:00:00Z", "2026-09-04T09:00:00Z"},
		{"2026-09-04T09:00:00Z", "2026-09-04T08:00:00Z"},
	} {
		kept, notes, err := trimToComplete([]flowEvent{e}, []cutoff{
			{at: at(t, times[0]), limit: flowLimit},
			{at: at(t, times[1]), limit: flowLimit},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "09:00:00") {
			t.Errorf("source order %v kept %d incomplete events; notes: %v", times, len(kept), notes)
		}
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

// Totals count model-proxy refusals, not ordinary upstream errors.
func TestFlowTotalsCountRefusalsAndCents(t *testing.T) {
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","model":"m","status":200,"cost_source":"priced","cost_cents":14}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:01Z","model":"m","status":403,"cost_source":"denied","cost_cents":0}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:03Z","model":"m","status":429,"cost_source":"denied","cost_cents":0}}`)["e"].(map[string]any), "model"),
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:04Z","model":"m","status":403,"cost_source":"free","cost_cents":0}}`)["e"].(map[string]any), "model"),
	}, nil)
	if !strings.Contains(out.String(), "4 events, 14 cents, 2 refused") {
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
	e := flowEventFrom(doc(t, `{"e":{"created_at":"not-a-time","model":"model","status":200}}`)["e"].(map[string]any), "model")
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
		// a model row with no caller attribution
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:00Z","model":"m",
			"status":200,"input_tokens":1,"output_tokens":2,"upstream":"openai"}}`)["e"].(map[string]any), "model"),
		// a denied model row with no caller attribution
		flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T14:00:02Z","model":"m",
			"status":429,"cost_source":"denied","cost_cents":0,"input_tokens":0,"output_tokens":0,"upstream":"openai"}}`)["e"].(map[string]any), "model"),
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
			"model":"gpt-4o","status":429,"cost_source":"denied","cost_cents":0}}`)["e"].(map[string]any), "model"),
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
	// The rendered line has to be read column by column. Every flow line
	// already contains a "-" in its date and in the trailing summary, so
	// looking for one anywhere proves nothing at all.
	var out bytes.Buffer
	renderFlow(&out, []flowEvent{blank, e}, nil)
	credentials := flowCredentialColumn(t, out.String())
	if want := []string{"-", "triage"}; !reflect.DeepEqual(credentials, want) {
		t.Errorf("credential column is %v, want %v — an absent credential must print as - so the column is never ambiguous:\n%s", credentials, want, out.String())
	}
}

// flowCredentialColumn pulls the credential cell out of each rendered event
// line, so a test can say what that one column holds rather than searching the
// whole page for a character that appears in every date.
func flowCredentialColumn(t *testing.T, rendered string) []string {
	t.Helper()
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		fields := strings.Fields(line)
		// Skip the header and the trailing "--" notes; event lines start
		// with the timestamp the plane stamped.
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "2026-") {
			continue
		}
		got = append(got, fields[1])
	}
	if len(got) == 0 {
		t.Fatalf("no event lines found; this test cannot read the rendering:\n%s", rendered)
	}
	return got
}

// A full ledger page discloses the cutoff. Malformed rows and the oldest
// parseable call survive, regardless of the page's order.
func TestFlowJudgesSaturationAtTheFetchedLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pageRows int
		wantNote bool
	}{
		{"partial", 49, false},
		{"full", 50, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := []string{
				ledgerRow("not-a-time", "malformed-time-model", 200),
				ledgerRow("2026-09-04T07:00:00Z", "an-early-call", 200),
			}
			for len(page) < tc.pageRows {
				page = append(page, ledgerRow("2026-09-04T10:00:00Z", "later-call", 200))
			}
			c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/admin/ledger" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("limit") != "50" {
					t.Errorf("unexpected page limit: %v", r.URL.Query())
				}
				fmt.Fprintf(w, `{"entries":[%s]}`, strings.Join(page, ","))
			}))
			var out bytes.Buffer
			if err := c.Flow(&out, "agent-1"); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"an-early-call", "malformed-time-model", fmt.Sprintf("%d events", tc.pageRows)} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("reading lost %q:\n%s", want, &out)
				}
			}
			if got := strings.Contains(out.String(), "hit its 50-row limit"); got != tc.wantNote {
				t.Errorf("window-was-cut note present=%v, want %v:\n%s", got, tc.wantNote, &out)
			}
			if tc.wantNote && !strings.Contains(out.String(), "window starts 2026-09-04T07:00:00") {
				t.Errorf("wrong cutoff:\n%s", &out)
			}
		})
	}
}

// Coverage follows the oldest parseable timestamp, not the row order.
func TestFlowFindsCoverageAcrossUnorderedAndMalformedTimestamps(t *testing.T) {
	page := make([]map[string]any, 0, 4)
	for _, ts := range []string{"2026-09-04T11:00:00Z", "not-a-time", "2026-09-04T09:00:00Z", "2026-09-04T10:00:00Z"} {
		page = append(page, map[string]any{"created_at": ts, "credential": "agent-1"})
	}
	oldest, ok := oldestRow(page)
	if !ok {
		t.Fatal("parseable rows must bound coverage")
	}
	if want := at(t, "2026-09-04T09:00:00Z"); !oldest.Equal(want) {
		t.Errorf("got %v, want %v — the oldest parseable row", oldest, want)
	}
}

// The documented fallback is that an unreadable timestamp keeps the row, since
// it is still evidence the call happened. That has to hold when a source is
// saturated too, or a malformed timestamp becomes a way to hide a call.
func TestFlowKeepsUnreadableTimestampsEvenWhenTrimming(t *testing.T) {
	bad := flowEventFrom(doc(t, `{"e":{"created_at":"not-a-time","model":"malformed-time-model","status":200}}`)["e"].(map[string]any), "model")
	old := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T07:00:00Z","model":"m"}}`)["e"].(map[string]any), "model")
	newer := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-04T10:00:00Z","model":"m"}}`)["e"].(map[string]any), "model")

	kept, notes, err := trimToComplete([]flowEvent{bad, old, newer}, []cutoff{{at: at(t, "2026-09-04T09:00:00Z"), limit: flowLimit}})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("the cut should still be reported, got %v", notes)
	}
	var sawBad bool
	for _, e := range kept {
		if e.what == "malformed-time-model" {
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

// A plane that cannot be read is not a plane with nothing to say. `kmx status`
// draws exactly this line — it prints `unknown` with the reason where it could
// not look, and a real count only where it could — and a timeline that answered
// a failed read with "no recorded activity" would be the second vocabulary for
// the same question: silence and blindness rendered identically, with the
// reassuring one winning.
//
// Get fails on a non-200. A missing or unreadable ledger must not be treated
// as an empty ledger.
func TestFlowRefusesToRenderAnUnreadableTrailAsSilence(t *testing.T) {
	for _, trail := range []string{"ledger"} {
		t.Run(trail, func(t *testing.T) {
			c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/admin/"+trail) {
					http.Error(w, "the store is unreachable", http.StatusBadGateway)
					return
				}
				w.Write([]byte(`{"entries": []}`))
			}))
			var out bytes.Buffer
			err := c.Flow(&out, "agent-1")
			if err == nil {
				t.Fatalf("a %s that could not be read passed for an empty one:\n%s", trail, out.String())
			}
			if !strings.Contains(err.Error(), trail) {
				t.Errorf("the failure does not name the trail that failed: %v", err)
			}
			if strings.Contains(out.String(), "no recorded activity") {
				t.Errorf("blindness was rendered as silence:\n%s", out.String())
			}
		})
	}
}
