package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	godigest "github.com/opencontainers/go-digest"
)

func releaseLiftFixture(t *testing.T, edits ...func(string)) (*App, *nativeReadFixture, ReleaseLiftOptions, *suiteNativePrepared) {
	t.Helper()
	root, p, suite, placement, observations := passingFixture(t, edits...)
	a, _, f := nativeFixture(t)
	native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{nativeOwnedDoc(t, native, "Provider"), nativeOwnedDoc(t, native, "Agent")}
	for i, doc := range docs {
		doc["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
		d, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
		if err != nil {
			t.Fatal(err)
		}
		owned := suiteNativeOwnedResource{UID: strings.ToLower(doc["kind"].(string)) + "-uid", Generation: 1, SpecDigest: d}
		if i == 0 {
			placement.Provider = owned
		} else {
			placement.Agent = owned
		}
		// Decode exactly as kubectl JSON to preserve numeric types for metadata checks.
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var live map[string]any
		if err := json.Unmarshal(raw, &live); err != nil {
			t.Fatal(err)
		}
		kind := doc["kind"].(string)
		f.objects[orkaPlural(kind)+"/"+suite.MemberID] = live
	}
	r, err := newSuitePassingReceipt(t.Context(), p, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(dir, "pass.json")
	if err := os.WriteFile(evidence, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	opt := ReleaseLiftOptions{Reference: "suite@" + suite.Digest, FromLayout: p.Path, ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", Evidence: evidence}
	return a, f, opt, native
}

func TestReleaseLiftOrkaLegacySummaryGolden(t *testing.T) {
	a, _, opt, _ := releaseLiftFixture(t)
	opt.ToNamespace = "" // Legacy Orka default remains independent of KG bindings.
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary()
	if s.Target.Namespace != "orka-system" || p.options.ToNamespace != "orka-system" {
		t.Fatal("lost legacy namespace default")
	}
	for _, tc := range []struct {
		value any
		wire  string
	}{
		{ReleaseLiftTarget{"ctx", "cluster", "orka-system", "ns-uid"}, `{"context":"ctx","clusterUID":"cluster","namespace":"orka-system","namespaceUID":"ns-uid"}`},
		{ReleaseLiftResource{"Provider", "greeter", "reuse", "uid", 1, "2", "spec"}, `{"kind":"Provider","name":"greeter","action":"reuse","uid":"uid","generation":1,"resourceVersion":"2","specDigest":"spec"}`},
	} {
		if raw, err := json.Marshal(tc.value); err != nil || string(raw) != tc.wire {
			t.Fatalf("legacy review metadata wire changed: %s %v", raw, err)
		}
	}
	// Exact pre-KG wire projection from a63cb7f0; fixture E run IDs are random.
	// Do not derive the field inventory from the current Summary type.
	legacy := struct {
		Suite          string                `json:"suite"`
		SuiteDigest    string                `json:"suiteDigest"`
		Member         string                `json:"member"`
		Model          string                `json:"model"`
		Runtime        string                `json:"runtime"`
		Target         ReleaseLiftTarget     `json:"target"`
		SourceTarget   ReleaseLiftTarget     `json:"sourceTarget"`
		Promote        bool                  `json:"promote"`
		ServingModel   string                `json:"servingModel,omitempty"`
		Inference      string                `json:"inference"`
		BindingDigest  string                `json:"bindingDigest"`
		RenderedDigest string                `json:"renderedDigest"`
		EvidenceDigest string                `json:"evidenceDigest"`
		EvidenceRunID  string                `json:"evidenceRunID"`
		Gate           string                `json:"gate"`
		Resources      []ReleaseLiftResource `json:"resources"`
		PlanDigest     string                `json:"planDigest"`
	}{s.Suite, s.SuiteDigest, s.Member, s.Model, "orka", s.Target, s.SourceTarget,
		false, "", opt.Inference, s.BindingDigest, s.RenderedDigest, s.EvidenceDigest,
		s.EvidenceRunID, "same-subject", s.Resources, ""}
	// Reviewed bytes include the empty planDigest and capitalized envelope keys.
	review, err := agentsuite.Marshal(struct {
		Domain string
		Plan   any
	}{"kmx.release-lift.plan/v1", legacy})
	if err != nil {
		t.Fatal(err)
	}
	current := s
	current.PlanDigest = ""
	currentReview, err := agentsuite.Marshal(struct {
		Domain string
		Plan   ReleaseLiftSummary
	}{"kmx.release-lift.plan/v1", current})
	if err != nil || !bytes.Equal(currentReview, review) {
		t.Fatalf("legacy canonical review bytes changed: %v", err)
	}
	legacy.PlanDigest = godigest.FromBytes(review).String()
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(s)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("legacy Orka wire/review digest changed: err=%v\ngot %s\nwant %s", err, got, want)
	}
}

func TestPrepareReleaseLiftGetOnlyPlan(t *testing.T) {
	for _, absent := range []string{"", "Provider", "Agent", "both"} {
		t.Run(absent, func(t *testing.T) {
			a, f, opt, native := releaseLiftFixture(t)
			for _, kind := range []string{"Provider", "Agent"} {
				if absent == kind || absent == "both" {
					delete(f.objects, orkaPlural(kind)+"/"+native.Subject.Member)
				}
			}
			plan, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			s := plan.Summary()
			if s.SuiteDigest != native.Subject.SuiteDigest || s.Member != native.Subject.Member || s.Runtime != "orka" || s.Gate != "same-subject" || s.PlanDigest == "" || s.EvidenceDigest == "" || len(s.Resources) != 2 {
				t.Fatalf("incomplete identities: %+v", s)
			}
			for _, r := range s.Resources {
				want := "reuse"
				if absent == r.Kind || absent == "both" {
					want = "create"
				}
				if r.Action != want {
					t.Fatalf("wrong resource decision: %+v", r)
				}
			}
			// nativeFixture rejects every non-GET call, including server dry-runs.
			if f.calls == 0 {
				t.Fatal("destination not observed")
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), nativeCanary) || strings.Contains(string(raw), "systemPrompt") {
				t.Fatal("plan contains payload")
			}
			s.Resources[0].Action = "update"
			if plan.Summary().Resources[0].Action == "update" {
				t.Fatal("summary modified retained review")
			}
		})
	}
}

func TestPrepareReleaseLiftRefusesEvidenceBeforeClusterRead(t *testing.T) {
	for _, tc := range []string{"missing", "bundle", "wrong S", "partial", "tag", "selector", "nil config", "cancel"} {
		t.Run(tc, func(t *testing.T) {
			a, f, opt, _ := releaseLiftFixture(t)
			before := f.calls
			ctx := t.Context()
			switch tc {
			case "missing":
				opt.Evidence += "-missing"
			case "bundle":
				if err := os.WriteFile(opt.Evidence, []byte(`{"schemaVersion":1,"portableDigest":"bundle"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong S", "partial":
				raw, err := os.ReadFile(opt.Evidence)
				if err != nil {
					t.Fatal(err)
				}
				r, err := parseSuitePassingReceipt(raw)
				if err != nil {
					t.Fatal(err)
				}
				if tc == "wrong S" {
					r.Subject.SuiteDigest = "sha256:" + strings.Repeat("a", 64)
				} else {
					r.Cases = r.Cases[:1]
				}
				raw, err = json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(opt.Evidence, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "tag":
				opt.Reference = "suite:v1"
			case "selector":
				opt.Inference = "copilot"
			case "nil config":
				a.Cfg = nil
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			_, err := a.PrepareReleaseLift(ctx, opt)
			if tc == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			} else if !errors.Is(err, ErrReleaseLift) {
				t.Fatalf("wrong refusal: %v", err)
			}
			if f.calls != before {
				t.Fatal("invalid request/evidence reached destination")
			}
		})
	}
}

func TestPrepareReleaseLiftRefusesChangedSubjectAndPlacement(t *testing.T) {
	for _, tc := range []string{"namespace", "provider RV", "secret RV", "runtime", "marker", "spec", "replacement", "generation", "not Ready", "foreign", "unknown", "input drift", "owned Provider spec", "owned Provider marker", "owned Provider replacement"} {
		t.Run(tc, func(t *testing.T) {
			a, f, opt, native := releaseLiftFixture(t)
			agent := f.objects[orkaPlural("Agent")+"/"+native.Subject.Member]
			ownedProvider := f.objects[orkaPlural("Provider")+"/"+native.Subject.Member]
			meta := agent["metadata"].(map[string]any)
			switch tc {
			case "owned Provider spec":
				ownedProvider["spec"].(map[string]any)["defaultModel"] = "changed"
			case "owned Provider marker":
				ownedProvider["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaSuiteMarkers[1]] = "sha256:" + strings.Repeat("a", 64)
			case "owned Provider replacement":
				ownedProvider["metadata"].(map[string]any)["uid"] = "replacement"
			case "namespace":
				f.objects["namespace/orka-system"]["metadata"].(map[string]any)["uid"] = "replacement"
			case "provider RV":
				f.objects["providers.core.orka.ai/inference"]["metadata"].(map[string]any)["resourceVersion"] = "2"
			case "secret RV":
				f.objects["secret/model-secret"]["metadata"].(map[string]any)["resourceVersion"] = "2"
			case "runtime":
				nativeController(f)["metadata"].(map[string]any)["uid"] = "replacement"
			case "marker":
				meta["annotations"].(map[string]any)[orkaSuiteMarkers[1]] = "sha256:" + strings.Repeat("a", 64)
			case "spec":
				agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "changed"}
			case "replacement":
				meta["uid"] = "replacement"
			case "generation":
				meta["generation"] = float64(2)
			case "not Ready":
				agent["status"] = map[string]any{}
			case "foreign":
				delete(meta, "annotations")
			case "unknown":
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == orkaPlural("Agent")+"/"+native.Subject.Member {
						return errors.New(nativeCanary)
					}
					return nil
				}
			case "input drift":
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == orkaPlural("Agent")+"/"+native.Subject.Member {
						f.objects["secret/model-secret"]["metadata"].(map[string]any)["resourceVersion"] = "2"
					}
					return nil
				}
			}
			_, err := a.PrepareReleaseLift(t.Context(), opt)
			if !errors.Is(err, ErrReleaseLift) {
				t.Fatalf("drift authorized: %v", err)
			}
			if strings.Contains(err.Error(), nativeCanary) {
				t.Fatal("untrusted diagnostic exposed")
			}
		})
	}
}

func TestPrepareReleaseLiftDoesNotSubstituteOrkaForKagent(t *testing.T) {
	a, f, opt, _ := releaseLiftFixture(t)
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer briefly.\n")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "exact-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}}); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	opt.Reference, opt.FromLayout = "suite@"+p.Descriptor.Digest.String(), p.Path
	before := f.calls
	if _, err := a.PrepareReleaseLift(t.Context(), opt); !errors.Is(err, ErrReleaseLift) {
		t.Fatalf("Orka evidence authorized kagent: %v", err)
	}
	if f.calls != before {
		t.Fatal("kagent candidate reached Orka destination inspection")
	}
}
