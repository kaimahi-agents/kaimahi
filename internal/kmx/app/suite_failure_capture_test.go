package app

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSuiteFailureTransportErrorsKeepOriginalMessage(t *testing.T) {
	original := errors.New("Orka result read refused (HTTP 503)")
	err := withSuiteFailureClass(original, "result-http-503")
	if err.Error() != original.Error() || !errors.Is(err, original) || suiteFailureClass(err) != "result-http-503" {
		t.Fatal("diagnostic class changed existing error interface", err)
	}
}

func TestSuiteFailureTransportClassPrecedence(t *testing.T) {
	conn := &orkaResultConn{cancel: func() {}}
	conn.rememberFailure(net.ErrClosed)
	if conn.failureClass() != "" {
		t.Fatal("local close became a transport class")
	}
	conn.rememberFailure(io.EOF)
	s := &orkaResultSession{conn: conn}
	if s.failureClass(context.DeadlineExceeded) != "timeout" {
		t.Fatal("socket close overrode timeout")
	}
	if s.failureClass(context.Canceled) != "eof" {
		t.Fatal("socket EOF lost to lifetime cancellation")
	}
}

func TestSuiteFailureNativeTransportClass(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "http503", true: "disconnect"}[disconnect], func(t *testing.T) {
			a, placement, frame, f := nativeEvaluationFixture(t)
			f.resultStatus = 503
			f.resultDisconnect = disconnect
			bounded, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ctx, _ := newSuiteFailureContext(bounded)
			obs, err := a.evaluateSuiteNative(ctx, placement, frame, f.opt.ResultPort, nil)
			want := "result-http-503"
			if disconnect {
				want = "eof"
			}
			if err == nil || obs.failure == nil || obs.failure.Class != want || suiteFailureClass(err) != want || obs.failure.Answer != "" {
				t.Fatalf("lost safe transport class: %+v err=%v", obs.failure, err)
			}
		})
	}
}

func TestSuiteFailureNativeAttribution(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed failure", true: "post-judge drift"}[drift], func(t *testing.T) {
			a, placement, frame, f := nativeEvaluationFixture(t)
			frame.Case.Assertions[0].Value = "missing requirement"
			ctx, cancel := context.WithTimeout(t.Context(), time.Second*10)
			defer cancel()
			ctx, _ = newSuiteFailureContext(ctx)
			judge := func(ctx context.Context, c agentruntime.EvaluationCase, e agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
				if drift {
					placement.Subject.Target.ClusterUID = "changed"
				}
				return agentruntime.EvaluateAssertions(c, e)
			}
			obs, err := a.evaluateSuiteNative(ctx, placement, frame, f.opt.ResultPort, judge)
			if err == nil || obs.failure == nil {
				t.Fatalf("failure diagnostic absent: %v", err)
			}
			if drift {
				if obs.failure.Answer != "" || obs.failure.AnswerStatus != "unavailable" {
					t.Fatal("drifted content escaped")
				}
			} else {
				if !errors.Is(err, errSuiteNativeCaseFailed) || obs.failure.Answer != f.answers[frame.Case.Input] {
					t.Fatalf("completed failure answer missing: %+v, %v", obs.failure, err)
				}
			}
		})
	}
}
