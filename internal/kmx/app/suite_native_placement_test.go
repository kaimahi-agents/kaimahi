package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Keep PR2's get-only fixture strict; overlay only pair/admission/guard calls.
func nativePlacementFixture(t *testing.T) (*App, *nativeReadFixture, *suiteNativePrepared, string) {
	t.Helper()
	a, _, f, p := nativePrepared(t)
	reads := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if i := slices.Index(args, "get"); i >= 0 && (args[i+1] == "crd" || f.objects[args[i+1]+"/"+args[i+2]] != nil) {
			return reads(ctx, cmd)
		}
		return reconcileCaptureFixture(ctx, cmd)
	}
	return a, f, p, os.Getenv("KMX_RECONCILE_DIR")
}

func nativeOwnedDoc(t *testing.T, p *suiteNativePrepared, kind string) map[string]any {
	t.Helper()
	desired := p.bundle.Agent
	if kind == "Provider" {
		desired = p.bundle.Provider
	}
	doc, err := cloneOrkaDoc(desired)
	if err != nil {
		t.Fatal(err)
	}
	meta := doc["metadata"].(map[string]any)
	meta["uid"], meta["generation"], meta["resourceVersion"] = strings.ToLower(kind)+"-uid", 1, "1"
	s := p.Subject
	for i, v := range []string{s.Suite, s.SuiteDigest, s.Member, s.Renderer, s.BindingDigest, s.RenderedDigest} {
		reserveSuiteDoc(doc, suiteReservedKeys[i], v)
	}
	return doc
}

func TestSuiteNativeOwnershipClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		want agentruntime.ResourceOutcome
	}{
		{"absent", agentruntime.ResourceCreated}, {"matching", agentruntime.ResourceReused}, {"revision", agentruntime.ResourceUpdated}, {"spec", agentruntime.ResourceUpdated},
		{"unmarked", ""}, {"bundle", ""}, {"mixed", ""}, {"incomplete", ""}, {"foreign", ""}, {"malformed", ""}, {"fraction", ""}, {"overflow", ""}, {"uid", ""}, {"rv", ""}, {"terminating", ""}, {"zero", ""}, {"string generation", ""}, {"max generation", agentruntime.ResourceReused}, {"bad kind", ""}, {"bad namespace", ""}, {"missing spec", ""}, {"invalid spec", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, p, dir := nativePlacementFixture(t)
			live := nativeOwnedDoc(t, p, "Agent")
			ann := nativeMetadata(live)["annotations"].(map[string]any)
			switch tc.name {
			case "unmarked":
				for _, k := range suiteReservedKeys {
					delete(ann, k)
				}
			case "bundle":
				for _, k := range suiteReservedKeys {
					delete(ann, k)
				}
				ann[orkaBundleMarker] = p.Subject.Member
			case "mixed":
				ann[orkaBundleMarker] = nil
			case "incomplete":
				delete(ann, suiteReservedKeys[4])
			case "foreign":
				ann[suiteReservedKeys[2]] = "other"
			case "malformed":
				ann[suiteReservedKeys[1]] = nil
			case "revision":
				ann[suiteReservedKeys[1]] = "sha256:" + strings.Repeat("a", 64)
			case "spec":
				live["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "different"}
			case "fraction":
				nativeMetadata(live)["generation"] = 1.5
			case "overflow":
				nativeMetadata(live)["generation"] = json.Number("9223372036854775808")
			case "uid":
				nativeMetadata(live)["uid"] = ""
			case "rv":
				nativeMetadata(live)["resourceVersion"] = ""
			case "terminating":
				nativeMetadata(live)["deletionTimestamp"] = "now"
			case "zero":
				nativeMetadata(live)["generation"] = 0
			case "string generation":
				nativeMetadata(live)["generation"] = "1"
			case "max generation":
				nativeMetadata(live)["generation"] = json.Number("9223372036854775807")
			case "bad kind":
				live["kind"] = "Provider"
			case "bad namespace":
				nativeMetadata(live)["namespace"] = "other"
			case "missing spec":
				delete(live, "spec")
			case "invalid spec":
				live["spec"] = "not an object"
			}
			if tc.name != "absent" {
				seedReconcile(t, dir, live)
				if tc.name == "bad kind" {
					if err := os.WriteFile(filepath.Join(dir, "agents.core.orka.ai.json"), mustJSON(t, live), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name == "max generation" {
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "agents.core.orka.ai") {
						return json.NewEncoder(cmd.Stdout).Encode(live)
					}
					return capture(ctx, cmd)
				}
			}
			check, err := a.inspectOrkaPlacement(t.Context(), OrkaNamespace, p.bundle.Agent, orkaPlacementIdentity{suite: suiteNativeOwner(p.Subject)})
			if tc.want == "" {
				if err == nil {
					t.Fatal("unsafe ownership accepted")
				}
				return
			}
			if err != nil || check.outcome != tc.want {
				t.Fatalf("outcome %s: %v", check.outcome, err)
			}
			if tc.name == "max generation" && check.id.Generation != math.MaxInt64 {
				t.Fatalf("maximum generation truncated: %d", check.id.Generation)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("inspection wrote")
			}
		})
	}
}

