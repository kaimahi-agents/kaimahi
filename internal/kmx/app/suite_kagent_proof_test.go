package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"testing"
)

func TestSuiteKagentBilateralOwnership(t *testing.T) {
	a, s, b, _ := suiteKagentFixture(t, "")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, kagentVersionMarker} {
		for _, value := range []any{nil, ""} {
			doc, _ := cloneOrkaDoc(p.documents[1])
			doc["metadata"].(map[string]any)["annotations"].(map[string]any)[key] = value
			if _, err := expectedSuiteKagentProof(doc, b.Namespace, suiteKagentOwner(p.subject)); err == nil {
				t.Fatal("legacy reservation authorized suite desired")
			}
			ann, _ := kagentStringMap(doc["metadata"].(map[string]any)["annotations"])
			spec, _ := json.Marshal(p.proofs[1].Spec)
			if err := validateKagentProofSpecAndMarkers(spec, ann, p.proofs[1]); err == nil {
				t.Fatal("legacy reservation authorized suite live")
			}
		}
	}
	for _, key := range orkaSuiteMarkers {
		doc, _ := cloneOrkaDoc(p.documents[1])
		doc["metadata"].(map[string]any)["annotations"].(map[string]any)[key] = "changed"
		if _, err := expectedSuiteKagentProof(doc, b.Namespace, suiteKagentOwner(p.subject)); err == nil {
			t.Fatal("changed suite marker accepted")
		}
	}
}

// Intercept only kubectl output; real schema/image/spec and ownership validators
// remain in the path. This permits identity drift without a second fake engine.
func suiteKagentAlterOutput(capture func(context.Context, *exec.Cmd) error, change func([]string, []byte) []byte) func(context.Context, *exec.Cmd) error {
	return func(ctx context.Context, cmd *exec.Cmd) error {
		out := cmd.Stdout
		var buf bytes.Buffer
		cmd.Stdout = &buf
		err := capture(ctx, cmd)
		cmd.Stdout = out
		if err != nil {
			return err
		}
		_, err = out.Write(change(cmd.Args, buf.Bytes()))
		return err
	}
}

