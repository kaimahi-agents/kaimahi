package app

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestReleaseLiftPromotionKeepsFreshPostGateInspection(t *testing.T) {
	a, f, opt, dir := releaseApplyFixture(t, "both")
	nativeMetadata(f.objects["namespace/kube-system"])["uid"] = "destination-cluster"
	nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "destination-namespace"
	f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = "test-model"
	opt.Promote = true
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary().Gate != "promotion" {
		t.Fatal("promotion fixture did not cross a subject")
	}
	capture := a.captureCommand
	triggered, namespaceReads := false, 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "namespace") && slices.Contains(cmd.Args, "orka-system") {
			namespaceReads++
		}
		err := capture(ctx, cmd)
		if err == nil && strings.Contains(strings.Join(cmd.Args, " "), "DefaultModel") {
			triggered = true
			nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed-during-serving-model-read"
		}
		return err
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !triggered || !errors.Is(err, ErrReleaseLiftApply) || result.State != "not-applied" {
		t.Fatalf("promotion lost post-gate fence: %+v err=%v", result, err)
	}
	if namespaceReads != 2 || len(persistentOrkaCalls(t, dir)) != 0 {
		t.Fatalf("promotion must reread after serving-model I/O: reads=%d", namespaceReads)
	}
}

func TestReleaseLiftRecheckObservesSourceAfterSchemaReads(t *testing.T) {
	for _, kind := range []string{"namespace", "Provider", "Secret", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			a, f, opt, _ := releaseApplyFixture(t, "")
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			triggered := false
			f.change = func(key string, _ int, _ map[string]any) error {
				if key == "crd/tasks.core.orka.ai" && !triggered {
					triggered = true
					switch kind {
					case "namespace":
						nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed-during-schema-inspection"
					case "Provider":
						nativeMetadata(f.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "changed"
					case "Secret":
						nativeMetadata(f.objects["secret/model-secret"])["resourceVersion"] = "changed"
					case "runtime":
						nativeContainer(f)["image"] = "changed"
					}
				}
				return nil
			}
			capture := a.captureCommand
			guardReads := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "config") {
					guardReads++
				}
				return capture(ctx, cmd)
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if !triggered || !errors.Is(err, ErrReleaseLiftApply) || result.State != "not-applied" {
				t.Fatalf("schema-window %s change: %+v err=%v triggered=%v", kind, result, err, triggered)
			}
			if guardReads != 0 {
				t.Fatal("source change during schema inspection reached later guard instead of its own fresh recheck")
			}
		})
	}
}

func TestNativePreparationSchemaFailurePrecedesLiveInputs(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "schema error", true: "cancel during schema read"}[cancelled], func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			schemaErr, sourceErr := errors.New("schema unavailable"), errors.New("source unavailable")
			liveRead := false
			f.change = func(key string, _ int, _ map[string]any) error {
				if key == "crd/agents.core.orka.ai" {
					if cancelled {
						cancel()
						return ctx.Err()
					}
					return schemaErr
				}
				if key == "namespace/kube-system" {
					liveRead = true
					return sourceErr
				}
				return nil
			}
			_, err := a.prepareSuiteNative(ctx, suite, OrkaNamespace, "inference")
			// API stderr/errors are intentionally sanitized; only cancellation
			// crosses the boundary as a typed cause.
			if err == nil || cancelled && !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cannot read installed Agent CRD") || liveRead {
				t.Fatalf("schema failure priority/cancellation: err=%v liveRead=%v", err, liveRead)
			}
		})
	}
}

// Every mandatory recheck and final verification still observes the live API.
// The performance budget forbids reading the same native subject twice within
// one same-subject recheck, not adding a cache across those boundaries.
func TestReleaseLiftSameSubjectReadBudget(t *testing.T) {
	a, f, opt, dir := releaseApplyFixture(t, "")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	clusterReads, namespaceReads, agentReads := 0, 0, 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		i := slices.Index(cmd.Args, "get")
		if i >= 0 && i+2 < len(cmd.Args) {
			switch cmd.Args[i+1] + "/" + cmd.Args[i+2] {
			case "namespace/kube-system":
				clusterReads++
			case "namespace/orka-system":
				namespaceReads++
			case "agents.core.orka.ai/greeter":
				agentReads++
			}
		}
		return capture(ctx, cmd)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("Apply %+v %v", result, err)
	}
	if clusterReads != 9 || namespaceReads != 9 || agentReads != 9 {
		t.Fatalf("eight fresh rechecks + final verification budget: cluster=%d namespace=%d Agent=%d; want9 each", clusterReads, namespaceReads, agentReads)
	}
	if len(persistentOrkaCalls(t, dir)) != 0 {
		t.Fatal("reuse mutated resources")
	}
	for _, r := range result.Resources {
		if r.Outcome != "reused" || !r.Ready {
			t.Fatalf("lost reuse readiness: %+v", r)
		}
	}
	// A subsequent invocation revalidation cannot borrow Apply's snapshot.
	nativeContainer(f)["image"] = "changed-after-apply"
	if err := a.VerifyReleaseLiftPlacement(t.Context(), result); !errors.Is(err, ErrReleaseLiftApply) {
		t.Fatalf("final invocation verification reused stale runtime: %v", err)
	}
	if clusterReads != 10 || namespaceReads != 10 {
		t.Fatal("invocation verification did not make fresh identity reads")
	}
}