func TestSuiteNativePlacementCreatesReusesAndReevaluates(t *testing.T) {
	a, _, p, dir := nativePlacementFixture(t)
	first, err := a.placeSuiteNative(t.Context(), p)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if first.Provider.UID != "provider-uid" || first.Agent.UID != "agent-uid" || first.Agent.Generation != 1 || first.Agent.SpecDigest == "" {
		t.Fatalf("invalid placement: %+v", first)
	}
	writes := persistentOrkaCalls(t, dir)
	if len(writes) != 2 {
		t.Fatalf("persistent writes=%d", len(writes))
	}
	for _, c := range writes {
		if c.Document == nil || (c.Document["kind"] != "Provider" && c.Document["kind"] != "Agent") {
			t.Fatal("unexpected write", c.Args)
		}
		ann := nativeMetadata(c.Document)["annotations"].(map[string]any)
		for _, k := range suiteReservedKeys {
			if ann[k] == nil {
				t.Fatal("missing suite marker", k)
			}
		}
		for _, k := range []string{orkaBundleMarker, orkaOriginMarker, orkaPortableMarker, orkaRenderedMarker} {
			if _, ok := ann[k]; ok {
				t.Fatal("legacy marker on native resource", k)
			}
		}
	}
	if err := a.verifySuiteNative(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := a.placeSuiteNative(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if len(persistentOrkaCalls(t, dir)) != 2 {
		t.Fatal("matching rerun wrote")
	}
	p.Subject.RenderedDigest = strings.Repeat("a", 64)
	next, err := a.placeSuiteNative(t.Context(), p)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	if len(persistentOrkaCalls(t, dir)) != 4 {
		t.Fatal("revision did not conditionally replace pair")
	}
	if err := a.verifySuiteNative(t.Context(), first); err == nil {
		t.Fatal("old revision remained usable")
	}
}

func TestSuiteNativeAgentPreflightPreventsProviderWrite(t *testing.T) {
	for _, boundary := range []string{"ownership", "admission", "schema"} {
		t.Run(boundary, func(t *testing.T) {
			a, _, p, dir := nativePlacementFixture(t)
			if boundary == "ownership" {
				live := nativeOwnedDoc(t, p, "Agent")
				reserveSuiteDoc(live, suiteReservedKeys[0], "foreign")
				seedReconcile(t, dir, live)
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if boundary == "admission" && slices.Contains(cmd.Args, "--dry-run=server") {
					body, _ := io.ReadAll(cmd.Stdin)
					cmd.Stdin = strings.NewReader(string(body))
					var doc map[string]any
					_ = json.Unmarshal(body, &doc)
					if doc["kind"] == "Agent" {
						return fmt.Errorf("admission unavailable")
					}
				}
				return capture(ctx, cmd)
			}
			if boundary == "schema" {
				p.bundle.Agent["spec"].(map[string]any)["systemPrompt"] = "invalid"
			}
			result, err := a.placeSuiteNative(t.Context(), p)
			if err == nil || result != nil {
				t.Fatalf("preflight accepted %s: %v", boundary, err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("Provider written before Agent preflight")
			}
		})
	}
}

func TestSuiteNativeFinalAdmissionAcceptsServerMetadataAndRefusesMutation(t *testing.T) {
	for _, change := range []string{"server metadata", "ownership", "render annotation", "render label", "spec", "uid", "rv"} {
		t.Run(change, func(t *testing.T) {
			a, _, p, dir := nativePlacementFixture(t)
			live := nativeOwnedDoc(t, p, "Agent")
			live["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "old"}
			seedReconcile(t, dir, live)
			reserveSuiteDoc(p.bundle.Agent, "review.example/note", "reviewed")
			nativeMetadata(p.bundle.Agent)["labels"] = map[string]any{"review.example/label": "reviewed"}
			finalSeen := false
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "replace") && slices.Contains(cmd.Args, "--dry-run=server") {
					raw, err := io.ReadAll(cmd.Stdin)
					if err != nil {
						return err
					}
					cmd.Stdin = strings.NewReader(string(raw))
					var input map[string]any
					if err := json.Unmarshal(raw, &input); err != nil {
						return err
					}
					ann, _ := nativeMetadata(input)["annotations"].(map[string]any)
					if input["kind"] == "Agent" && ann["review.example/note"] == "reviewed" {
						if ann[suiteReservedKeys[5]] != p.Subject.RenderedDigest {
							t.Fatal("final admission request missing native ownership")
						}
						finalSeen = true
						var response strings.Builder
						out := cmd.Stdout
						cmd.Stdout = &response
						err := capture(ctx, cmd)
						cmd.Stdout = out
						if err != nil {
							return err
						}
						var doc map[string]any
						if err := json.Unmarshal([]byte(response.String()), &doc); err != nil {
							return err
						}
						meta := nativeMetadata(doc)
						meta["generation"] = 2
						meta["managedFields"] = []any{map[string]any{"manager": "kubectl-replace", "operation": "Update", "apiVersion": "core.orka.ai/v1alpha1", "fieldsType": "FieldsV1", "fieldsV1": map[string]any{}}}
						switch change {
						case "ownership":
							reserveSuiteDoc(doc, suiteReservedKeys[5], strings.Repeat("f", 64))
						case "render annotation":
							reserveSuiteDoc(doc, "review.example/note", "different")
						case "render label":
							meta["labels"].(map[string]any)["review.example/label"] = "different"
						case "spec":
							doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "different"}
						case "uid":
							meta["uid"] = "different"
						case "rv":
							meta["resourceVersion"] = "different"
						}
						return json.NewEncoder(cmd.Stdout).Encode(doc)
					}
				}
				return capture(ctx, cmd)
			}
			result, err := a.placeSuiteNative(t.Context(), p)
			if !finalSeen {
				t.Fatal("final marked candidate not admitted")
			}
			if change == "server metadata" {
				if err != nil || result == nil {
					t.Fatalf("legitimate server metadata refused: %v", err)
				}
				return
			}
			if err == nil || result != nil || !strings.Contains(err.Error(), "final marked admission changed candidate") {
				t.Fatalf("unexpected admission refusal: %v", err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("write before final admission refusal")
			}
		})
	}
}

func TestSuiteNativeGuardDriftPreventsWrites(t *testing.T) {
	for _, drift := range []string{"input", "pair"} {
		t.Run(drift, func(t *testing.T) {
			a, f, p, dir := nativePlacementFixture(t)
			guardReached := false
			want := "native pair changed after review"
			if drift == "input" {
				want = "native suite target, binding or runtime changed; subject is unknown"
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				err := capture(ctx, cmd)
				if err == nil && slices.Contains(cmd.Args, "config") {
					guardReached = true
					if drift == "input" {
						nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed"
					} else {
						seedReconcile(t, dir, nativeOwnedDoc(t, p, "Agent"))
					}
				}
				return err
			}
			result, err := a.placeSuiteNative(t.Context(), p)
			if !guardReached || err == nil || result != nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("guard drift not exercised or wrong refusal: %v (guard reached=%t)", err, guardReached)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("write after guard drift")
			}
		})
	}
}

func TestSuiteNativeLateFailureReturnsNoPlacementAndExplicitRerunCompletes(t *testing.T) {
	a, _, p, dir := nativePlacementFixture(t)
	t.Setenv("KMX_RECONCILE_FAIL_ONCE", "Agent")
	result, err := a.placeSuiteNative(t.Context(), p)
	if err == nil || result != nil {
		t.Fatal("late failure returned placement")
	}
	if len(persistentOrkaCalls(t, dir)) != 2 {
		t.Fatal("write retried or extra mutation")
	}
	result, err = a.placeSuiteNative(t.Context(), p)
	if err != nil || result == nil {
		t.Fatal(err)
	}
	if len(persistentOrkaCalls(t, dir)) != 3 {
		t.Fatal("explicit rerun rewrote owned Provider")
	}
}

func TestSuiteNativeConcurrentReplaceStopsWithoutResend(t *testing.T) {
	a, _, p, dir := nativePlacementFixture(t)
	seedReconcile(t, dir, nativeOwnedDoc(t, p, "Provider"))
	p.Subject.RenderedDigest = strings.Repeat("b", 64)
	t.Setenv("KMX_RECONCILE_RACE", "Provider")
	result, err := a.placeSuiteNative(t.Context(), p)
	if err == nil || result != nil {
		t.Fatal("conditional conflict accepted")
	}
	if len(persistentOrkaCalls(t, dir)) != 1 {
		t.Fatal("replace retried or Agent written")
	}
}

func TestSuiteNativeFreshRVAfterProviderWaitAllowsOwnedAgentReplace(t *testing.T) {
	a, _, p, dir := nativePlacementFixture(t)
	agent := nativeOwnedDoc(t, p, "Agent")
	reserveSuiteDoc(agent, suiteReservedKeys[5], strings.Repeat("b", 64))
	seedReconcile(t, dir, agent)
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "providers.core.orka.ai") && !slices.Contains(cmd.Args, "--ignore-not-found=true") {
			nativeMetadata(agent)["resourceVersion"] = "status-churn"
			seedReconcile(t, dir, agent)
		}
		return capture(ctx, cmd)
	}
	result, err := a.placeSuiteNative(t.Context(), p)
	if err != nil || result == nil {
		t.Fatal("owned status RV churn refused", err)
	}
	writes := persistentOrkaCalls(t, dir)
	if len(writes) != 2 || nativeMetadata(writes[1].Document)["resourceVersion"] != "status-churn" {
		t.Fatal("replace did not use immediate inspection RV")
	}
}