func TestSuiteKagentConsumedDrift(t *testing.T) {
	for _, kind := range []string{"secret", "watch", "controller-image", "generation", "rbac", "node"} {
		t.Run(kind, func(t *testing.T) {
			a, s, b, dir := suiteKagentFixture(t, "")
			p, err := a.prepareSuiteKagent(t.Context(), s, b)
			if err != nil {
				t.Fatal(err)
			}
			p.worker.captureCommand = suiteKagentAlterOutput(p.worker.captureCommand, func(args []string, raw []byte) []byte {
				switch kind {
				case "secret":
					if slices.Contains(args, "model-key") {
						return bytes.ReplaceAll(raw, []byte("key-rv"), []byte("changed-rv"))
					}
				case "watch":
					if slices.Contains(args, "configmap") {
						return bytes.Replace(raw, []byte(`"WATCH_NAMESPACES":""`), []byte(`"WATCH_NAMESPACES":"agents"`), 1)
					}
				case "controller-image":
					if slices.Contains(args, "pods") {
						return bytes.ReplaceAll(raw, []byte(kagentLiftProfile().ControllerDigests[1]), []byte(kagentLiftProfile().ControllerDigests[0]))
					}
				case "generation":
					if slices.Contains(args, "deployments") {
						return bytes.ReplaceAll(raw, []byte(`"generation":1`), []byte(`"generation":2`))
					}
				case "rbac":
					if slices.Contains(args, "auth") {
						return []byte("no")
					}
				case "node":
					if slices.Contains(args, "node") {
						return bytes.ReplaceAll(raw, []byte("amd64"), []byte("arm64"))
					}
				}
				return raw
			})
			before := len(kagentCalls(t, dir))
			got, err := a.placeSuiteKagent(t.Context(), p, nil)
			if err == nil || got.Observation != nil {
				t.Fatal("consumed drift authorized", kind)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestSuiteKagentReuseRequiresCompleteExactProof(t *testing.T) {
	for _, kind := range []string{"resource", "subject", "child", "incomplete", "legacy-live", "suite-live"} {
		t.Run(kind, func(t *testing.T) {
			a, s, b, dir := suiteKagentFixture(t, "")
			p, err := a.prepareSuiteKagent(t.Context(), s, b)
			if err != nil {
				t.Fatal(err)
			}
			placed, err := a.placeSuiteKagent(t.Context(), p, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected := placed.Observation
			switch kind {
			case "resource":
				expected.Resources[1].UID = "replacement"
			case "subject":
				expected.Subject.Binding.ModelConfig.BaseURL = "https://changed.invalid"
			case "child":
				expected.Children[0].UID = "replacement"
			case "incomplete":
				expected.Resources = expected.Resources[:1]
			case "legacy-live", "suite-live":
				p.worker.captureCommand = suiteKagentAlterOutput(p.worker.captureCommand, func(args []string, raw []byte) []byte {
					if !slices.Contains(args, "agents.kagent.dev") || slices.Contains(args, "crd") || !slices.Contains(args, "-o") {
						return raw
					}
					var doc map[string]any
					if json.Unmarshal(raw, &doc) != nil {
						return raw
					}
					meta, _ := doc["metadata"].(map[string]any)
					if meta == nil {
						return raw
					}
					ann := meta["annotations"].(map[string]any)
					if kind == "legacy-live" {
						ann[orkaBundleMarker] = nil
					} else {
						ann[orkaSuiteMarkers[1]] = "changed"
					}
					encoded, _ := json.Marshal(doc)
					return encoded
				})
			}
			before := len(kagentCalls(t, dir))
			if _, err := a.placeSuiteKagent(t.Context(), p, expected); err == nil {
				t.Fatal("invalid retained proof authorized")
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestSuiteKagentFirstAmbiguityStopsAgent(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "unknown-create")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.placeSuiteKagent(t.Context(), p, nil)
	if err == nil || got.Observation != nil || got.Outcomes[0].Outcome != "unknown" || got.Outcomes[0].UID != "" || got.Outcomes[1].Outcome != "not-attempted" {
		t.Fatalf("ambiguous outcome: %+v %v", got, err)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig")
}

func TestSuiteKagentFinalProofFailsWithoutSuccess(t *testing.T) {
	for _, scenario := range []string{"final-agent-notready", "final-model-rejected", "generated-secret-owner-foreign", "workload-owner-foreign", "agent-platform-wrong-imageid"} {
		t.Run(scenario, func(t *testing.T) {
			a, s, b, _ := suiteKagentFixture(t, scenario)
			p, err := a.prepareSuiteKagent(t.Context(), s, b)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.placeSuiteKagent(t.Context(), p, nil)
			if err == nil || got.Observation != nil || got.Outcomes[1].UID != "agent-uid" {
				t.Fatalf("final proof claimed success: %+v %v", got, err)
			}
		})
	}
}

func TestSuiteKagentConsentBeforeAdmission(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "remote-context")
	p, err := a.prepareSuiteKagent(t.Context(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	before := len(kagentCalls(t, dir))
	if _, err := a.placeSuiteKagent(t.Context(), p, nil); err == nil {
		t.Fatal("missing consent authorized placement")
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
}

func TestSuiteKagentPrepareRejectsMidSnapshotImageMutation(t *testing.T) {
	a, s, b, dir := suiteKagentFixture(t, "")
	reads := 0
	a.captureCommand = suiteKagentAlterOutput(a.captureCommand, func(args []string, raw []byte) []byte {
		if slices.Contains(args, "deployments") {
			reads++
			if reads == 2 {
				return bytes.ReplaceAll(raw, []byte("ghcr.io/kagent-dev/kagent/controller:0.10.3"), []byte("example.invalid/foreign:latest"))
			}
		}
		return raw
	})
	if _, err := a.prepareSuiteKagent(t.Context(), s, b); err == nil {
		t.Fatal("mixed qualified image snapshot accepted")
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
}
