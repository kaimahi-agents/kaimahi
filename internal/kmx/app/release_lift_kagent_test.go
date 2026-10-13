package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Real evaluation constructor and immutable layout; lift needs no source tree.
func releaseKagentFixture(t *testing.T) (*App, ReleaseLiftOptions, string, *App) {
	t.Helper()
	source, _, _, _ := suiteKagentFixture(t, "")
	eo := suiteKagentEvaluationOptions(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), eo)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
	if err := source.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	opt := ReleaseLiftOptions{Reference: "suite@" + p.suite.Digest, FromLayout: p.prepared.Path, ToContext: eo.ToContext, Evidence: eo.Out}
	opt.Bindings = eo.Bindings
	if err := os.RemoveAll(eo.Workspace); err != nil {
		t.Fatal(err)
	}
	destination, _, _, dir := suiteKagentFixture(t, "")
	return destination, opt, dir, source
}

func rewriteReleaseKagentEvidence(t *testing.T, opt ReleaseLiftOptions, change func(*suitePassingReceipt)) {
	t.Helper()
	r, err := readSuitePassingReceipt(t.Context(), opt.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	change(r)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opt.Evidence, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseLiftKagentFinalFullSourceImages(t *testing.T) {
	a, opt, dir, _ := releaseKagentFixture(t)
	// Both index and platform are qualified pins: qualification alone is not
	// equality with the literal complete source execution inventory.
	rewriteReleaseKagentEvidence(t, opt, func(r *suitePassingReceipt) { r.Kagent.Observation.Images[0].Digest = kagentLiftProfile().GoDigests[0] })
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal("valid source inventory must plan", err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !errors.Is(err, ErrReleaseLiftApply) || result.State != "partial" || result.Resources[1].Ready || result.Resources[1].UID == "" || result.Resources[1].SpecDigest == "" {
		t.Fatalf("source image mismatch invented success: %+v %v", result.Resources, err)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig", "Agent")
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatal("partial gained authority", err)
	}
}

func TestReleaseLiftKagentSourceFreeAndOpaque(t *testing.T) {
	a, opt, dir, _ := releaseKagentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal("valid typed kagent receipt must plan", err)
	}
	s := p.Summary()
	if s.Runtime != "kagent" || s.Gate != "same-subject" || len(s.Resources) != 2 || s.Resources[0].Kind != "ModelConfig" || s.Resources[1].Kind != "Agent" {
		t.Fatalf("wrong review: %+v", s)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
	result, err := a.ApplyReleaseLift(t.Context(), p, s.PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("create-only lift: %s %v", result.State, err)
	}
	for _, row := range result.Resources {
		if row.Outcome != "created" || !row.Ready || row.UID == "" || row.SpecDigest == "" {
			t.Fatalf("wrong outcome: %+v", row)
		}
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig", "Agent")
	if len(evaluationSends(t, dir)) != 0 {
		t.Fatal("lift sent Task")
	}
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	result.Summary.Target.Context = "foreign"
	result.Resources[0].UID = "foreign"
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
		t.Fatal("public edits rewrote private proof", err)
	}
	raw, _ := json.Marshal(result)
	var serialized ReleaseLiftResult
	_ = json.Unmarshal(raw, &serialized)
	if err := a.VerifyReleaseLiftPlacement(t.Context(), serialized); !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatalf("JSON gained authority: %v", err)
	}
	if _, err := a.ApplyReleaseLift(context.Background(), p, s.PlanDigest); !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatalf("reapplied: %v", err)
	}
}

func TestReleaseLiftKagentOccupiedAndUnknown(t *testing.T) {
	for _, mode := range []string{"exact evaluated pair", "ModelConfig", "Agent", "child", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, dir, _ := releaseKagentFixture(t)
			if mode == "exact evaluated pair" {
				p, err := a.PrepareReleaseLift(t.Context(), opt)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
					t.Fatal(err)
				}
			} else {
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					primary := map[string]string{"ModelConfig": "modelconfigs.kagent.dev", "Agent": "agents.kagent.dev", "unreadable": "modelconfigs.kagent.dev"}[mode]
					get := slices.Index(cmd.Args, "get")
					if primary != "" && get >= 0 && get+2 < len(cmd.Args) && cmd.Args[get+1] == primary && cmd.Args[get+2] == "sample" {
						if mode == "unreadable" {
							return errors.New("private payload canary")
						}
						kind := mode
						_, err := cmd.Stdout.Write([]byte(`{"apiVersion":"kagent.dev/v1alpha2","kind":"` + kind + `","metadata":{"name":"sample","namespace":"agents","uid":"occupied","generation":1}}`))
						return err
					}
					if mode == "child" && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "secrets") && slices.Contains(cmd.Args, "sample") {
						_, err := cmd.Stdout.Write([]byte("present"))
						return err
					}
					return capture(ctx, cmd)
				}
			}
			before := len(kagentCalls(t, dir))
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if p != nil || !errors.Is(err, ErrReleaseLift) || errors.Is(err, errSuiteKagentOccupied) == (mode == "unreadable") || strings.Contains(err.Error(), "private payload") {
				t.Fatalf("wrong absence classification: %v", err)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestReleaseLiftKagentCheckpointDrift(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary int
	}{{"namespace", 0}, {"namespace", 1}, {"namespace", 2}, {"namespace", 3}, {"namespace", 4}, {"binding bytes", 1}, {"binding identity", 1}, {"evidence", 1}, {"artifact", 1}, {"late child", 1}, {"late Agent", 1}, {"secret", 1}, {"runtime", 1}} {
		t.Run(tc.name+string(rune('0'+tc.boundary)), func(t *testing.T) {
			a, opt, dir, _ := releaseKagentFixture(t)
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			effects := 0
			changed := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if effects >= tc.boundary {
					if tc.name == "namespace" && slices.Contains(cmd.Args, "namespace") && slices.Contains(cmd.Args, "agents") {
						changed = true
						_, err := cmd.Stdout.Write([]byte(`{"kind":"Namespace","metadata":{"name":"agents","uid":"replacement"}}`))
						return err
					}
					get := slices.Index(cmd.Args, "get")
					if tc.name == "late Agent" && get >= 0 && get+2 < len(cmd.Args) && cmd.Args[get+1] == "agents.kagent.dev" && cmd.Args[get+2] == "sample" {
						changed = true
						_, err := cmd.Stdout.Write([]byte(`{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","metadata":{"name":"sample","namespace":"agents","uid":"occupied","generation":1}}`))
						return err
					}
					if tc.name == "secret" && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "secret") && slices.Contains(cmd.Args, "model-key") {
						changed = true
						_, err := cmd.Stdout.Write([]byte("Secret\nmodel-key\nagents\nkey-uid\nchanged\nactive\npresent"))
						return err
					}
					if tc.name == "late child" && slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "secrets") && slices.Contains(cmd.Args, "sample") {
						changed = true
						_, err := cmd.Stdout.Write([]byte("present"))
						return err
					}
				}
				effect := slices.Contains(cmd.Args, "create") && slices.Contains(cmd.Args, "-f")
				err := capture(ctx, cmd)
				if effect {
					effects++
					if effects == tc.boundary && !changed {
						switch tc.name {
						case "binding bytes":
							raw, e := os.ReadFile(opt.Bindings)
							if e != nil {
								t.Fatal(e)
							}
							if e := os.WriteFile(opt.Bindings, append(raw, []byte("# drift\n")...), 0600); e != nil {
								t.Fatal(e)
							}
							changed = true
						case "binding identity":
							raw, e := os.ReadFile(opt.Bindings)
							if e != nil {
								t.Fatal(e)
							}
							if e := os.WriteFile(opt.Bindings+".next", raw, 0600); e != nil {
								t.Fatal(e)
							}
							if e := os.Rename(opt.Bindings+".next", opt.Bindings); e != nil {
								t.Fatal(e)
							}
							changed = true
						case "evidence":
							if e := os.WriteFile(opt.Evidence, []byte("{}"), 0600); e != nil {
								t.Fatal(e)
							}
							changed = true
						case "artifact":
							_, digest, _ := strings.Cut(opt.Reference, "@")
							file := filepath.Join(opt.FromLayout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
							if e := os.Chmod(file, 0600); e != nil {
								t.Fatal(e)
							}
							if e := os.WriteFile(file, []byte("{}"), 0600); e != nil {
								t.Fatal(e)
							}
							changed = true
						case "secret":
							t.Setenv("KMX_KAGENT_TEST_SCENARIO", "dependency-drift")
							changed = true
						case "runtime":
							t.Setenv("KMX_KAGENT_TEST_SCENARIO", "wrong-image-config")
							changed = true
						}
					}
				}
				return err
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if !changed || !errors.Is(err, ErrReleaseLiftApply) || effects != tc.boundary {
				t.Fatalf("checkpoint allowed later effect: changed=%t effects=%d want=%d err=%v", changed, effects, tc.boundary, err)
			}
			if (tc.name == "late Agent" || tc.name == "late child") && !errors.Is(err, errSuiteKagentOccupied) {
				t.Fatalf("late occupied reason lost: %v", err)
			}
			state := "not-applied"
			if tc.boundary > 2 {
				state = "partial"
			}
			if result.State != state {
				t.Fatalf("wrong partial state %s want %s", result.State, state)
			}
			if len(evaluationSends(t, dir)) != 0 {
				t.Fatal("drift triggered Task")
			}
		})
	}
}

