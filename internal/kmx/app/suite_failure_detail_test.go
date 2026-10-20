package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func failureDetailFixture(answer string) (suitePassingInventoryCase, suitePassingObservation) {
	threshold := 70
	c := agentruntime.EvaluationCase{ID: "release-facts", Assertions: []agentruntime.EvaluationAssertion{
		{ID: "no-promise", Type: "notContains", Value: "no action required"},
		{ID: "quality", Type: "llmJudge", Rubric: "grounded", JudgeModel: "judge", JudgeVersion: "v1", MinScore: &threshold},
	}}
	rows := []agentruntime.EvaluationAssertionResult{
		{ID: "no-promise", Type: "notContains", Verdict: agentruntime.EvaluationFail},
		{ID: "quality", Type: "llmJudge", Verdict: agentruntime.EvaluationFail, Judge: &agentruntime.EvaluationJudgeEvidence{MinScore: 70, Scores: []int{20, 30}}},
	}
	return suitePassingInventoryCase{Case: c}, suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: c.ID, AnswerSHA256: agentruntime.EvaluationAnswerDigest(answer), Assertions: rows, Verdict: agentruntime.EvaluationFail}}
}

func TestSuiteFailureDetail(t *testing.T) {
	answer := strings.Repeat("background ", 80) + "no action required" + strings.Repeat(" ending", 80)
	frame, obs := failureDetailFixture(answer)
	d := captureSuiteFailure(frame, obs, answer, map[string]suiteJudgeDiagnostic{"quality": {Reasons: []string{"unsupported promise", "missing warning"}}}, nil)
	if d == nil || d.CaseID != "release-facts" || len(d.Assertions) != 2 || d.Assertions[0].Expected != "no action required" || !strings.Contains(d.Assertions[0].Excerpt, "no action required") || d.Assertions[0].ExcerptKind != "match" {
		t.Fatalf("missing assertion failure detail: %+v", d)
	}
	if d.Assertions[1].Threshold == nil || *d.Assertions[1].Threshold != 70 || len(d.Assertions[1].Scores) != 2 || d.Assertions[1].Scores[0] != 20 || len(d.Assertions[1].Reasons) != 2 || len(d.Assertions[0].Excerpt) > 512 {
		t.Fatalf("missing judge detail or unbounded excerpt: %+v", d)
	}
	obs.Assertions[0].Verdict = agentruntime.EvaluationPass
	obs.Assertions[1].Verdict = agentruntime.EvaluationUnknown
	obs.Assertions[1].Judge.Scores = []int{20, 90}
	d = captureSuiteFailure(frame, obs, "", map[string]suiteJudgeDiagnostic{"quality": {Class: "judge-split"}}, nil)
	if len(d.Assertions) != 1 || d.Assertions[0].Class != "judge-split" || d.Assertions[0].ExcerptKind != "leading" {
		t.Fatalf("unknown judge split lost: %+v", d)
	}
	obs.AnswerSHA256 = ""
	d = captureSuiteFailure(frame, obs, answer, nil, errors.New("private raw cause"))
	if d.Answer != "" || d.AnswerStatus != "unavailable" || d.Class != "unknown" || d.Assertions[0].Excerpt != "" {
		t.Fatalf("unattributed answer disclosed: %+v", d)
	}
}

func TestSuiteFailurePrivacy(t *testing.T) {
	unsafe := []string{"Authorization: opaque-private-value", "X-Private-Header: opaque-private-value", "Cookie: session=opaque-private-value", "\xff", "known-private-key"}
	for _, shape := range secretshapes.All() {
		unsafe = append(unsafe, shape.Example, shape.Example[:len(shape.Example)/2]+"\x00"+shape.Example[len(shape.Example)/2:], shape.Example[:len(shape.Example)/2]+"\n"+shape.Example[len(shape.Example)/2:], shape.Example[:len(shape.Example)/2]+"\t"+shape.Example[len(shape.Example)/2:], shape.Example[:len(shape.Example)/2]+"\r"+shape.Example[len(shape.Example)/2:])
	}
	for i, value := range unsafe {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			raw := strings.Repeat("safe ", 14000) + value
			frame, obs := failureDetailFixture(raw)
			frame.Case.ID = value
			frame.Case.Assertions[0].ID = value
			frame.Case.Assertions[0].Value = value
			obs.Assertions[0].ID = value
			notes := map[string]suiteJudgeDiagnostic{"quality": {Reasons: []string{value}}}
			d := captureSuiteFailure(frame, obs, raw, notes, nil, "known-private-key")
			b, err := json.Marshal(d)
			if err != nil || d.Answer != "" || d.AnswerStatus != "withheld" || d.Assertions[0].Expected != "" || d.CaseID != "withheld" || len(d.Assertions[1].Reasons) != 0 {
				t.Fatalf("unsafe field retained: status=%s error=%v", d.AnswerStatus, err)
			}
			if strings.Contains(string(b), value) && value != "\xff" {
				t.Fatal("private content in diagnostic")
			}
			var out bytes.Buffer
			if err := writeSuiteFailureDetail(&out, *d, true); err != nil {
				t.Fatal(err)
			}
			if value != "\xff" && strings.Contains(out.String(), value) {
				t.Fatal("private content printed")
			}
		})
	}
}

