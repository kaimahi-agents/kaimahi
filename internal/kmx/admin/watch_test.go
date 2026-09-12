package admin

// `kmx watch` against a fake plane.
//
// The behaviours under test are the ones that decide whether an operator can
// believe an empty feed: that an event is printed once, that a gap in
// reading is not printed as a gap in activity, and that a watch which has
// stopped watching says so instead of going quiet.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pollingPlane serves a growing tool-audit trail: each poll after the first
// `after` polls includes one more row, which is how a live cluster looks
// through this endpoint.
func pollingPlane(t *testing.T, rowsByPoll [][]string) (http.HandlerFunc, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	return health(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "tool-audit") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"entries":[]}`))
			return
		}
		n := int(polls.Add(1)) - 1
		if n >= len(rowsByPoll) {
			n = len(rowsByPoll) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"entries":[%s]}`, strings.Join(rowsByPoll[n], ","))
	}), &polls
}

func toolRow(at, tool, decision string) string {
	return fmt.Sprintf(`{"created_at":%q,"credential":"ap-agent","tool":%q,"decision":%q,"status":"403",
	  "detail":"outside the standing constraint","upstream":"erp"}`, at, tool, decision)
}

// All surviving sources must contribute on each poll; the retired inbound
// endpoint returns 404 and must not interrupt the feed.
func TestWatchCombinesSurvivingTrails(t *testing.T) {
	replies := map[string]string{
		"/admin/ledger":         `{"created_at":"2026-09-04T14:00:03Z","credential":"agent-1","model":"gpt-4o","status":200,"cost_source":"priced","cost_cents":14,"input_tokens":1204,"output_tokens":88,"upstream":"openai","caller_claim":"ua:client","caller_addr":"10.0.0.1"}`,
		"/admin/tool-audit":     `{"created_at":"2026-09-04T14:00:01Z","credential":"agent-1","tool":"delete_ns","decision":"denied","status":403,"arg_summary":"delete_ns: namespace prod","arg_digest":"abcdef1234567890"}`,
		"/admin/approval-audit": `{"created_at":"2026-09-04T14:00:02Z","credential":"agent-1","kind":"tool","subject":"delete_ns","action":"approved","decided_by":"alice","bounds":"uses=1"}`,
	}
	polls := map[string]int{}
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		row, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if q := r.URL.Query(); q.Get("credential") != "agent-1" || q.Get("limit") != "50" {
			t.Errorf("%s lost its credential filter or page limit: %v", r.URL.Path, q)
		}
		polls[r.URL.Path]++
		if polls[r.URL.Path] == 1 {
			row = ""
		}
		fmt.Fprintf(w, `{"entries":[%s]}`, row)
	}))
	var out bytes.Buffer
	if err := c.Watch(&out, make(chan struct{}), WatchOptions{
		Credential: "agent-1", Interval: time.Millisecond, Limit: 3, For: time.Second, JSON: true,
	}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want all three events once, got:\n%s", out.String())
	}
	for i, want := range []struct {
		kind, what, detail string
		denied             bool
	}{
		{"tool", "delete_ns", "delete_ns: namespace prod [abcdef123456]", true},
		{"approval", "tool:delete_ns", "by alice uses=1", false},
		{"model", "gpt-4o", `called by claimed "ua:client" from 10.0.0.1`, false},
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] != want.kind || event["what"] != want.what || event["credential"] != "agent-1" || event["denied"] != want.denied || !strings.Contains(event["detail"].(string), want.detail) {
			t.Errorf("event %d lost ordering, attribution or decision detail: %v", i, event)
		}
	}
}

// The first poll is the baseline. Printing it would bury the event the
// operator started the watch to see under everything that already happened.
func TestWatchDoesNotPrintTheHistoryItStartedFrom(t *testing.T) {
	existing := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	handler, _ := pollingPlane(t, [][]string{existing, existing})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(120 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: time.Second, Replay: 0}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if strings.Contains(out.String(), "invoice_get") {
		t.Errorf("the baseline was printed as if it had just happened:\n%s", out.String())
	}
}

// --replay is how somebody asks for that history deliberately.
func TestWatchReplaysWhenAsked(t *testing.T) {
	existing := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	handler, _ := pollingPlane(t, [][]string{existing, existing})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(120 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: time.Second, Replay: 5}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if !strings.Contains(out.String(), "invoice_get") {
		t.Errorf("--replay printed nothing:\n%s", out.String())
	}
}

