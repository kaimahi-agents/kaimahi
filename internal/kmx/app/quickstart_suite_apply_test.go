package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quickstartSuiteApplyFixture(t *testing.T) (*App, *preparedQuickstartSuite, *evalFixture) {
	t.Helper()
	a, _, _, f := nativeEvaluationFixture(t)
	a.Cfg.Model = "packaged-model"
	a.quickstartResultPort = f.opt.ResultPort
	for _, kind := range []string{"providers.core.orka.ai", "agents.core.orka.ai"} {
		if err := os.Remove(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), kind+".json")); err != nil {
			t.Fatal(err)
		}
	}
	f.answers["What is 2+2?"] = "4"
	f.answers["A separate first question"] = "A fresh answer"
	p, err := a.prepareQuickstartSuite(t.Context(), "provider:inference", f.opt.ResultPort)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, f
}

// Reuse is a placement outcome, not an answer. Successful Apply must be followed
// by read-only proof verification and exactly one fresh requested Task.
func TestQuickstartSuiteApplyThenFreshAnswer(t *testing.T) {
	a, p, f := quickstartSuiteApplyFixture(t)
	before := f.taskCreates(t)
	result, err := a.applyWorkspaceSuiteLift(t.Context(), p, p.Plan.Summary().PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if f.taskCreates(t) != before || len(result.Resources) != 2 || result.Resources[0].Outcome != "reused" || result.Resources[1].Outcome != "reused" {
		t.Fatal("apply sent a Task or rewrote an evaluated pair")
	}
	answer, err := a.answerSuitePlacement(t.Context(), result, "A separate first question")
	if err != nil || answer != "A fresh answer" || f.taskCreates(t) != before+1 {
		t.Fatalf("fresh answer: %q err=%v", answer, err)
	}
	if !strings.Contains(a.Err.(*bytes.Buffer).String(), "reused") {
		t.Fatal("actual reuse outcomes were not reported")
	}
}

// Exercise the public quickstart phase selection, not merely a new helper.
func TestQuickstartPhaseUsesEvaluatedSuitePlacement(t *testing.T) {
	a, _, f := quickstartSuiteApplyFixture(t)
	provider, err := cloneOrkaDoc(f.nativeReads.objects["providers.core.orka.ai/inference"])
	if err != nil {
		t.Fatal(err)
	}
	nativeMetadata(provider)["name"] = "local"
	f.nativeReads.objects["providers.core.orka.ai/local"] = provider
	// Keep the fixed source, but clear the helper's prior different-binding pair.
	for _, kind := range []string{"providers.core.orka.ai", "agents.core.orka.ai"} {
		if err := os.Remove(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), kind+".json")); err != nil {
			t.Fatal(err)
		}
	}
	before := f.taskCreates(t)
	steps := a.quickstartSteps()
	if err := steps[len(steps)-1].fn(); err != nil {
		t.Fatal(err)
	}
	if a.quickstartPlacement == nil || a.quickstartPrepared == nil || f.taskCreates(t) != before+1 {
		t.Fatal("fixed phase bypassed full suite eval or Apply")
	}
	r := a.quickstartResult("question", "answer", time.Now())
	if r.SuiteDigest == "" || r.Workspace == "" || r.Layout == "" || r.Evidence == "" || !strings.Contains(r.Manifest, r.SuiteDigest) {
		t.Fatal("result omitted the actual release identity")
	}
}

func TestQuickstartSuiteApplyRefusalDoesNotAnswer(t *testing.T) {
	for _, state := range []string{"drift after review", "cancelled", "review output failed", "wrong review digest"} {
		t.Run(state, func(t *testing.T) {
			a, p, f := quickstartSuiteApplyFixture(t)
			before := f.taskCreates(t)
			ctx := t.Context()
			if state == "drift after review" {
				doc := nativeOwnedDoc(t, p.Plan.native, "Agent")
				doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "operator edit"}
				seedReconcile(t, os.Getenv("KMX_RECONCILE_DIR"), doc)
			}
			if state == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if state == "review output failed" {
				a.Err = evaluationDiffFailWriter{}
			}
			digest := p.Plan.Summary().PlanDigest
			if state == "wrong review digest" {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			result, err := a.applyWorkspaceSuiteLift(ctx, p, digest)
			if err == nil || result.State == "ready" || f.taskCreates(t) != before {
				t.Fatalf("refusal claimed placement/execution: state=%s err=%v", result.State, err)
			}
		})
	}
}

func TestQuickstartSuiteVerificationStopsAReplacedAgent(t *testing.T) {
	a, p, f := quickstartSuiteApplyFixture(t)
	result, err := a.applyWorkspaceSuiteLift(t.Context(), p, p.Plan.Summary().PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	before := f.taskCreates(t)
	doc := nativeOwnedDoc(t, p.Plan.native, "Agent")
	nativeMetadata(doc)["uid"] = "replacement-uid"
	seedReconcile(t, os.Getenv("KMX_RECONCILE_DIR"), doc)
	answer, err := a.answerSuitePlacement(t.Context(), result, "A separate first question")
	if err == nil || answer != "" || f.taskCreates(t) != before {
		t.Fatal("replacement received a fresh Task")
	}
}
