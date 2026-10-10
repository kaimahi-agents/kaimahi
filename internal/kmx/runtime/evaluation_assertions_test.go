package runtime

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestEvaluationCaseAssertionsIncludesLegacyOrdinalsWithoutMutatingCase(t *testing.T) {
	c := EvaluationCase{ExpectContains: []string{"hello", "hello"}, Assertions: []EvaluationAssertion{{ID: "negative", Type: "notContains", Value: "refuse"}}}
	got := EvaluationCaseAssertions(c)
	want := []EvaluationAssertion{
		{ID: "expectContains.0", Type: "contains", Value: "hello"},
		{ID: "expectContains.1", Type: "contains", Value: "hello"},
		{ID: "negative", Type: "notContains", Value: "refuse"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("assertions = %+v, want %+v", got, want)
	}
	got[2].Value = "changed"
	if c.Assertions[0].Value != "refuse" {
		t.Fatal("normalization mutated the authored case")
	}
}

func TestEvaluateAssertionsTextEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, kind, value, pattern, answer string
		available                          bool
		want                               EvaluationVerdict
	}{
		{"contains present", "contains", "hello", "", "hello world", true, EvaluationPass},
		{"contains absent", "contains", "Hello", "", "hello world", true, EvaluationFail},
		{"notContains absent", "notContains", "refuse", "", "hello world", true, EvaluationPass},
		{"notContains present", "notContains", "world", "", "hello world", true, EvaluationFail},
		{"regex matches", "regex", "", "^hello [a-z]+$", "hello world", true, EvaluationPass},
		{"regex mismatch", "regex", "", "^hello$", "hello world", true, EvaluationFail},
		{"contains unavailable", "contains", "hello", "", "hello", false, EvaluationUnknown},
		{"negative unavailable", "notContains", "refuse", "", "", false, EvaluationUnknown},
		{"regex unavailable", "regex", "", "^$", "", false, EvaluationUnknown},
		{"known empty contains", "contains", "hello", "", "", true, EvaluationFail},
		{"known empty negative", "notContains", "refuse", "", "", true, EvaluationPass},
		{"known empty regex", "regex", "", "^$", "", true, EvaluationPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := EvaluationAssertion{ID: "text", Type: tc.kind, Value: tc.value, Pattern: tc.pattern}
			got := EvaluateAssertions(EvaluationCase{Assertions: []EvaluationAssertion{a}}, EvaluationEvidence{Answer: tc.answer, AnswerAvailable: tc.available})
			if len(got) != 1 || got[0].Verdict != tc.want || got[0].ID != "text" || got[0].Type != tc.kind || len(got[0].DefinitionDigest) != 64 || got[0].Reason == "" {
				t.Fatalf("results = %+v, want %s", got, tc.want)
			}
			if !tc.available && got[0].Reason != "answer unavailable" {
				t.Fatalf("unavailable reason = %q", got[0].Reason)
			}
		})
	}
	got := EvaluateAssertions(EvaluationCase{ExpectContains: []string{"hello"}}, EvaluationEvidence{Answer: "hello", AnswerAvailable: true})
	if len(got) != 1 || got[0].ID != "expectContains.0" || got[0].Verdict != EvaluationPass {
		t.Fatalf("legacy result = %+v", got)
	}
}

