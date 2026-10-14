package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func suiteKagentFixture(t *testing.T, scenario string) (*App, *workspace.NativeSuite, agentruntime.KagentBindings, string) {
	t.Helper()
	// Keep the existing runtime fixture's fixed sample resource names.
	_, s, _ := nativeFixture(t, workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"})
	a, _, dir := kagentLiftFixture(t, scenario)
	s.MemberID = "sample"
	b := agentruntime.KagentBindings{APIVersion: agentruntime.KagentBindingsAPIVersion, Kind: agentruntime.KagentBindingsKind, Namespace: "agents", ModelConfig: agentruntime.KagentModelConfigBindings{Provider: "openai", SecretRef: agentruntime.KagentSecretRefBindings{Name: "model-key", Key: "api-key"}}}
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "namespace") && slices.Contains(cmd.Args, "agents") {
			_, err := cmd.Stdout.Write([]byte(`{"kind":"Namespace","metadata":{"name":"agents","uid":"namespace-uid"}}`))
			return err
		}
		return kagentCaptureFixture(ctx, cmd)
	}
	return a, s, b, dir
}

func TestSuiteKagentPrepareExactReadOnly(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
	raw, _ := json.Marshal(p.subject)
	if bytes.Contains(raw, s.Instructions) || bytes.Contains(raw, []byte(s.Description)) || bytes.Contains(raw, []byte("secret-hash")) {
		t.Fatal("subject leaked payload")
	}
	model := p.bundle.ModelConfig["spec"].(map[string]any)
	agent := p.bundle.Agent["spec"].(map[string]any)
	if model["model"] != "packaged-model" || agent["description"] != "Public description" || agent["declarative"].(map[string]any)["systemMessage"] != string(s.Instructions) {
		t.Fatal("packaged behavior substituted")
	}
	s.Instructions[0] = 'X'
	b.ModelConfig.SecretRef.Name = "changed"
	if err := p.worker.verifySuiteKagentInputs(t.Context(), p); err != nil {
		t.Fatal("caller mutation rebound frozen inputs", err)
	}
	if p.worker.selectedKagentProfile().Version != "v0.10.3" {
		t.Fatal("historical profile selected")
	}
}

func TestSuiteKagentPreservesEmptyOptionalDescription(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	s.Description = ""
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal("valid optional description refused", err)
	}
	if description, exists := p.bundle.Agent["spec"].(map[string]any)["description"]; exists && description != "" {
		t.Fatal("empty packaged description substituted")
	}
	if err := p.bundle.Validate(); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatal("legacy review-bundle validation was weakened", err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
}

func TestSuiteKagentPrepareRefusesUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name, scenario string
		change         func(*workspace.NativeSuite, *agentruntime.KagentBindings)
	}{
		{"provider", "", func(_ *workspace.NativeSuite, b *agentruntime.KagentBindings) { b.ModelConfig.Provider = "anthropic" }},
		{"profile", "", func(s *workspace.NativeSuite, _ *agentruntime.KagentBindings) { s.Runtime.Version = "v0.10.2" }},
		{"agentsessions", "", func(s *workspace.NativeSuite, _ *agentruntime.KagentBindings) {
			s.Runtime = workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}
		}},
		{"renderer", "", func(s *workspace.NativeSuite, _ *agentruntime.KagentBindings) { s.RendererVersion = "other" }},
		{"images", "", func(s *workspace.NativeSuite, _ *agentruntime.KagentBindings) { s.RequiredImages = []string{"image"} }},
		{"nil-images", "", func(s *workspace.NativeSuite, _ *agentruntime.KagentBindings) { s.RequiredImages = nil }},
		{"arm64", "status-image-config-arm64", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, s, b, dir := suiteKagentFixture(t, tc.scenario)
			if tc.change != nil {
				tc.change(s, &b)
			}
			if _, err := a.prepareSuiteKagent(t.Context(), s, b); err == nil {
				t.Fatal("unsupported preparation accepted")
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
		})
	}
}

