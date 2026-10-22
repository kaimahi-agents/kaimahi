package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type previewCopilotAccess struct {
	t            *testing.T
	clients      int
	sends        int
	clientErr    error
	sendErr      error
	sendErrAt    int
	returnModel  string
	nilTransport bool
}

func (p *previewCopilotAccess) Client(ctx context.Context, route modelaccess.Route) (http.RoundTripper, error) {
	p.clients++
	if ctx.Err() != nil || route != copilotPreviewRoute() {
		p.t.Fatalf("unexpected auth request: %+v / %v", route, ctx.Err())
	}
	if p.nilTransport {
		return nil, nil
	}
	return p, p.clientErr
}

func (p *previewCopilotAccess) RoundTrip(req *http.Request) (*http.Response, error) {
	p.sends++
	if req.Method != http.MethodPost || req.URL.String() != modelaccess.CopilotEndpoint+"/chat/completions" ||
		req.Header.Get("Accept") != "application/json" || req.Header.Get("Authorization") != "" {
		p.t.Fatal("runner bypassed the credential-owning transport")
	}
	if p.sendErr != nil && (p.sendErrAt == 0 || p.sends == p.sendErrAt) {
		return nil, p.sendErr
	}
	var body directChat
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		p.t.Fatal(err)
	}
	if body.Model != "gpt-test" || body.Stream || body.MaxCompletionTokens != 1024 ||
		len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" ||
		strings.Contains(body.Messages[0].Content, "## Examples") {
		p.t.Fatalf("unexpected completion request: %+v", body)
	}
	answer := "Hello, PRIVATE INPUT"
	if body.Messages[0].Content == directJudgePolicy {
		answer = `{"score":95,"reasoning":"PRIVATE JUDGE REASON"}`
	}
	model := "gpt-test"
	if p.returnModel != "" {
		model = p.returnModel
	}
	response := httptest.NewRecorder()
	previewEnvelope(response, model, answer)
	return response.Result(), nil
}

func copilotPreviewRoute() modelaccess.Route {
	return modelaccess.Route{Provider: "copilot", Model: "gpt-test", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
}

func TestDirectPreviewCopilotRunTestAndJudge(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		for _, judge := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/judge=%v", operation, judge), func(t *testing.T) {
				provider := &previewCopilotAccess{t: t}
				opt := directOptions(t, "gpt-test", judge)
				var diagnostics strings.Builder
				opt.Progress = func(message string) error {
					diagnostics.WriteString(message + "\n")
					return nil
				}
				route := copilotPreviewRoute()
				opt.Route, opt.Operation = &route, operation
				if operation == "run" {
					input := "PRIVATE AD HOC INPUT"
					opt.Input = &input
				}
				result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
				sends := 2
				if judge {
					sends += 2
				}
				if operation == "run" {
					sends++
				}
				if err != nil || !result.FullPass || result.Verdict != runtime.EvaluationPass ||
					provider.clients != 1 || provider.sends != sends || result.Runner != "preview runner (no journal)" ||
					result.Retention.Status != "not_requested" || result.ModelRoute.Provider != "copilot" {
					t.Fatalf("result=%+v err=%v clients=%d sends=%d", result, err, provider.clients, provider.sends)
				}
				b, err := json.Marshal(result)
				if err != nil || strings.Contains(string(b)+diagnostics.String(), "PRIVATE") {
					t.Fatalf("private content in result: %s / %v", b, err)
				}
				if strings.Count(diagnostics.String(), " started:") != sends ||
					strings.Count(diagnostics.String(), " finished:") != sends ||
					strings.Count(diagnostics.String(), "HTTP=200") != sends ||
					!strings.Contains(diagnostics.String(), "endpoint="+modelaccess.CopilotEndpoint) {
					t.Fatalf("missing Copilot diagnostics: %s", diagnostics.String())
				}
			})
		}
	}
}

func TestDirectPreviewCopilotFailuresStaySafeAndStopSends(t *testing.T) {
	for _, failure := range []struct {
		err  error
		code string
	}{
		{copilot.ErrAuthRequired, "copilot_auth_required"},
		{copilot.ErrAuthRevoked, "copilot_auth_revoked"},
		{copilot.ErrSubscription, "copilot_no_subscription"},
		{copilot.ErrPolicy, "copilot_policy_denied"},
		{copilot.ErrQuota, "copilot_quota"},
		{copilot.ErrModelUnavailable, "copilot_model_unavailable"},
		{copilot.ErrMalformed, "copilot_malformed"},
		{copilot.ErrKeychain, "copilot_keychain_unavailable"},
		{copilot.ErrUnavailable, "copilot_unavailable"},
	} {
		for _, phase := range []string{"catalog", "completion", "judge"} {
			t.Run(failure.code+"/"+phase, func(t *testing.T) {
				wrapped := fmt.Errorf("PRIVATE CREDENTIAL AND PROVIDER BODY: %w", failure.err)
				provider := &previewCopilotAccess{t: t}
				if phase == "catalog" {
					provider.clientErr = wrapped
				} else {
					provider.sendErr = wrapped
					if phase == "judge" {
						provider.sendErrAt = 2
					}
				}
				opt := directOptions(t, "gpt-test", phase == "judge")
				var diagnostics strings.Builder
				opt.Progress = func(message string) error {
					diagnostics.WriteString(message + "\n")
					return nil
				}
				route := copilotPreviewRoute()
				opt.Route = &route
				result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
				wantSends := map[string]int{"catalog": 0, "completion": 1, "judge": 2}[phase]
				if err == nil || result.FullPass || result.Verdict != runtime.EvaluationUnknown ||
					result.ReasonCode != failure.code || provider.sends != wantSends || provider.clients != 1 {
					t.Fatalf("result=%+v err=%v clients=%d sends=%d", result, err, provider.clients, provider.sends)
				}
				b, _ := json.Marshal(result)
				if strings.Contains(string(b)+err.Error()+diagnostics.String(), "PRIVATE") {
					t.Fatal("raw transport error disclosed")
				}
				if phase != "catalog" && !strings.Contains(diagnostics.String(), "finished: "+failure.code) {
					t.Fatalf("missing safe Copilot failure category: %s", diagnostics.String())
				}
			})
		}
	}
}