func TestEvaluateAssertionsToolCapabilityAndCompleteness(t *testing.T) {
	c := EvaluationCase{Assertions: []EvaluationAssertion{
		{ID: "positive", Type: "toolCalled", Tool: "inventory"},
		{ID: "negative", Type: "toolNotCalled", Tool: "inventory"},
	}}
	for _, tc := range []struct {
		name                string
		supported, complete bool
		calls               map[string]int
		want                []EvaluationVerdict
	}{
		{"unsupported positive complete", false, true, map[string]int{"inventory": 1}, []EvaluationVerdict{EvaluationUnknown, EvaluationUnknown}},
		{"unsupported zero complete", false, true, map[string]int{}, []EvaluationVerdict{EvaluationUnknown, EvaluationUnknown}},
		{"unsupported incomplete", false, false, nil, []EvaluationVerdict{EvaluationUnknown, EvaluationUnknown}},
		{"supported called", true, true, map[string]int{"inventory": 2}, []EvaluationVerdict{EvaluationPass, EvaluationFail}},
		{"supported absent", true, true, map[string]int{}, []EvaluationVerdict{EvaluationFail, EvaluationPass}},
		{"supported other tool", true, true, map[string]int{"other": 1}, []EvaluationVerdict{EvaluationFail, EvaluationPass}},
		{"supported zero count", true, true, map[string]int{"inventory": 0}, []EvaluationVerdict{EvaluationFail, EvaluationPass}},
		{"supported missing evidence", true, true, nil, []EvaluationVerdict{EvaluationUnknown, EvaluationUnknown}},
		{"supported incomplete positive", true, false, map[string]int{"inventory": 1}, []EvaluationVerdict{EvaluationUnknown, EvaluationUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateAssertions(c, EvaluationEvidence{ToolSupported: tc.supported, ToolEvidenceComplete: tc.complete, ToolCalls: tc.calls})
			if len(got) != 2 {
				t.Fatalf("results = %+v", got)
			}
			for i, row := range got {
				if row.Verdict != tc.want[i] {
					t.Fatalf("results = %+v, want %v", got, tc.want)
				}
				if !tc.supported && row.Reason != "runtime does not support tools" {
					t.Fatalf("unsupported reason = %q", row.Reason)
				}
				if tc.supported && (!tc.complete || tc.calls == nil) && row.Reason != "tool evidence unavailable" {
					t.Fatalf("unavailable tool reason = %q", row.Reason)
				}
			}
		})
	}
}

func TestEvaluationAssertionsVerdictPrecedence(t *testing.T) {
	for _, tc := range []struct {
		verdicts []EvaluationVerdict
		want     EvaluationVerdict
	}{
		{nil, EvaluationUnknown},
		{[]EvaluationVerdict{EvaluationPass, EvaluationPass}, EvaluationPass},
		{[]EvaluationVerdict{EvaluationPass, EvaluationUnknown}, EvaluationUnknown},
		{[]EvaluationVerdict{EvaluationUnknown, EvaluationFail}, EvaluationFail},
		{[]EvaluationVerdict{EvaluationFail, EvaluationUnknown, EvaluationPass}, EvaluationFail},
	} {
		var rows []EvaluationAssertionResult
		for _, verdict := range tc.verdicts {
			rows = append(rows, EvaluationAssertionResult{Verdict: verdict})
		}
		if got := EvaluationAssertionsVerdict(rows); got != tc.want {
			t.Fatalf("aggregate %v = %s, want %s", tc.verdicts, got, tc.want)
		}
	}
}

func TestEvaluationAssertionDigestsBindDefinitionsNotIDs(t *testing.T) {
	a := EvaluationAssertion{ID: "first", Type: "contains", Value: "hello"}
	base := EvaluationAssertionDigest(a)
	if !regexpDigest(base) {
		t.Fatalf("invalid digest %q", base)
	}
	b := a
	b.ID = "second"
	if EvaluationAssertionDigest(b) != base {
		t.Fatal("assertion id changed definition identity")
	}
	for _, changed := range []EvaluationAssertion{
		{Type: "contains", Value: "Hello"},
		{Type: "notContains", Value: "hello"},
		{Type: "regex", Pattern: "hello"},
		{Type: "toolCalled", Tool: "hello"},
		{Type: "toolNotCalled", Tool: "hello"},
	} {
		if EvaluationAssertionDigest(changed) == base {
			t.Fatalf("changed definition has the same digest: %+v", changed)
		}
	}
	for _, a := range []EvaluationAssertion{{Type: "regex", Pattern: "a"}, {Type: "toolCalled", Tool: "a"}} {
		b := a
		b.Pattern, b.Tool = "b", "b"
		if EvaluationAssertionDigest(a) == EvaluationAssertionDigest(b) {
			t.Fatal("authored operand change not bound by digest")
		}
	}
	const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if EvaluationInputDigest("hello") != helloSHA256 || EvaluationCaseDigest([]byte("hello")) != helloSHA256 {
		t.Fatal("input/case digests do not hash exact bytes")
	}
	if EvaluationInputDigest("hello\n") == helloSHA256 || EvaluationCaseDigest([]byte("hello\n")) == helloSHA256 {
		t.Fatal("digest ignored trailing newline")
	}
}

