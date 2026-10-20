package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSuiteFailureKeyIsFrozenBeforeRuns(t *testing.T) {
	const key = "opaque-private-key"
	t.Setenv("KMX_FAILURE_TEST_KEY", key)
	var frozen string
	_, err := newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: "https://judge.example.invalid", KeyEnv: "KMX_FAILURE_TEST_KEY", diagnosticKey: &frozen}, []agentruntime.EvaluationCase{judgeCase()}, io.Discard)
	if err != nil || frozen != key {
		t.Fatal("judge key not captured during preflight", err)
	}
	t.Setenv("KMX_FAILURE_TEST_KEY", "changed-key")
	frame, _ := failureDetailFixture("")
	frame.Case.ID = key
	var details []suiteFailureDetail
	var output bytes.Buffer
	ctx := withSuiteFailureReporting(t.Context(), &details, &output, false, frozen)
	_, err = evaluateSuiteRunsReporting(ctx, []suitePassingInventoryCase{frame}, time.Second, &output, func(context.Context, suitePassingInventoryCase) (suitePassingObservation, error) {
		return suitePassingObservation{}, errors.New("safe failure before judging")
	}, nil)
	if err == nil || len(details) != 1 || details[0].CaseID != "withheld" || strings.Contains(output.String(), key) {
		t.Fatal("key escaped before judging", err)
	}
}

func TestSuiteFailureJudgeKeyProtectsNonJudgeCase(t *testing.T) {
	key := "opaque-private-key"
	client := evaluationJudgeClient{key: key}
	frame, obs := failureDetailFixture("prefix " + key)
	frame.Case.Assertions = frame.Case.Assertions[:1]
	frame.Case.Assertions[0].Value = "missing"
	ctx, _ := newSuiteFailureContext(t.Context())
	obs.Assertions = client.evaluate(ctx, frame.Case, agentruntime.EvaluationEvidence{Answer: "prefix " + key, AnswerAvailable: true})
	captureSuiteFailureContext(ctx, frame, &obs, "prefix "+key, nil)
	if obs.failure == nil || obs.failure.Answer != "" || obs.failure.AnswerStatus != "withheld" {
		t.Fatal("judge key leaked from non-judge case")
	}
}

func TestSuiteFailureJudgeHeadersAreWithheldBeforeNormalization(t *testing.T) {
	reasoning := "First-Field: private-one\r\nSecond-Field: private-two"
	payload, err := json.Marshal(map[string]any{"score": 20, "reasoning": reasoning})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, judgeEnvelope(string(payload))) }))
	defer server.Close()
	bounded, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx, _ := newSuiteFailureContext(bounded)
	client := evaluationJudgeClient{baseURL: server.URL, httpClient: server.Client(), out: io.Discard}
	c := judgeCase()
	rows := client.evaluate(ctx, c, agentruntime.EvaluationEvidence{Answer: "fixture answer", AnswerAvailable: true})
	if rows[0].Verdict != agentruntime.EvaluationFail || len(rows[0].Judge.Scores) != 2 || rows[0].Judge.Scores[0] != 20 || len(rows[0].Judge.ModelMatches) != 2 {
		t.Fatal("withholding rationale changed accepted scores or model matches", rows)
	}
	frame := suitePassingInventoryCase{Case: c, Metadata: suitePassingCase{Assertions: make([]suitePassingAssertion, len(rows))}}
	obs := suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationFail, AnswerSHA256: agentruntime.EvaluationAnswerDigest("fixture answer"), Assertions: rows}}
	captureSuiteFailureContext(ctx, frame, &obs, "fixture answer", nil)
	var out bytes.Buffer
	if err := writeSuiteFailureDetail(&out, *obs.failure, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private-one") || strings.Contains(out.String(), "private-two") {
		t.Fatal("parser normalization hid headers from diagnostic privacy checks")
	}
	if !strings.Contains(out.String(), "rationale: unavailable or withheld") || strings.Contains(out.String(), `rationale: ""`) {
		t.Fatal("withheld parser rationale was displayed as empty")
	}
}

func TestSuiteFailureJudgeDiagnosticChannel(t *testing.T) {
	mode := "http"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "http":
			w.Header().Set("X-Private", "header-canary")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "payload-canary")
		case "bad":
			_, _ = io.WriteString(w, `{"private":"body-canary"}`)
		case "good":
			_, _ = io.WriteString(w, judgeEnvelope(`{"score":20,"reasoning":"missing explicit lifetime"}`))
		}
	}))
	defer server.Close()
	client := evaluationJudgeClient{baseURL: server.URL, httpClient: server.Client(), out: io.Discard}
	c := judgeCase()
	for _, tc := range []struct {
		mode, want string
		reasons    int
	}{{"http", "judge-http-503", 0}, {"good", "", 2}, {"bad", "judge-malformed", 0}} {
		mode = tc.mode
		bounded, cancel := context.WithTimeout(t.Context(), time.Second)
		ctx, notes := newSuiteFailureContext(bounded)
		rows := client.evaluate(ctx, c, agentruntime.EvaluationEvidence{Answer: "fictional answer", AnswerAvailable: true})
		cancel()
		note, ok := notes.judge["quality"]
		if !ok || note.Class != tc.want || len(note.Reasons) != tc.reasons {
			t.Fatalf("mode %s diagnostics=%+v", mode, notes)
		}
		if strings.Contains(note.Class, "canary") || note.CaseID != c.ID {
			t.Fatal("unsafe/unattributed diagnostic")
		}
		if mode != "good" && (rows[0].Verdict != agentruntime.EvaluationUnknown || len(rows[0].Judge.Scores) != 0) {
			t.Fatal("diagnostics changed judge evidence")
		}
	}
	server.Close()
	bounded, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx, notes := newSuiteFailureContext(bounded)
	client.evaluate(ctx, c, agentruntime.EvaluationEvidence{Answer: "fictional answer", AnswerAvailable: true})
	if notes.judge["quality"].Class != "connection-refused" {
		t.Fatalf("actual transport class lost: %+v", notes)
	}
}