func TestSuiteNativeLateInputDriftAndReadFailureStopBeforeAgent(t *testing.T) {
	for _, failure := range []string{"input", "read"} {
		t.Run(failure, func(t *testing.T) {
			a, f, p, dir := nativePlacementFixture(t)
			capture := a.captureCommand
			providerWritten := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if providerWritten && failure == "read" && slices.Contains(cmd.Args, "agents.core.orka.ai") {
					return fmt.Errorf("unavailable")
				}
				err := capture(ctx, cmd)
				if slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
					providerWritten = true
					if failure == "input" {
						nativeMetadata(f.objects["secret/model-secret"])["resourceVersion"] = "changed"
					}
				}
				return err
			}
			result, err := a.placeSuiteNative(t.Context(), p)
			if err == nil || result != nil {
				t.Fatal("late unknown earned placement")
			}
			if len(persistentOrkaCalls(t, dir)) != 1 {
				t.Fatal("Agent written or automatic retry")
			}
		})
	}
}

func TestSuiteNativeVerificationRejectsOwnedLeafDrift(t *testing.T) {
	for _, field := range []string{"uid", "generation", "spec", "ownership", "terminating", "ready"} {
		t.Run(field, func(t *testing.T) {
			a, _, p, _ := nativePlacementFixture(t)
			result, err := a.placeSuiteNative(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "providers.core.orka.ai") && slices.Contains(cmd.Args, p.Subject.Member) {
					var b strings.Builder
					out := cmd.Stdout
					cmd.Stdout = &b
					err := capture(ctx, cmd)
					cmd.Stdout = out
					if err != nil {
						return err
					}
					var doc map[string]any
					if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
						return err
					}
					switch field {
					case "uid":
						nativeMetadata(doc)["uid"] = "changed"
					case "generation":
						nativeMetadata(doc)["generation"] = 2
					case "spec":
						doc["spec"].(map[string]any)["defaultModel"] = "changed"
					case "ownership":
						reserveSuiteDoc(doc, suiteReservedKeys[4], strings.Repeat("c", 64))
					case "terminating":
						nativeMetadata(doc)["deletionTimestamp"] = "2026-01-01T00:00:00Z"
					case "ready":
						doc["status"].(map[string]any)["ready"] = false
					}
					return json.NewEncoder(cmd.Stdout).Encode(doc)
				}
				return capture(ctx, cmd)
			}
			if err := a.verifySuiteNative(t.Context(), result); err == nil {
				t.Fatal("owned leaf drift remained usable", field)
			}
		})
	}
}