func TestDirectPreviewCopilotJudgeDiagnosticFailureStopsSends(t *testing.T) {
	provider := &previewCopilotAccess{t: t}
	opt := directOptions(t, "gpt-test", true)
	route := copilotPreviewRoute()
	opt.Route = &route
	opt.Progress = func(message string) error {
		if strings.HasPrefix(message, "Judge ") && strings.Contains(message, " finished:") {
			return errors.New("PRIVATE DIAGNOSTIC ERROR")
		}
		return nil
	}
	result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
	if err == nil || result.FullPass || result.ReasonCode != "diagnostics_failed" || provider.sends != 2 {
		t.Fatalf("judge diagnostic failure continued: %+v / %v sends=%d", result, err, provider.sends)
	}
}

func TestDirectPreviewCopilotJudgeTransportFailuresStopSends(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"cancelled", context.Canceled, "cancelled"},
		{"timeout", context.DeadlineExceeded, "deadline_exceeded"},
		{"unknown", errors.New("PRIVATE TRANSPORT ERROR"), "execution_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &previewCopilotAccess{t: t, sendErr: tc.err, sendErrAt: 2}
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
			if err == nil || result.FullPass || result.ReasonCode != tc.code || provider.sends != 2 {
				t.Fatalf("judge transport failure continued: %+v / %v sends=%d", result, err, provider.sends)
			}
		})
	}
}

func TestDirectPreviewCopilotNilTransportRefuses(t *testing.T) {
	provider := &previewCopilotAccess{t: t, nilTransport: true}
	opt := directOptions(t, "gpt-test", false)
	route := copilotPreviewRoute()
	opt.Route = &route
	result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
	if err == nil || result.ReasonCode != "copilot_unavailable" || provider.sends != 0 {
		t.Fatalf("nil transport accepted: %+v / %v", result, err)
	}
}

func TestDirectPreviewLocalRoutesNeverAccessCopilot(t *testing.T) {
	for _, model := range []string{"fake", "unit"} {
		t.Run(model, func(t *testing.T) {
			provider := &previewCopilotAccess{t: t}
			opt := directOptions(t, model, false)
			if model != "fake" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
				}))
				defer server.Close()
				opt.Route = &modelaccess.Route{Provider: "compatible", Model: model, Endpoint: server.URL, Auth: "none"}
			}
			result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
			if err != nil || !result.FullPass || provider.clients != 0 || provider.sends != 0 {
				t.Fatalf("local route changed: %+v / %v", result, err)
			}
		})
	}
}

func TestDirectPreviewCopilotPreflightIsInert(t *testing.T) {
	for _, mode := range []string{"missing_provider", "wrong_endpoint", "wrong_auth", "wrong_model", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			provider := &previewCopilotAccess{t: t}
			opt := directOptions(t, "gpt-test", false)
			route := copilotPreviewRoute()
			opt.Route = &route
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "missing_provider":
				opt.Route = nil
			case "wrong_endpoint":
				route.Endpoint = "https://private.invalid"
			case "wrong_auth":
				route.Auth = "none"
			case "wrong_model":
				route.Model = "another"
			case "cancelled":
				cancel()
			}
			result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(ctx, opt)
			if err == nil || result.FullPass || provider.clients != 0 || provider.sends != 0 {
				t.Fatalf("refusal had effects: %+v / %v clients=%d sends=%d", result, err, provider.clients, provider.sends)
			}
		})
	}
}

func TestDirectPreviewCopilotPreservesCancellationAndModelIdentity(t *testing.T) {
	for _, mode := range []string{"cancelled", "timeout", "model_mismatch", "unknown_error", "subset"} {
		t.Run(mode, func(t *testing.T) {
			provider := &previewCopilotAccess{t: t}
			opt := directOptions(t, "gpt-test", false)
			route := copilotPreviewRoute()
			opt.Route = &route
			wantCode, wantSends := "execution_failed", 1
			switch mode {
			case "cancelled":
				provider.sendErr, wantCode = context.Canceled, "cancelled"
			case "timeout":
				provider.sendErr, wantCode = context.DeadlineExceeded, "deadline_exceeded"
			case "model_mismatch":
				provider.returnModel = "gpt-other"
			case "unknown_error":
				provider.sendErr = errors.New("PRIVATE TRANSPORT ERROR")
			case "subset":
				opt.CaseID, wantCode = "first", "checks_passed"
			}
			result, err := (&App{FileRunner: &directFileRunner{copilot: provider}}).PreviewAgentFile(t.Context(), opt)
			if result.ReasonCode != wantCode || result.FullPass || provider.sends != wantSends || (err == nil) != (mode == "subset") {
				t.Fatalf("result=%+v err=%v sends=%d", result, err, provider.sends)
			}
		})
	}
}
