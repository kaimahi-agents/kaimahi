package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func judgeTestAssertion() EvaluationAssertion {
	min := 50
	return EvaluationAssertion{ID: "judge", Type: "llmJudge", Rubric: "rubric-private-canary", MinScore: &min, JudgeModel: "model/pinned", JudgeVersion: "v1"}
}

func TestEvaluationJudgeDigestBindsAuthoredPolicyAndKeepsOldDigests(t *testing.T) {
	a := judgeTestAssertion()
	base := EvaluationAssertionDigest(a)
	for _, mutate := range []func(*EvaluationAssertion){
		func(a *EvaluationAssertion) { a.Rubric += "changed" },
		func(a *EvaluationAssertion) { n := 51; a.MinScore = &n },
		func(a *EvaluationAssertion) { a.MinScore = nil },
		func(a *EvaluationAssertion) { a.JudgeModel += "changed" },
		func(a *EvaluationAssertion) { a.JudgeVersion += "changed" },
	} {
		b := a
		mutate(&b)
		if EvaluationAssertionDigest(b) == base {
			t.Fatal("judge definition change did not change digest")
		}
	}
	b := a
	b.ID = "other"
	if EvaluationAssertionDigest(b) != base {
		t.Fatal("judge id changed authored definition digest")
	}
	for _, old := range []EvaluationAssertion{
		{Type: "contains", Value: "hello"}, {Type: "notContains", Value: "hello"},
		{Type: "regex", Pattern: "hello"}, {Type: "toolCalled", Tool: "hello"}, {Type: "toolNotCalled", Tool: "hello"},
	} {
		frames := frameEntry("type", []byte(old.Type))
		frames = append(frames, frameEntry("operand", []byte("hello"))...)
		sum := sha256.Sum256(frames)
		if EvaluationAssertionDigest(old) != hex.EncodeToString(sum[:]) {
			t.Fatalf("old %s digest changed", old.Type)
		}
	}
	sum := sha256.Sum256([]byte(EvaluationJudgePolicy))
	if EvaluationJudgeEvaluatorDigest() != hex.EncodeToString(sum[:]) {
		t.Fatal("evaluator digest does not bind fixed policy")
	}
	policyFrames := frameEntry("type", []byte(a.Type))
	policyFrames = append(policyFrames, frameEntry("operand", []byte(a.Rubric))...)
	policyFrames = append(policyFrames, frameEntry("minScore", []byte("50"))...)
	policyFrames = append(policyFrames, frameEntry("judgeModel", []byte(a.JudgeModel))...)
	policyFrames = append(policyFrames, frameEntry("judgeVersion", []byte(a.JudgeVersion))...)
	policyFrames = append(policyFrames, frameEntry("evaluatorDigest", []byte(EvaluationJudgeEvaluatorDigest()))...)
	sum = sha256.Sum256(policyFrames)
	if base != hex.EncodeToString(sum[:]) {
		t.Fatal("judge definition digest does not bind evaluator policy")
	}
	config := EvaluationJudgeConfigurationDigest("https://judge.example/v1", a.JudgeModel, a.JudgeVersion)
	configFrames := frameEntry("endpoint", []byte("https://judge.example/v1"))
	configFrames = append(configFrames, frameEntry("model", []byte(a.JudgeModel))...)
	configFrames = append(configFrames, frameEntry("modelVersion", []byte(a.JudgeVersion))...)
	configFrames = append(configFrames, frameEntry("evaluatorDigest", []byte(EvaluationJudgeEvaluatorDigest()))...)
	sum = sha256.Sum256(configFrames)
	if config != hex.EncodeToString(sum[:]) {
		t.Fatal("configuration digest does not bind endpoint, model, version, and policy")
	}
	if !regexpDigest(config) || config != EvaluationJudgeConfigurationDigest("https://judge.example/v1", a.JudgeModel, a.JudgeVersion) {
		t.Fatal("configuration digest not deterministic SHA-256")
	}
	for _, args := range [][3]string{
		{"https://other.example/v1", a.JudgeModel, a.JudgeVersion},
		{"https://judge.example/v1", "other-model", a.JudgeVersion},
		{"https://judge.example/v1", a.JudgeModel, "v2"},
		{"ab", "c", "d"}, {"a", "bc", "d"},
	} {
		if EvaluationJudgeConfigurationDigest(args[0], args[1], args[2]) == config {
			t.Fatal("configuration change did not change digest")
		}
	}
	if EvaluationJudgeConfigurationDigest("ab", "c", "d") == EvaluationJudgeConfigurationDigest("a", "bc", "d") {
		t.Fatal("configuration digest has ambiguous framing")
	}
}