// A new row appears once, and only once. The plane exposes no row id on
// these endpoints, so this is the property the content key has to buy.
func TestWatchPrintsEachEventExactlyOnce(t *testing.T) {
	first := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	second := append([]string{toolRow("2026-09-04T14:00:05Z", "payment_schedule", "denied")}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second, second, second})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(260 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: 60 * time.Millisecond}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if n := strings.Count(out.String(), "payment_schedule"); n != 1 {
		t.Errorf("the new event was printed %d times, want 1:\n%s", n, out.String())
	}
}

// Two events in the same second are ordinary — the plane's timestamps are
// second-resolution — so a high-water mark alone would silently drop one.
func TestWatchKeepsBothEventsInsideOneSecond(t *testing.T) {
	first := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	second := append([]string{
		toolRow("2026-09-04T14:00:05Z", "payment_schedule", "denied"),
		toolRow("2026-09-04T14:00:05Z", "dispute_open", "denied"),
	}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second, second})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: 60 * time.Millisecond}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	for _, want := range []string{"payment_schedule", "dispute_open"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("an event sharing a second was dropped (%s):\n%s", want, out.String())
		}
	}
}

// A denial is the line the operator is watching for, so it is the one the
// feed marks.
func TestWatchMarksADenial(t *testing.T) {
	first := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	second := append([]string{toolRow("2026-09-04T14:00:05Z", "payment_schedule", "denied")}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second, second})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: 60 * time.Millisecond}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if !strings.Contains(out.String(), "DENIED") {
		t.Errorf("the denial is not marked:\n%s", out.String())
	}
}

// The structured mode is one object per line, so a script can read it as it
// arrives rather than waiting for the watch to end.
func TestWatchJSONIsOneObjectPerLine(t *testing.T) {
	first := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	second := append([]string{toolRow("2026-09-04T14:00:05Z", "payment_schedule", "denied")}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second, second})
	c, _ := open(t, handler)

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: 60 * time.Millisecond, JSON: true}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("a line is not an object: %q (%v)", line, err)
		}
		if event["denied"] == true && event["what"] != "payment_schedule" {
			t.Errorf("the wrong event was marked denied: %v", event)
		}
	}
}

// --limit is what makes this usable from a script or a test at all.
func TestWatchStopsAtTheLimit(t *testing.T) {
	first := []string{toolRow("2026-09-04T14:00:00Z", "invoice_get", "allowed")}
	second := append([]string{
		toolRow("2026-09-04T14:00:05Z", "payment_schedule", "denied"),
		toolRow("2026-09-04T14:00:06Z", "dispute_open", "denied"),
	}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second, second})
	c, _ := open(t, handler)

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- c.Watch(&out, make(chan struct{}), WatchOptions{Interval: 50 * time.Millisecond, Limit: 1})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("--limit did not stop the watch")
	}
	if n := strings.Count(out.String(), "denied") + strings.Count(out.String(), "DENIED"); n != 1 {
		t.Errorf("--limit 1 printed %d events:\n%s", n, out.String())
	}
}

// A read that failed is not a quiet cluster. Saying nothing here would let an
// operator read an empty feed as "nothing is happening" when the truth is
// "nothing is being read".
func TestWatchSaysWhenItCannotRead(t *testing.T) {
	var polls atomic.Int32
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tool-audit") && polls.Add(1) > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[]}`))
	}))

	var out bytes.Buffer
	stop := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(stop) }()
	if err := c.Watch(&out, stop, WatchOptions{Interval: 50 * time.Millisecond}); err != nil {
		t.Fatalf("watch returned early: %v", err)
	}
	if !strings.Contains(out.String(), "NOT an absence of activity") {
		t.Errorf("a failed read was not reported as a gap in reading:\n%s", out.String())
	}
}

// And a watch that cannot recover stops being a reading of anything, so it
// fails rather than sitting there looking calm.
func TestWatchFailsAfterItStopsBeingAReading(t *testing.T) {
	var polls atomic.Int32
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tool-audit") && polls.Add(1) > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[]}`))
	}))

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- c.Watch(&out, make(chan struct{}), WatchOptions{Interval: 20 * time.Millisecond}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no longer a reading") {
			t.Fatalf("a broken watch did not fail: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a broken watch never gave up")
	}
}

// The bounded set must forget, or a long watch grows without limit.
func TestSeenSetForgetsTheOldestKeys(t *testing.T) {
	s := newSeenSet(2)
	s.add("a")
	s.add("b")
	s.add("c")
	if s.has("a") {
		t.Error("the oldest key was kept past the window")
	}
	if !s.has("b") || !s.has("c") {
		t.Error("a key inside the window was forgotten")
	}
}