func TestSuiteNativeVerificationCannotSpliceReadyFromAnotherSnapshot(t *testing.T) {
	a, _, p, _ := nativePlacementFixture(t)
	result, err := a.placeSuiteNative(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	seen := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !seen && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "providers.core.orka.ai") && slices.Contains(cmd.Args, p.Subject.Member) {
			seen = true
			var b strings.Builder
			out := cmd.Stdout
			cmd.Stdout = &b
			err := capture(ctx, cmd)
			cmd.Stdout = out
			if err != nil {
				return err
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
				return err
			}
			doc["status"].(map[string]any)["ready"] = false
			return json.NewEncoder(cmd.Stdout).Encode(doc)
		}
		return capture(ctx, cmd)
	}
	if err := a.verifySuiteNative(t.Context(), result); err == nil {
		t.Fatal("NotReady ownership snapshot combined with later Ready")
	}
}

func TestSuiteNativeProviderRegressionDuringAgentWaitPreventsSuccess(t *testing.T) {
	a, _, p, _ := nativePlacementFixture(t)
	t.Setenv("KMX_RECONCILE_READY_REGRESSION", "1")
	agentWaitReached := false
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		err := capture(ctx, cmd)
		if err == nil && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "agents.core.orka.ai") && !slices.Contains(cmd.Args, "--ignore-not-found=true") {
			agentWaitReached = true
		}
		return err
	}
	result, err := a.placeSuiteNative(t.Context(), p)
	if !agentWaitReached || err == nil || result != nil || !strings.Contains(err.Error(), "Provider/"+p.Subject.Member) || !strings.Contains(err.Error(), "Ready") {
		t.Fatalf("Provider regression not exercised or wrong refusal: %v (Agent wait reached=%t)", err, agentWaitReached)
	}
}