func TestSuiteKagentNewReuseOccupied(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := a.placeSuiteKagent(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if placed.Observation == nil || len(placed.Observation.Resources) != 2 || len(placed.Observation.Children) != 4 {
		t.Fatalf("incomplete observation: %+v", placed)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig", "Agent")
	before := len(kagentCalls(t, dir))
	reused, err := a.placeSuiteKagent(t.Context(), p, placed.Observation)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Outcomes[0].Outcome != agentruntime.ResourceReused {
		t.Fatal("not reused")
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
	if _, err := a.placeSuiteKagent(t.Context(), p, nil); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("unrecorded occupancy authorized: %v", err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
}

func TestSuiteKagentPartialStopsWrites(t *testing.T) {
	for _, scenario := range []string{"create-response-mutated"} {
		t.Run(scenario, func(t *testing.T) {
			a, s, b, dir := suiteKagentFixture(t, scenario)
			p, err := a.prepareSuiteKagent(t.Context(), s, b)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.placeSuiteKagent(t.Context(), p, nil)
			if err == nil || got.Observation != nil {
				t.Fatal("failure claimed readiness", err)
			}
			if got.Outcomes[0].UID != "modelconfig-uid" || got.Outcomes[0].Outcome != agentruntime.ResourceCreated {
				t.Fatalf("confirmed identity lost: %+v", got.Outcomes)
			}
			if scenario == "create-response-mutated" && got.Outcomes[1].UID != "agent-uid" {
				t.Fatalf("valid mutated response identity lost: %+v", got.Outcomes)
			}
			calls := kagentCalls(t, dir)
			writes := 0
			for _, c := range calls {
				if slices.Contains(c.Args, "-f") && !slices.Contains(c.Args, "--dry-run=server") {
					writes++
				}
			}
			if writes != 2 {
				t.Fatalf("unexpected write attempts: %d", writes)
			}
		})
	}
}

func TestSuiteKagentDriftBeforeEveryEffect(t *testing.T) {
	for _, boundary := range []int{0, 1, 2, 3, 4} {
		t.Run(string(rune('0'+boundary)), func(t *testing.T) {
			a, s, b, dir := suiteKagentFixture(t, "")
			p, err := a.prepareSuiteKagent(t.Context(), s, b)
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			effects := 0
			p.worker.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "namespace") && slices.Contains(cmd.Args, "agents") && effects >= boundary {
					_, err := cmd.Stdout.Write([]byte(`{"kind":"Namespace","metadata":{"name":"agents","uid":"replacement"}}`))
					return err
				}
				if slices.Contains(cmd.Args, "-f") {
					effects++
				}
				return capture(ctx, cmd)
			}
			got, err := a.placeSuiteKagent(t.Context(), p, nil)
			if err == nil || got.Observation != nil {
				t.Fatal("namespace drift authorized", err)
			}
			if effects != boundary {
				t.Fatalf("effects after drift: %d want %d", effects, boundary)
			}
			if boundary == 0 {
				assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
			}
		})
	}
}

func TestSuiteKagentAdmissionCancellationRetainsReason(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "--dry-run=server") {
			cancel()
			return context.Canceled
		}
		return capture(ctx, cmd)
	}
	p, err := a.prepareSuiteKagent(ctx, s, b)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.placeSuiteKagent(ctx, p, nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "admission request") || result.Observation != nil {
		t.Fatalf("cancellation reason lost: %v", err)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir))
}

func TestSuiteKagentSecondCreateTransportFailure(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	capture := p.worker.captureCommand
	p.worker.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "-f") && !slices.Contains(cmd.Args, "--dry-run=server") {
			var doc map[string]any
			json.NewDecoder(cmd.Stdin).Decode(&doc)
			raw, _ := json.Marshal(doc)
			cmd.Stdin = bytes.NewReader(raw)
			if doc["kind"] == "Agent" {
				return errors.New("private transport canary")
			}
		}
		return capture(ctx, cmd)
	}
	got, err := a.placeSuiteKagent(t.Context(), p, nil)
	if err == nil || strings.Contains(err.Error(), "private transport canary") || got.Outcomes[0].UID != "modelconfig-uid" || got.Outcomes[1].Outcome != "unknown" || got.Observation != nil {
		t.Fatalf("partial failure: %+v %v", got, err)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig")
}