func TestSuiteFailureEnvctlAnswerIsUseful(t *testing.T) {
	answer := "## Summary\nNew environments: 8h default; existing environments keep their expiry.\n**Note:** overnight jobs need an explicit lifetime.\nSee https://docs.example/expiry at 12:30."
	frame, obs := failureDetailFixture(answer)
	d := captureSuiteFailure(frame, obs, answer, nil, nil)
	if d.Answer != answer || d.AnswerStatus != "complete" || !strings.Contains(d.Assertions[0].Excerpt, "New environments: 8h") {
		t.Fatalf("ordinary release notes withheld: status=%s", d.AnswerStatus)
	}
	for _, header := range []string{"X-Trace-ID: opaque-private-value", "Content-Type: application/json", "HTTP/1.1 503\r\nServer: internal\r\nVia: internal", "First-Field: private\r\nSecond-Field: private"} {
		obs.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(header)
		d = captureSuiteFailure(frame, obs, header, nil, nil)
		if d.Answer != "" || d.AnswerStatus != "withheld" {
			t.Fatal("HTTP headers retained")
		}
	}
}

func TestSuiteFailureHeaderWithoutWhitespace(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n"} {
		header := "Api-Key:opaque-private-value" + separator + "Trace-Id:trace-private-value"
		frame, obs := failureDetailFixture(header)
		frame.Case.ID, frame.Case.Assertions[0].ID, frame.Case.Assertions[0].Value = header, header, header
		obs.Assertions[0].ID = header
		notes := map[string]suiteJudgeDiagnostic{"quality": {Reasons: []string{header}}}
		d := captureSuiteFailure(frame, obs, header, notes, nil)
		if d.Answer != "" || d.AnswerStatus != "withheld" || d.CaseID != "withheld" || d.Assertions[0].Expected != "" || len(d.Assertions[1].Reasons) != 0 {
			t.Fatal("no-whitespace header block escaped privacy checks")
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := writeSuiteFailureDetail(&out, *d, true); err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"opaque-private-value", "trace-private-value"} {
			if strings.Contains(string(raw), value) || strings.Contains(out.String(), value) {
				t.Fatal("header value serialized or printed")
			}
		}
	}
	// These can also be parsed as consecutive header fields. Prefer withholding
	// ambiguous content rather than weakening the header privacy boundary.
	for _, ambiguous := range []string{"**Score:** 20\n**Reason:** weak", "10:30 start\n11:00 end", "https://a.example/x\nhttps://b.example/y"} {
		if _, ok := suiteDiagnosticText(ambiguous, len(ambiguous), nil); ok {
			t.Fatal("ambiguous consecutive fields were not withheld")
		}
	}
}

func TestSuiteFailureAssertionExcerpts(t *testing.T) {
	for _, kind := range []string{"contains", "regex", "notContains"} {
		t.Run(kind, func(t *testing.T) {
			answer := strings.Repeat("grounded ", 10000) + "forbidden promise"
			frame, obs := failureDetailFixture(answer)
			frame.Case.Assertions = frame.Case.Assertions[:1]
			frame.Case.Assertions[0].Type = kind
			frame.Case.Assertions[0].Value = "missing requirement"
			frame.Case.Assertions[0].Pattern = "missing.*requirement"
			if kind == "notContains" {
				frame.Case.Assertions[0].Value = "forbidden promise"
			}
			obs.Assertions = obs.Assertions[:1]
			obs.Assertions[0].Type = kind
			d := captureSuiteFailure(frame, obs, answer, nil, nil)
			a := d.Assertions[0]
			want := "leading"
			if kind == "notContains" {
				want = "match"
				if !strings.Contains(a.Excerpt, "forbidden promise") {
					t.Fatal("late match not excerpted")
				}
			}
			if a.ExcerptKind != want || len(a.Excerpt) > 512 || d.AnswerStatus != "truncated" || len(d.Answer) > 64<<10 {
				t.Fatal("unbounded or misleading excerpt", a.ExcerptKind, d.AnswerStatus)
			}
		})
	}
}

func TestSuiteFailureClass(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {io.EOF, "eof"}, {tls.RecordHeaderError{}, "tls"},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connection-refused"},
		{errors.New("https://private.invalid Authorization private"), "unknown"},
	} {
		if got := suiteFailureClass(tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