func TestEvaluationJudgeVerdictRequiresTwoValidThresholdSideScores(t *testing.T) {
	for _, tc := range []struct {
		scores []int
		min    int
		want   EvaluationVerdict
	}{
		{[]int{80, 100}, 50, EvaluationPass}, {[]int{0, 20}, 50, EvaluationFail},
		{[]int{49, 50}, 50, EvaluationUnknown}, {[]int{50, 49}, 50, EvaluationUnknown},
		{[]int{50, 50}, 50, EvaluationPass}, {[]int{0, 0}, 0, EvaluationPass},
		{[]int{100, 100}, 100, EvaluationPass}, {[]int{99, 99}, 100, EvaluationFail},
		{nil, 50, EvaluationUnknown}, {[]int{}, 50, EvaluationUnknown}, {[]int{100}, 50, EvaluationUnknown},
		{[]int{0, 50, 100}, 50, EvaluationUnknown}, {[]int{-1, 100}, 50, EvaluationUnknown},
		{[]int{100, 101}, 50, EvaluationUnknown}, {[]int{80, 100}, -1, EvaluationUnknown},
		{[]int{80, 100}, 101, EvaluationUnknown},
	} {
		if got := EvaluationJudgeVerdict(tc.scores, tc.min); got != tc.want {
			t.Errorf("scores %v threshold %d = %s, want %s", tc.scores, tc.min, got, tc.want)
		}
	}
}

func TestEvaluationJudgeEvidenceIntegrity(t *testing.T) {
	valid := EvaluationAssertionResult{Type: "llmJudge", Verdict: EvaluationPass, Judge: &EvaluationJudgeEvidence{
		Model: "model/pinned", ModelVersion: "v1", Provenance: "user-declared",
		EvaluatorDigest: EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: strings.Repeat("a", 64),
		MinScore: 50, Scores: []int{80, 100},
	}}
	if !EvaluationJudgeEvidenceValid(valid) {
		t.Fatal("valid evidence rejected")
	}
	for _, mutate := range []func(*EvaluationAssertionResult){
		func(r *EvaluationAssertionResult) { r.Type = "contains" },
		func(r *EvaluationAssertionResult) { r.Judge = nil },
		func(r *EvaluationAssertionResult) { r.Judge.Model = "" },
		func(r *EvaluationAssertionResult) { r.Judge.ModelVersion = "v1\nforged" },
		func(r *EvaluationAssertionResult) { r.Judge.Model = "modèle" },
		func(r *EvaluationAssertionResult) { r.Judge.ModelVersion = strings.Repeat("v", 257) },
		func(r *EvaluationAssertionResult) { r.Judge.Model = "gh" + "p_" + strings.Repeat("A", 36) },
		func(r *EvaluationAssertionResult) { r.Judge.Provenance = "verified" },
		func(r *EvaluationAssertionResult) { r.Judge.EvaluatorDigest = strings.Repeat("b", 64) },
		func(r *EvaluationAssertionResult) { r.Judge.ConfigurationDigest = strings.Repeat("G", 64) },
		func(r *EvaluationAssertionResult) { r.Judge.MinScore = -1 },
		func(r *EvaluationAssertionResult) { r.Judge.MinScore = 101 },
		func(r *EvaluationAssertionResult) { r.Judge.Scores = nil },
		func(r *EvaluationAssertionResult) { r.Judge.Scores = []int{100} },
		func(r *EvaluationAssertionResult) { r.Judge.Scores = []int{100, 100, 100} },
		func(r *EvaluationAssertionResult) { r.Judge.Scores = []int{100, 101} },
		func(r *EvaluationAssertionResult) { r.Judge.Scores = []int{-1, 100} },
		func(r *EvaluationAssertionResult) { r.Verdict = EvaluationFail },
		func(r *EvaluationAssertionResult) { r.Verdict = EvaluationUnknown },
	} {
		r := valid
		e := *valid.Judge
		r.Judge = &e
		mutate(&r)
		if EvaluationJudgeEvidenceValid(r) {
			t.Errorf("invalid evidence accepted: %+v %+v", r, r.Judge)
		}
	}
	for _, tc := range []struct {
		scores  []int
		min     int
		verdict EvaluationVerdict
	}{
		{nil, 0, EvaluationUnknown}, {[]int{}, 100, EvaluationUnknown},
		{[]int{49, 50}, 50, EvaluationUnknown}, {[]int{0, 20}, 50, EvaluationFail},
	} {
		r := valid
		e := *valid.Judge
		e.Scores, e.MinScore = tc.scores, tc.min
		r.Judge, r.Verdict = &e, tc.verdict
		if !EvaluationJudgeEvidenceValid(r) {
			t.Errorf("coherent evidence refused: %+v", r)
		}
	}
	if !EvaluationJudgeEvidenceValid(EvaluationAssertionResult{Type: "contains", Verdict: EvaluationPass}) {
		t.Fatal("old ordinary row refused")
	}
}