func regexpDigest(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) < 0
}

func TestEvaluationResultsJSONDoesNotCarryOperands(t *testing.T) {
	c := EvaluationCase{Assertions: []EvaluationAssertion{
		{ID: "contains", Type: "contains", Value: "needle-private-canary"},
		{ID: "regex", Type: "regex", Pattern: "pattern-private-canary"},
		{ID: "tool", Type: "toolCalled", Tool: "tool-private-canary"},
	}}
	rows := EvaluateAssertions(c, EvaluationEvidence{Answer: "answer-private-canary", AnswerAvailable: true})
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"needle-private-canary", "pattern-private-canary", "tool-private-canary", "answer-private-canary"} {
		if strings.Contains(string(data), payload) {
			t.Fatalf("payload leaked: %s", data)
		}
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected three payload-free rows, got %s", data)
	}
	for _, row := range decoded {
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"definitionDigest", "id", "reason", "type", "verdict"}) {
			t.Fatalf("unexpected result fields %v", keys)
		}
	}
	var roundTrip []EvaluationAssertionResult
	if err := json.Unmarshal(data, &roundTrip); err != nil || !slices.Equal(roundTrip, rows) {
		t.Fatalf("result rows did not round-trip: %+v (%v)", roundTrip, err)
	}
	// Additive lifecycle fields preserve the old caller contract, but receipts
	// accept only result rows, never authored definitions.
	request := EvaluationRequest{ExpectContains: []string{"legacy"}, Assertions: c.Assertions}
	receipt := EvaluationReceipt{Assertions: rows}
	if len(request.ExpectContains) != 1 || !reflect.DeepEqual(receipt.Assertions, rows) {
		t.Fatal("lifecycle assertion contract changed")
	}
	data, err = json.Marshal(receipt)
	if err != nil || strings.Contains(string(data), "private-canary") {
		t.Fatalf("receipt leaked operands: %s (%v)", data, err)
	}
}

func TestValidateEvaluationAssertionsStandalone(t *testing.T) {
	valid := EvaluationAssertion{ID: "a", Type: "contains", Value: "hello"}
	if err := ValidateEvaluationAssertions([]EvaluationAssertion{valid}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvaluationAssertions(nil); err != nil {
		t.Fatalf("empty explicit definitions should permit legacy request: %v", err)
	}
	for _, invalid := range []EvaluationAssertion{
		{ID: "a", Type: "contains", Value: "hello", Tool: "wrong"},
		{ID: "a", Type: "regex", Pattern: "[private-canary"},
		{ID: "a", Type: "toolCalled", Tool: ""},
		{ID: "a", Type: "llmJudge", Value: "hello"},
		{ID: "expectContains.custom", Type: "contains", Value: "hello"},
		{ID: "a", Type: "contains", Value: string([]byte{0xff})},
	} {
		if err := ValidateEvaluationAssertions([]EvaluationAssertion{invalid}); err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatalf("invalid assertion not safely refused: %v", err)
		}
	}
	if err := ValidateEvaluationAssertions([]EvaluationAssertion{valid, valid}); err == nil {
		t.Fatal("duplicate explicit ids accepted")
	}
}
