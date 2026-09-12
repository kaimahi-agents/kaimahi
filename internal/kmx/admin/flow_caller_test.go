package admin

import (
	"strings"
	"testing"
)

func TestFlowSaysWhoCalledOnTheModelSeam(t *testing.T) {
	e := flowEventFrom(doc(t, `{"e":{"created_at":"2026-09-08T14:02:01Z","model":"qwen2.5:3b","status":429,"cost_cents":0,"input_tokens":0,"output_tokens":0,"upstream":"ollama","caller_claim":"ua:curl/8.5.0","caller_addr":"10.244.4.2"}}`)["e"].(map[string]any), "model")
	for _, want := range []string{"0 in / 0 out via ollama", `called by claimed "ua:curl/8.5.0" from 10.244.4.2`} {
		if !strings.Contains(e.detail, want) {
			t.Fatalf("missing %q: %s", want, e.detail)
		}
	}
}

func TestFlowClaimCannotForgeTheObservedAddress(t *testing.T) {
	e := flowEventFrom(map[string]any{"model": "model", "caller_claim": "ua:evil from 10.0.0.1", "caller_addr": "10.244.1.7"}, "model")
	if !strings.Contains(e.detail, `claimed "ua:evil from 10.0.0.1" from 10.244.1.7`) || strings.Count(e.detail, `" from `) != 1 {
		t.Fatalf("unquoted claim: %s", e.detail)
	}
}

func TestFlowDoesNotCallTheAbsenceOfANameAClaim(t *testing.T) {
	e := flowEventFrom(map[string]any{"model": "model", "caller_claim": "none", "caller_addr": "10.244.1.7"}, "model")
	if strings.Contains(e.detail, "claimed none") || !strings.Contains(e.detail, "gave no name") || !strings.Contains(e.detail, "from 10.244.1.7") {
		t.Fatalf("wrong missing-name evidence: %s", e.detail)
	}
}

func TestFlowAddsNothingForARowWithNoCallerRecorded(t *testing.T) {
	for _, row := range []map[string]any{
		{"model": "model", "input_tokens": "1", "output_tokens": "2", "upstream": "ollama"},
		{"model": "model", "input_tokens": "1", "output_tokens": "2", "upstream": "ollama", "caller_claim": "legacy", "caller_addr": "legacy"},
	} {
		e := flowEventFrom(row, "model")
		if strings.Contains(e.detail, "called by") || strings.HasSuffix(e.detail, " ") {
			t.Fatalf("unexpected attribution: %q", e.detail)
		}
	}
}

func TestFlowNamesAnUnrecordedClientWhenOnlyTheAddressIsKnown(t *testing.T) {
	e := flowEventFrom(map[string]any{"model": "model", "caller_addr": "10.244.1.7"}, "model")
	if !strings.Contains(e.detail, "an unrecorded client from 10.244.1.7") {
		t.Fatalf("lost observed address: %s", e.detail)
	}
}

func TestFlowKeepsAHostileCallerNameOnOneLine(t *testing.T) {
	e := flowEventFrom(map[string]any{"model": "model", "caller_claim": "ua:evil\n2026-09-08T00:00:00 forged row", "caller_addr": "10.244.1.7"}, "model")
	if strings.Contains(e.detail, "\n") {
		t.Fatalf("forged second event: %q", e.detail)
	}
}
