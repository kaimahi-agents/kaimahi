package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Removing any fence must expose admission or a new CREATE, not merely change an error string.
func TestCandidateCorePlacementFencesAndPartialACK(t *testing.T) {
	for _, stage := range []string{"pass", "unguarded", "collision", "admission spec", "unknown create", "missing spec", "create marker", "ready UID", "source", "binding", "runtime", "cluster", "namespace"} {
		t.Run(stage, func(t *testing.T) {
			a, p, r, f, reads, _, prepared := candidateCoreSetup(t)
			if stage == "unguarded" {
				a.guarded = false
			}
			if stage == "collision" {
				f.objects["Agent/"+p.Subject.PhysicalMember] = nativeOwnedDoc(t, p, "Agent")
			}
			capture := a.captureCommand
			admissions, postUnknownReads := 0, 0
			unknown := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if unknown && slices.Contains(cmd.Args, "get") {
					postUnknownReads++
				}
				if slices.Contains(cmd.Args, "-f") {
					raw, _ := io.ReadAll(cmd.Stdin)
					cmd.Stdin = bytes.NewReader(raw)
					var doc map[string]any
					_ = json.Unmarshal(raw, &doc)
					dry := slices.Contains(cmd.Args, "--dry-run=server")
					if dry {
						admissions++
					}
					if dry && stage == "admission spec" && doc["kind"] == "Agent" {
						doc["spec"].(map[string]any)["providerRef"] = map[string]any{"name": "foreign"}
						return json.NewEncoder(cmd.Stdout).Encode(doc)
					}
					if !dry {
						rows := r.Resources()
						if len(rows) == 0 || rows[len(rows)-1].UID != "" {
							t.Fatal("CREATE before unknown intent", rows)
						}
						var response bytes.Buffer
						out := cmd.Stdout
						cmd.Stdout = &response
						err := capture(ctx, cmd)
						cmd.Stdout = out
						if err != nil {
							return err
						}
						if stage == "unknown create" {
							unknown = true
							return errors.New(nativeCanary)
						}
						_ = json.Unmarshal(response.Bytes(), &doc)
						switch stage {
						case "missing spec":
							delete(doc, "spec")
						case "create marker":
							delete(nativeMetadata(doc)["annotations"].(map[string]any), suiteCandidateRunMarker)
						case "ready UID":
							nativeMetadata(f.objects["Provider/"+p.Subject.PhysicalMember])["uid"] = "replacement"
						case "source":
							writeFile(t, filepath.Join(prepared.root, "eval/example.yaml"), "id: edited\ninput: edited\nexpectContains: [yes]\n")
						case "binding":
							nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "2"
						case "runtime":
							nativeMetadata(nativeController(reads))["uid"] = "changed"
						case "cluster":
							nativeMetadata(reads.objects["namespace/kube-system"])["uid"] = "changed"
						case "namespace":
							nativeMetadata(reads.objects["namespace/orka-system"])["uid"] = "changed"
						}
						return json.NewEncoder(out).Encode(doc)
					}
				}
				return capture(ctx, cmd)
			}
			placement, err := a.placeSuiteNativeCandidate(t.Context(), p, r)
			if stage == "pass" {
				if err != nil || !r.ownsPlacement(placement) || len(f.creates) != 2 {
					t.Fatal("fresh pair", err, placement)
				}
				snapshot := r.Resources()
				snapshot[0].UID = "changed"
				if r.Resources()[0].UID == "changed" {
					t.Fatal("aliased ledger")
				}
				return
			}
			if err == nil || placement != nil || strings.Contains(err.Error(), nativeCanary) {
				t.Fatal("unsafe placement", err)
			}
			want := 1
			if slices.Contains([]string{"unguarded", "collision", "admission spec"}, stage) {
				want = 0
			}
			if len(f.creates) != want {
				t.Fatal("wrong partial effects", f.creates)
			}
			if stage == "unguarded" && admissions != 0 {
				t.Fatal("unguarded admission")
			}
			rows := r.Resources()
			if want == 1 {
				if len(rows) != 1 || (rows[0].UID == "") != (stage == "unknown create") {
					t.Fatal("ACK lost or invented", rows)
				}
			}
			if stage == "unknown create" && postUnknownReads != 0 {
				t.Fatal("unknown CREATE recovered by name")
			}
			before := len(f.creates)
			if _, err := a.placeSuiteNativeCandidate(t.Context(), p, r); err == nil || len(f.creates) != before {
				t.Fatal("partial run replayed")
			}
		})
	}
}