func TestReleaseLiftKagentPartialAndAdmissionProof(t *testing.T) {
	for _, mode := range []string{"admission mutation", "admission UID", "create mutation", "second ambiguity", "wait cancelled", "guard refusal"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, dir, _ := releaseKagentFixture(t)
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			capture := a.captureCommand
			effects := 0
			a.captureCommand = func(c context.Context, cmd *exec.Cmd) error {
				effect := slices.Contains(cmd.Args, "create") && slices.Contains(cmd.Args, "-f")
				admission := slices.Contains(cmd.Args, "--dry-run=server")
				if mode == "guard refusal" && slices.Contains(cmd.Args, "config") {
					return errors.New("guard unavailable")
				}
				if effect {
					effects++
				}
				if mode == "second ambiguity" && effect && !admission && effects == 4 {
					return errors.New("private payload canary")
				}
				var buf bytes.Buffer
				dst := cmd.Stdout
				cmd.Stdout = &buf
				e := capture(c, cmd)
				cmd.Stdout = dst
				if e != nil {
					return e
				}
				raw := buf.Bytes()
				if effect {
					var doc map[string]any
					if e := json.Unmarshal(raw, &doc); e != nil {
						return e
					}
					if mode == "admission mutation" && admission && effects == 2 || mode == "create mutation" && !admission && effects == 4 {
						doc["spec"].(map[string]any)["description"] = "changed"
					}
					if mode == "admission UID" && admission {
						doc["metadata"].(map[string]any)["uid"] = "fake-admission-uid"
						doc["metadata"].(map[string]any)["generation"] = 99
					}
					raw, e = json.Marshal(doc)
					if e != nil {
						return e
					}
					if mode == "wait cancelled" && !admission {
						cancel()
					}
				}
				_, e = dst.Write(raw)
				return e
			}
			result, err := a.ApplyReleaseLift(ctx, p, p.Summary().PlanDigest)
			if mode == "admission UID" {
				if err != nil || result.State != "ready" || result.Resources[0].UID != "modelconfig-uid" || result.Resources[0].Generation != 1 {
					t.Fatalf("admission metadata became authority: %+v %v", result.Resources, err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "private payload") {
				t.Fatalf("failed proof authorized/leaked: %v", err)
			}
			if mode == "guard refusal" || mode == "admission mutation" {
				if result.State != "not-applied" {
					t.Fatal("preflight attempted write")
				}
				assertKagentWriteKinds(t, kagentCalls(t, dir))
			} else {
				if !errors.Is(err, ErrReleaseLiftApply) || result.State != "partial" || result.Resources[0].UID != "modelconfig-uid" {
					t.Fatalf("ack lost: %+v %v", result.Resources, err)
				}
				if mode == "create mutation" && (result.Resources[1].UID != "agent-uid" || result.Resources[1].SpecDigest != "" || result.Resources[1].Ready) {
					t.Fatalf("invalid spec credited: %+v", result.Resources[1])
				}
				if mode == "second ambiguity" && (result.Resources[1].Outcome != "unknown" || result.Resources[1].UID != "" || !result.Resources[0].Ready) {
					t.Fatalf("ambiguous identity invented: %+v", result.Resources)
				}
				if mode == "wait cancelled" && (!errors.Is(err, context.Canceled) || result.Resources[0].Ready || result.Resources[1].Outcome != "not-attempted") {
					t.Fatalf("wait cancellation lost: %+v %v", result.Resources, err)
				}
			}
			before := len(kagentCalls(t, dir))
			if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); !errors.Is(err, ErrReleaseLiftApply) {
				t.Fatal("valid start not consumed", err)
			}
			if len(kagentCalls(t, dir)) != before {
				t.Fatal("consumed plan performed effects")
			}
		})
	}
}