func TestEvaluationJudgePureUnknownAndPayloadFreePort(t *testing.T) {
	c := EvaluationCase{ID: "case", Input: "input-private-canary", Assertions: []EvaluationAssertion{judgeTestAssertion()}}
	for _, available := range []bool{false, true} {
		rows := EvaluateAssertions(c, EvaluationEvidence{Answer: "answer-private-canary", AnswerAvailable: available})
		if len(rows) != 1 || rows[0].Verdict != EvaluationUnknown || rows[0].Reason != "judge unavailable" || rows[0].Judge != nil {
			t.Fatalf("pure judge evaluator must not invent identity or verdict: %+v", rows)
		}
	}
	// The app-owned evaluator receives ephemeral evidence, and returns the entire
	// case's ordinary and judge rows; the port does not expose payloads in results.
	var judge EvaluationJudge = func(ctx context.Context, c EvaluationCase, e EvaluationEvidence) []EvaluationAssertionResult {
		rows := EvaluateAssertions(c, e)
		rows[0].Judge = &EvaluationJudgeEvidence{Model: "model/pinned", ModelVersion: "v1", Provenance: "user-declared",
			EvaluatorDigest: EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: strings.Repeat("a", 64), MinScore: 50, Scores: []int{80, 100}}
		rows[0].Verdict, rows[0].Reason = EvaluationPass, "assertion satisfied"
		return rows
	}
	req := EvaluationRequest{Judge: judge}
	rows := req.Judge(context.Background(), c, EvaluationEvidence{Answer: "answer-private-canary", AnswerAvailable: true})
	data, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(data), "private-canary") || strings.Contains(string(data), "rubric") || strings.Contains(string(data), "reasoning") {
		t.Fatalf("judge rows leaked payload: %s (%v)", data, err)
	}
	var decoded []EvaluationAssertionResult
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, rows) {
		t.Fatalf("judge evidence did not round-trip: %+v (%v)", decoded, err)
	}
}

func TestValidateEvaluationJudgeProgrammaticOperands(t *testing.T) {
	valid := judgeTestAssertion()
	for _, mutate := range []func(*EvaluationAssertion){
		func(a *EvaluationAssertion) { a.Value = "mixed" },
		func(a *EvaluationAssertion) { a.Pattern = "mixed" },
		func(a *EvaluationAssertion) { a.Tool = "mixed" },
		func(a *EvaluationAssertion) { a.Rubric = string([]byte{0xff}) },
		func(a *EvaluationAssertion) { a.Rubric = "bad\x7fcontrol" },
		func(a *EvaluationAssertion) { a.JudgeVersion = "gh" + "p_" + strings.Repeat("A", 36) },
	} {
		a := valid
		mutate(&a)
		if err := ValidateEvaluationAssertions([]EvaluationAssertion{a}); err == nil {
			t.Fatal("invalid programmatic judge accepted")
		}
	}
	for _, mutate := range []func(*EvaluationAssertion){
		func(a *EvaluationAssertion) { a.Rubric = "mixed" },
		func(a *EvaluationAssertion) { n := 0; a.MinScore = &n },
		func(a *EvaluationAssertion) { a.JudgeModel = "mixed" },
		func(a *EvaluationAssertion) { a.JudgeVersion = "mixed" },
	} {
		a := EvaluationAssertion{ID: "ordinary", Type: "contains", Value: "hello"}
		mutate(&a)
		if err := ValidateEvaluationAssertions([]EvaluationAssertion{a}); err == nil {
			t.Fatal("ordinary programmatic assertion accepted judge metadata")
		}
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
