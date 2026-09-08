package admin

// `kmx flow` is where a reader asks "what happened here", so it is where
// the difference between an agent the plane deployed and a script holding
// its token has to be visible — in the one free-form column, since the
// flow view has no room for a column per fact.

import (
	"strings"
	"testing"
)

func TestFlowSaysWhoCalledAndSaysTheNameIsClaimed(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-08T13:58:28Z","tool":"invoice_get",
		"decision":"allowed","status":"200","acted_for":"none",
		"caller_claim":"ua:curl/8.5.0","caller_addr":"127.0.0.1"}}`)["e"].(map[string]any), "tool")

	if !strings.Contains(e.detail, "claimed ua:curl/8.5.0") {
		t.Errorf("the name is the caller's word and the line must say so, got %q", e.detail)
	}
	if !strings.Contains(e.detail, "from 127.0.0.1") {
		t.Errorf("the observed address is missing, got %q", e.detail)
	}
}

func TestFlowSaysWhoCalledOnTheModelSeamToo(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-08T14:02:01Z","model":"qwen2.5:3b",
		"status":"denied","cost_cents":"0","input_tokens":"0","output_tokens":"0",
		"upstream":"ollama","caller_claim":"ua:curl/8.5.0","caller_addr":"10.244.4.2"}}`)["e"].(map[string]any), "model")

	if !strings.Contains(e.detail, "0 in / 0 out via ollama") {
		t.Errorf("the existing detail was lost, got %q", e.detail)
	}
	if !strings.Contains(e.detail, "called by claimed ua:curl/8.5.0 from 10.244.4.2") {
		t.Errorf("the ledger row does not name its caller, got %q", e.detail)
	}
}

func TestFlowAddsNothingForARowWithNoCallerRecorded(t *testing.T) {
	// A screenful of rows written before the columns existed should not
	// each carry "unrecorded"; the absence is the answer and repeating it
	// buries the rows that do say something.
	for _, row := range []string{
		`{"e":{"created_at":"2026-09-08T13:58:28Z","tool":"invoice_get","decision":"allowed","status":"200"}}`,
		`{"e":{"created_at":"2026-09-08T13:58:28Z","tool":"invoice_get","decision":"allowed","status":"200",
		   "caller_claim":"legacy","caller_addr":"legacy"}}`,
	} {
		e := flowEventFrom(doc(t, row)["e"].(map[string]any), "tool")
		if strings.Contains(e.detail, "called by") {
			t.Errorf("a row with no recorded caller should say nothing, got %q", e.detail)
		}
		if strings.HasSuffix(e.detail, " ") {
			t.Errorf("trailing whitespace breaks the greps CI runs: %q", e.detail)
		}
	}
}

func TestFlowNamesAnUnrecordedClientWhenOnlyTheAddressIsKnown(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-08T13:58:28Z","tool":"invoice_get",
		"decision":"allowed","status":"200","caller_claim":"none","caller_addr":"10.244.1.7"}}`)["e"].(map[string]any), "tool")

	// 'none' means the caller offered no name — a fact about the call, not
	// an absence of record, so the line still reports what was observed.
	if !strings.Contains(e.detail, "from 10.244.1.7") {
		t.Errorf("an observed address must still be reported, got %q", e.detail)
	}
}

func TestFlowKeepsAHostileCallerNameOnOneLine(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-08T13:58:28Z","tool":"invoice_get",
		"decision":"allowed","status":"200",
		"caller_claim":"ua:evil\n2026-09-08T00:00:00 ap-agent erp tools/call payment_schedule allowed 200",
		"caller_addr":"10.244.1.7"}}`)["e"].(map[string]any), "tool")

	if strings.Contains(e.detail, "\n") {
		t.Errorf("a newline in the flow view forges a second event: %q", e.detail)
	}
}