func TestReleaseLiftKagentConsumptionAndVerifyDrift(t *testing.T) {
	a, opt, _, _ := releaseKagentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, "wrong digest"); !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.ApplyReleaseLift(cancelled, p, p.Summary().PlanDigest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	results := make(chan ReleaseLiftResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	success := 0
	var result ReleaseLiftResult
	for e := range errs {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrReleaseLiftApply) {
			t.Fatal(e)
		}
	}
	for r := range results {
		if r.State == "ready" {
			result = r
		}
	}
	if success != 1 {
		t.Fatalf("atomic consumption successful calls %d", success)
	}
	for _, scenario := range []string{"dependency-drift", "wrong-agent-imageid", "wrong-image-config", "trusted-proxy", "final-agent-notready"} {
		t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
		if err := a.VerifyReleaseLiftPlacement(t.Context(), result); !errors.Is(err, ErrReleaseLiftApply) {
			t.Fatalf("Verify accepted %s: %v", scenario, err)
		}
	}
}

func TestReleaseLiftKagentPromotionWhitelist(t *testing.T) {
	a, opt, _, _ := releaseKagentFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	source := p.kagent.prepared.subject
	clone := func() suiteKagentSubject {
		raw, e := json.Marshal(source)
		if e != nil {
			t.Fatal(e)
		}
		var s suiteKagentSubject
		if e := json.Unmarshal(raw, &s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	for _, tc := range []struct {
		name     string
		physical bool
		change   func(*suiteKagentSubject)
	}{
		{"target", true, func(s *suiteKagentSubject) {
			s.Target = suiteNativeTarget{Context: "other", ClusterUID: "other", Namespace: "other", NamespaceUID: "other"}
		}},
		{"binding namespace", true, func(s *suiteKagentSubject) { s.Binding.Namespace = "other" }},
		{"endpoint", true, func(s *suiteKagentSubject) { s.Binding.ModelConfig.BaseURL = "https://other.invalid" }},
		{"secret reference", true, func(s *suiteKagentSubject) {
			s.Binding.ModelConfig.SecretRef.Name = "other"
			s.Binding.ModelConfig.SecretRef.Key = "other"
		}},
		{"secret identity", true, func(s *suiteKagentSubject) {
			s.Secret.Name = "other"
			s.Secret.Namespace = "other"
			s.Secret.UID = "other"
			s.Secret.ResourceVersion = "other"
			s.Secret.Key = "other"
		}},
		{"render identities", true, func(s *suiteKagentSubject) {
			s.BindingDigest = "other"
			s.PayloadDigest = "other"
			s.RenderedDigest = "other"
		}},
		{"runtime physical", true, func(s *suiteKagentSubject) {
			r := &s.Runtime
			r.Namespace = "other"
			r.ServiceName = "other"
			r.ServiceUID = "other"
			r.DeploymentName = "other"
			r.DeploymentUID = "other"
			r.DeploymentGeneration = 3
			r.ConfigName = "other"
			r.ConfigUID = "other"
			r.ControllerAccount = "other"
			r.Selector = map[string]string{"other": "other"}
			r.WatchNamespaces = "other"
		}},
		{"binding apiVersion", false, func(s *suiteKagentSubject) { s.Binding.APIVersion = "other" }},
		{"binding kind", false, func(s *suiteKagentSubject) { s.Binding.Kind = "other" }},
		{"provider", false, func(s *suiteKagentSubject) { s.Binding.ModelConfig.Provider = "other" }},
		{"secret kind", false, func(s *suiteKagentSubject) { s.Secret.Kind = "other" }},
		{"secret generation", false, func(s *suiteKagentSubject) { s.Secret.Generation = 3 }},
		{"secret spec", false, func(s *suiteKagentSubject) { s.Secret.SpecDigest = "other" }},
		{"model", false, func(s *suiteKagentSubject) { s.Model = "other" }},
		{"profile", false, func(s *suiteKagentSubject) { s.Profile.Engine = "other" }},
		{"renderer", false, func(s *suiteKagentSubject) { s.Renderer = "other" }},
		{"execution profile", false, func(s *suiteKagentSubject) { s.ExecutionProfile = "other" }},
		{"controller image", false, func(s *suiteKagentSubject) { s.Runtime.ControllerImage = "other" }},
		{"auth mode", false, func(s *suiteKagentSubject) { s.Runtime.AuthMode = "trusted-proxy" }},
		{"go registry", false, func(s *suiteKagentSubject) { s.Runtime.GoRegistry = "other" }},
		{"go repository", false, func(s *suiteKagentSubject) { s.Runtime.GoRepository = "other" }},
		{"go tag", false, func(s *suiteKagentSubject) { s.Runtime.GoTag = "other" }},
		{"full controller inventory", false, func(s *suiteKagentSubject) {
			s.Runtime.ControllerImages = append(s.Runtime.ControllerImages, s.Runtime.ControllerImages[0])
		}},
		{"controller platform", false, func(s *suiteKagentSubject) { s.Runtime.ControllerImages[0].Architecture = "arm64" }},
		{"controller digest", false, func(s *suiteKagentSubject) {
			s.Runtime.ControllerImages[0].Digest = kagentLiftProfile().ControllerDigests[0]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := clone()
			tc.change(&dst)
			gate, model, e := releaseLiftKagentGate(source, dst, true)
			if tc.physical {
				if e != nil || gate != "promotion" || model != source.Model {
					t.Fatalf("approved physical change refused: %v", e)
				}
			} else if !errors.Is(e, ErrReleaseLift) {
				t.Fatalf("semantic drift allowed: %v", e)
			}
			if _, _, e := releaseLiftKagentGate(source, dst, false); !errors.Is(e, ErrReleaseLift) {
				t.Fatal("default allowed changed subject", e)
			}
		})
	}
	// Explicit copy-blank comparisons must leave the original inventory untouched.
	if !reflect.DeepEqual(source, p.kagent.prepared.subject) {
		t.Fatal("policy mutated retained subject")
	}
	rewriteReleaseKagentEvidence(t, opt, func(r *suitePassingReceipt) {
		s := &r.Kagent.Observation.Subject
		s.Target.Context = "other-source"
		s.RenderedDigest = ""
		digest, e := suiteNativeDigest("kmx.native-kagent.revision/v1", *s)
		if e != nil {
			t.Fatal(e)
		}
		s.RenderedDigest = digest
	})
	opt.Promote = true
	promoted, e := a.PrepareReleaseLift(t.Context(), opt)
	if e != nil || promoted.Summary().Gate != "promotion" || promoted.Summary().ServingModel != source.Model {
		t.Fatalf("public promotion plan: %v", e)
	}
	if r, e := a.ApplyReleaseLift(t.Context(), promoted, promoted.Summary().PlanDigest); e != nil || r.State != "ready" {
		t.Fatalf("promotion apply: %s %v", r.State, e)
	}
}
