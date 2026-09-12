package admin

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

// Polls grow the model ledger; retired endpoints are unavailable.
func pollingPlane(t *testing.T, rowsByPoll [][]string) (http.HandlerFunc, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	return health(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/admin/ledger" {
			http.NotFound(w, r)
			return
		}
		n := int(polls.Add(1)) - 1
		if n >= len(rowsByPoll) {
			n = len(rowsByPoll) - 1
		}
		fmt.Fprintf(w, `{"entries":[%s]}`, strings.Join(rowsByPoll[n], ","))
	}), &polls
}

func ledgerRow(at, model string, status int) string {
	source := "free"
	if status == 429 {
		source = "denied"
	}
	return fmt.Sprintf(`{"created_at":%q,"credential":"agent","model":%q,"status":%d,"cost_source":%q,"cost_cents":0,"input_tokens":0,"output_tokens":0,"upstream":"ollama"}`, at, model, status, source)
}

func TestWatchOrdersModelLedgerWithoutRetiredEndpoints(t *testing.T) {
	polls := 0
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/ledger" {
			http.NotFound(w, r)
			return
		}
		if q := r.URL.Query(); q.Get("credential") != "agent-1" || q.Get("limit") != "50" {
			t.Errorf("query: %s %v", r.URL.Path, q)
		}
		polls++
		if polls == 1 {
			w.Write([]byte(`{"entries":[]}`))
			return
		}
		w.Write([]byte(`{"entries":[
			{"created_at":"2026-09-04T14:00:03Z","credential":"agent-1","model":"gpt-4o","status":200,"cost_source":"priced","cost_cents":14,"input_tokens":1204,"output_tokens":88,"upstream":"openai","caller_claim":"ua:client","caller_addr":"10.0.0.1"},
			{"created_at":"2026-09-04T14:00:02Z","credential":"agent-1","model":"gpt-4o-mini","status":429,"cost_source":"denied","cost_cents":0,"input_tokens":0,"output_tokens":0,"upstream":"openai"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Watch(&out, make(chan struct{}), WatchOptions{Credential: "agent-1", Interval: time.Millisecond, Limit: 2, For: time.Second, JSON: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two events: %s", &out)
	}
	for i, want := range []struct {
		at, what, detail string
		denied           bool
	}{
		{"2026-09-04T14:00:02Z", "gpt-4o-mini", "0 in / 0 out via openai", true},
		{"2026-09-04T14:00:03Z", "gpt-4o", `called by claimed "ua:client" from 10.0.0.1`, false},
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil {
			t.Fatal(err)
		}
		if event["at"] != want.at || event["kind"] != "model" || event["what"] != want.what || event["credential"] != "agent-1" || event["denied"] != want.denied || !strings.Contains(event["detail"].(string), want.detail) {
			t.Fatalf("event: %v", event)
		}
	}
}

func TestWatchHistoryIsPrintedOnlyWhenReplayWasRequested(t *testing.T) {
	existing := []string{ledgerRow("2026-09-04T14:00:01Z", "latest", 200), ledgerRow("2026-09-04T14:00:00Z", "earlier", 200)}
	for _, replay := range []int{0, 1, 5} {
		handler, _ := pollingPlane(t, [][]string{existing})
		c, _ := open(t, handler)
		var out bytes.Buffer
		if err := c.Watch(&out, make(chan struct{}), WatchOptions{Interval: time.Second, For: time.Millisecond, Replay: replay}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "latest") != (replay > 0) || strings.Contains(out.String(), "earlier") != (replay > 1) {
			t.Fatalf("replay=%d: %s", replay, &out)
		}
	}
}

func TestWatchPrintsEachEventOnceIncludingEventsInsideOneSecond(t *testing.T) {
	first := []string{ledgerRow("2026-09-04T14:00:00Z", "old", 200)}
	second := append([]string{ledgerRow("2026-09-04T14:00:05Z", "token-capped", 429), ledgerRow("2026-09-04T14:00:05Z", "cent-capped", 429)}, first...)
	handler, polls := pollingPlane(t, [][]string{first, second, second})
	c, _ := open(t, handler)
	var out bytes.Buffer
	if err := c.Watch(&out, make(chan struct{}), WatchOptions{Interval: time.Millisecond, For: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if polls.Load() < 3 {
		t.Fatal("fixture never replayed a page")
	}
	for _, want := range []string{"token-capped", "cent-capped"} {
		if n := strings.Count(out.String(), want); n != 1 {
			t.Fatalf("%s printed %d times: %s", want, n, &out)
		}
	}
	if strings.Count(out.String(), "429") != 2 || strings.Contains(out.String(), "old") {
		t.Fatalf("denials missing or startup history leaked: %s", &out)
	}
}

func TestWatchJSONIsOneObjectPerLineAndStopsAtTheLimit(t *testing.T) {
	first := []string{ledgerRow("2026-09-04T14:00:00Z", "old", 200)}
	second := append([]string{ledgerRow("2026-09-04T14:00:05Z", "token-capped", 429), ledgerRow("2026-09-04T14:00:06Z", "cent-capped", 429)}, first...)
	handler, _ := pollingPlane(t, [][]string{first, second})
	c, _ := open(t, handler)
	var out bytes.Buffer
	if err := c.Watch(&out, make(chan struct{}), WatchOptions{Interval: time.Millisecond, Limit: 1, For: time.Second, JSON: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("limit 1: %s", &out)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	if event["denied"] != true || event["what"] != "token-capped" || event["at"] != "2026-09-04T14:00:05Z" {
		t.Fatalf("event = %v", event)
	}
}

func TestWatchReadFailuresAreReportedAndEventuallyFatal(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		var polls atomic.Int32
		c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/admin/ledger" {
				http.NotFound(w, r)
				return
			}
			n := polls.Add(1)
			if n > 1 && (fatal || n == 2) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"entries":[]}`))
		}))
		var out bytes.Buffer
		err := c.Watch(&out, make(chan struct{}), WatchOptions{Interval: time.Millisecond, For: 100 * time.Millisecond})
		if fatal && (err == nil || !strings.Contains(err.Error(), "no longer a reading") || polls.Load() != 7) {
			t.Fatalf("permanent failure after %d polls: %v", polls.Load(), err)
		}
		if !fatal && (err != nil || polls.Load() < 3) {
			t.Fatalf("transient failure after %d polls: %v", polls.Load(), err)
		}
		if !strings.Contains(out.String(), "NOT an absence of activity") {
			t.Fatalf("gap hidden: %s", &out)
		}
	}
}

func TestWatchStopsWhenInterrupted(t *testing.T) {
	handler, _ := pollingPlane(t, [][]string{nil})
	c, _ := open(t, handler)
	stop := make(chan struct{})
	close(stop)
	if err := c.Watch(&bytes.Buffer{}, stop, WatchOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSeenSetForgetsTheOldestKeys(t *testing.T) {
	s := newSeenSet(2)
	s.add("a")
	s.add("b")
	s.add("c")
	if s.has("a") || !s.has("b") || !s.has("c") {
		t.Fatalf("wrong window: %#v", s)
	}
}