func TestCandidateCoreTaskRequiresIndependentProvenance(t *testing.T) {
	for _, stage := range []string{"not started", "missing ACK", "UID", "generation", "spec", "name"} {
		t.Run(stage, func(t *testing.T) {
			a, p, r, f, _, eval, prepared := candidateCoreSetup(t)
			placement, err := a.placeSuiteNativeCandidate(t.Context(), p, r)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "not started":
				r.started = false
			case "missing ACK":
				r.acknowledged = nil
			case "UID":
				r.acknowledged[0].UID = "external"
			case "generation":
				r.acknowledged[0].Generation++
			case "spec":
				r.acknowledged[0].SpecDigest = placement.Agent.SpecDigest
			case "name":
				r.acknowledged[0].Name = "greeter"
			}
			if err := a.verifySuiteNative(t.Context(), placement); err != nil {
				t.Fatal("live positive control", err)
			}
			capture := a.captureCommand
			taskCalls := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "-f") {
					taskCalls++
				}
				return capture(ctx, cmd)
			}
			observed, err := a.evaluateSuiteNativeCandidate(cleanupContext(t), placement, prepared.inventory[0], eval.opt.ResultPort, nil, r)
			if err == nil || observed.Verdict == "pass" || taskCalls != 0 || len(f.creates) != 2 || len(r.intents) != 2 {
				t.Fatal("foreign pair reached Task", err, taskCalls)
			}
		})
	}
}

func TestCandidateCoreTaskAttributionAndUnknownIntent(t *testing.T) {
	for _, stage := range []string{"admission marker", "unknown create", "returned spec", "post result marker"} {
		t.Run(stage, func(t *testing.T) {
			a, p, r, f, _, eval, prepared := candidateCoreSetup(t)
			placement, err := a.placeSuiteNativeCandidate(t.Context(), p, r)
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				taskCreate := slices.Contains(cmd.Args, "-f")
				postResult := len(f.creates) == 3 && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "tasks.core.orka.ai")
				if taskCreate || (stage == "post result marker" && postResult) {
					var response bytes.Buffer
					out := cmd.Stdout
					cmd.Stdout = &response
					err := capture(ctx, cmd)
					cmd.Stdout = out
					if err != nil {
						return err
					}
					dry := slices.Contains(cmd.Args, "--dry-run=server")
					if taskCreate && !dry && stage == "unknown create" {
						return errors.New(nativeCanary)
					}
					var doc map[string]any
					_ = json.Unmarshal(response.Bytes(), &doc)
					if stage == "admission marker" || stage == "post result marker" && postResult {
						delete(nativeMetadata(doc)["annotations"].(map[string]any), suiteCandidateRunMarker)
					}
					if !dry && stage == "returned spec" {
						doc["spec"].(map[string]any)["prompt"] = "changed"
					}
					return json.NewEncoder(out).Encode(doc)
				}
				return capture(ctx, cmd)
			}
			observed, err := a.evaluateSuiteNativeCandidate(cleanupContext(t), placement, prepared.inventory[0], eval.opt.ResultPort, nil, r)
			if err == nil || observed.Verdict == "pass" || strings.Contains(err.Error(), nativeCanary) {
				t.Fatal("unsafe Task credit", err)
			}
			rows := r.Resources()
			if stage == "admission marker" {
				if len(rows) != 2 {
					t.Fatal("intent before admission")
				}
			} else if len(rows) != 3 || (rows[2].UID == "") != (stage == "unknown create") {
				t.Fatal("partial Task ACK", rows)
			}
		})
	}
}
