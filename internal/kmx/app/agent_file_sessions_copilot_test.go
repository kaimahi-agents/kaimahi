package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type journalCopilotAccess struct {
	sends      int
	failAt     int
	setupErr   error
	reply      string
	checkCalls int
	beforeSend func(*http.Request) error
}

func (p *journalCopilotAccess) PreviewClient(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
	return p, p.setupErr
}
func (p *journalCopilotAccess) CheckContent(fields ...string) error {
	p.checkCalls++
	for _, field := range fields {
		if strings.Contains(field, "opaquecredential") {
			return copilot.ErrCredentialContent
		}
	}
	return nil
}
func (p *journalCopilotAccess) Validate(ctx context.Context) error                  { return ctx.Err() }
func (p *journalCopilotAccess) Client(modelaccess.Route) (http.RoundTripper, error) { return p, nil }
func (p *journalCopilotAccess) RoundTrip(req *http.Request) (*http.Response, error) {
	p.sends++
	if p.beforeSend != nil {
		if err := p.beforeSend(req); err != nil {
			return nil, err
		}
	}
	if req.Header.Get("Authorization") != "" {
		panic("credential injected by runner")
	}
	if p.failAt == p.sends {
		return nil, copilot.ErrAuthRevoked
	}
	var body directChat
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	answer := "Hello, PRIVATE INPUT"
	if body.Messages[0].Content == runtime.EvaluationJudgePolicy {
		answer = `{"score":95,"reasoning":"good"}`
	}
	if p.reply != "" {
		answer = p.reply
	}
	w := httptest.NewRecorder()
	previewEnvelope(w, body.Model, answer)
	return w.Result(), nil
}

func TestSessionsCopilotAgentJudgeAndMixedRoutes(t *testing.T) {
	for _, selection := range []string{"copilot", "copilot-judge", "local-judge"} {
		t.Run(selection, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			provider := &journalCopilotAccess{}
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			if selection == "copilot-judge" {
				opt = directOptions(t, "fake", true)
				opt.JudgeRoute = &route
			} else if selection == "local-judge" {
				opt.JudgeRoute = &modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}
			}
			parsed, err := agentfile.Load(opt.File)
			if err != nil {
				t.Fatal(err)
			}
			if opt.JudgeRoute != nil {
				for i := range parsed.Examples {
					for j := range parsed.Examples[i].Assertions {
						parsed.Examples[i].Assertions[j].JudgeModel = opt.JudgeRoute.Model
					}
				}
			}
			var diagnostics strings.Builder
			opt.Progress = func(s string) error { diagnostics.WriteString(s); return nil }
			result, err := (&App{FileRunner: newLocalFileRunner(provider.PreviewClient)}).PreviewAgentSnapshot(t.Context(), parsed, opt)
			want := 6
			if selection == "copilot-judge" {
				want = 4
			}
			if selection == "local-judge" {
				want = 2
			}
			if err != nil || !result.FullPass || strings.Contains(result.Runner, "no journal") || provider.sends != want {
				t.Fatalf("%+v / %v sends=%d", result, err, provider.sends)
			}
			dir, _ := config.LocalPreviewJournalDir()
			ids, _ := localpreview.ListRuns(dir)
			if len(ids) != 1 {
				t.Fatal(ids)
			}
			replay, err := localpreview.Inspect(t.Context(), dir, ids[0])
			if err != nil || len(replay.Sessions) != 6 || replay.ModelCalls != 0 || provider.sends != want {
				t.Fatalf("replay %+v %v sends=%d", replay, err, provider.sends)
			}
			if provider.checkCalls == 0 {
				t.Fatal("provider-owned preflight not used")
			}
		})
	}
}

func TestSessionsCopilotCredentialRefusalWithholdsPrefilledAppMetadata(t *testing.T) {
	for _, field := range []string{"identity", "model", "input", "rubric", "file", "assertion", "setup-error"} {
		t.Run(field, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KMX_HOME", home)
			provider := &journalCopilotAccess{}
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			parsed, err := agentfile.Load(opt.File)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "identity":
				parsed.Identity.ID = "opaquecredential"
			case "model", "setup-error":
				parsed.Model = "opaquecredential"
				opt.Route.Model = parsed.Model
				for i := range parsed.Examples {
					parsed.Examples[i].Assertions[0].JudgeModel = parsed.Model
				}
				if field == "setup-error" {
					provider.setupErr = copilot.ErrModelUnavailable
				}
			case "input":
				parsed.Examples[0].Input = "opaquecredential"
			case "rubric":
				parsed.Examples[0].Assertions[0].Rubric = "opaquecredential"
			case "file":
				opt.File = "opaquecredential.agent.md"
			case "assertion":
				parsed.Examples[0].Assertions[0].ID = "opaquecredential"
			}
			result, err := (&App{FileRunner: newLocalFileRunner(provider.PreviewClient)}).PreviewAgentSnapshot(t.Context(), parsed, opt)
			data, _ := json.Marshal(result)
			if err == nil || provider.sends != 0 || strings.Contains(string(data)+err.Error(), "opaquecredential") || len(result.Examples) != 0 {
				t.Fatalf("credential escaped: %s / %v sends=%d", data, err, provider.sends)
			}
			if _, err := os.Stat(filepath.Join(home, "local-preview")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal touched before preflight: %v", err)
			}
		})
	}
}

func TestSessionsCopilotFailureAbortsJudgingAndLaterSends(t *testing.T) {
	for _, at := range []int{1, 2, 3} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			p := &journalCopilotAccess{failAt: at}
			opt := directOptions(t, "gpt-test", true)
			route := copilotPreviewRoute()
			opt.Route = &route
			result, err := (&App{FileRunner: newLocalFileRunner(p.PreviewClient)}).PreviewAgentFile(t.Context(), opt)
			if err == nil || result.ReasonCode != "copilot_auth_revoked" || p.sends != at {
				t.Fatalf("%+v / %v sends=%d", result, err, p.sends)
			}
		})
	}
}
