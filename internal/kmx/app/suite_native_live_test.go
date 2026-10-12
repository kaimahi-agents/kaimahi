package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// This read-only opt-in verifies CLI evidence against the independently loaded
// package and live subject. It never creates a Task or supplies a model answer.
func TestLiveSuiteNativeEvidence(t *testing.T) {
	root := os.Getenv("KMX_SUITE_NATIVE_WORKSPACE")
	if root == "" {
		t.Skip("dedicated native-suite qualifier only")
	}
	if os.Getenv("KMX_LIVE_KIND_CONTEXT") != "kind-kmx-suite-native" || os.Getenv("KAIMAHI_CONFIRM") != "kind-kmx-suite-native" {
		t.Fatal("qualification context or named confirmation refused")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	a := &App{Cfg: &config.Config{KubeContext: "kind-kmx-suite-native"}, Run: &run.Runner{Context: ctx, Stdout: io.Discard, Stderr: io.Discard}, Out: io.Discard, Err: io.Discard}
	if err := requireLiveLocalKind(ctx, a); err != nil {
		t.Fatal("dedicated local kind guard unavailable")
	}
	w, err := lockSuiteCandidate(root, os.Getenv("KMX_HOME"))
	if err != nil {
		t.Fatal("candidate unavailable")
	}
	p, err := w.load(ctx)
	closeErr := w.Close()
	if err != nil || closeErr != nil || p == nil {
		t.Fatal("pinned candidate unavailable")
	}
	s, err := workspace.Load(ctx, p)
	if err != nil {
		t.Fatal("exact packaged candidate unavailable")
	}
	raw, err := os.ReadFile(os.Getenv("KMX_SUITE_NATIVE_RECEIPT"))
	if err != nil {
		t.Fatal("explicit receipt unavailable")
	}
	r, err := decodeSuiteEvaluationReceipt(raw)
	if err != nil {
		t.Fatal("strict receipt refused")
	}
	// Expected subject comes from current read-only native preparation, not the
	// receipt's subject or a PortableAgent/bundle fallback.
	plan, err := a.prepareSuiteNative(ctx, s, "orka-system", "qualification-inference")
	if refusal := os.Getenv("KMX_SUITE_NATIVE_REFUSAL"); refusal != "" {
		want := "has legacy bundle ownership markers"
		if refusal == "unmarked" {
			want = "has no suite ownership marker"
		} else if refusal != "bundle" && refusal != "mixed" {
			t.Fatal("unsupported refusal control")
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatal("specific live ownership refusal was not observed")
		}
		if os.WriteFile(filepath.Join(os.Getenv("KMX_SUITE_NATIVE_PROOF_DIR"), "refused-"+refusal+".json"), []byte(`{"ownershipRefused":true}`), 0600) != nil {
			t.Fatal("refusal metadata unavailable")
		}
		return
	}
	if err != nil {
		t.Fatal("live expected subject unavailable")
	}
	canonical, digest, err := canonicalSuiteEvaluationReceipt(s, plan.Subject, r)
	if err != nil || string(canonical) != string(raw) || !r.FullCaseSet || !r.PromotionEligible || r.Result != rt.EvaluationPass || !r.Placement.Complete {
		t.Fatal("full canonical native proof refused")
	}
	placement := &suiteNativePlacement{Subject: r.Subject, Before: r.Placement.Before, Resources: r.Placement.Resources, Complete: r.Placement.Complete, plan: plan}
	if a.verifySuiteNative(ctx, placement) != nil {
		t.Fatal("live owned pair does not match proof")
	}
	port := os.Getenv("KMX_SUITE_NATIVE_RESULT_PORT")
	session, err := a.openOrkaResultSession(ctx, CreateOptions{Namespace: "orka-system", ResultServiceAccount: "orka-result-reader", ResultPort: port})
	if err != nil {
		t.Fatal("independent result access unavailable")
	}
	defer session.close()
	for _, row := range r.Cases {
		var packaged workspace.PackagedCase
		for _, c := range s.Cases {
			if c.Path == row.Path {
				packaged = c
			}
		}
		c, err := rt.ParseEvaluationCase(packaged.Bytes)
		if err != nil {
			t.Fatal("packaged case unavailable")
		}
		taskRaw, err := a.orkaCapture(ctx, nil, "-n", "orka-system", "get", "tasks.core.orka.ai", row.TaskName, "-o", "json")
		var task struct {
			Metadata struct {
				UID        string
				Generation int64
			}
			Spec struct {
				Type, Prompt string
				AgentRef     struct{ Name, Namespace string }
			}
		}
		if err != nil || json.Unmarshal(taskRaw, &task) != nil || task.Metadata.UID != row.TaskUID || task.Spec.Type != "ai" || task.Spec.Prompt != c.Input || task.Spec.AgentRef.Name != s.MemberID || task.Spec.AgentRef.Namespace != "orka-system" {
			t.Fatal("actual Task type, identity or exact packaged input mismatch")
		}
		answer, err := a.readOrkaTaskResult(ctx, "orka-system", orkaIdentity{Kind: "Task", Name: row.TaskName, UID: row.TaskUID, Generation: task.Metadata.Generation}, session, nil, false)
		if err != nil || rt.EvaluationAnswerDigest(answer) != row.AnswerSHA256 {
			t.Fatal("actual terminal result digest mismatch")
		}
		assertions := rt.EvaluateAssertions(c, rt.EvaluationEvidence{Answer: answer, AnswerAvailable: true})
		for _, assertion := range assertions {
			if assertion.Verdict != rt.EvaluationPass {
				t.Fatal("independent assertion did not pass")
			}
		}
	}
	policy, err := readSuitePromotionPolicyFile(os.Getenv("KMX_SUITE_NATIVE_POLICY"))
	if err != nil {
		t.Fatal("explicit operator JSON policy unavailable")
	}
	decision, err := gateSuitePromotion(s, plan.Subject, plan.Subject, r, policy)
	if err != nil || decision.DestinationEvaluated || decision.ReceiptDigest != digest || len(decision.AuthorizedTransitions) != 0 {
		t.Fatal("pure exact-subject permission refused or destination execution claimed")
	}
	for _, name := range []string{"provider", "secret", "runtime", "target", "evidence", "policy"} {
		t.Run(name, func(t *testing.T) {
			e := plan.Subject
			badR := suiteCloneReceipt(t, r)
			badP := suiteClonePolicy(t, policy)
			switch name {
			case "provider":
				e.Binding.Provider.ResourceVersion += "-changed"
			case "secret":
				e.Binding.Secret.ResourceVersion += "-changed"
			case "runtime":
				e.Runtime.ControllerUID += "-changed"
			case "target":
				e.Target.NamespaceUID += "-changed"
			case "evidence":
				badR.Cases[0].AnswerSHA256 = strings.Repeat("f", 64)
			case "policy":
				badP.RunID = strings.Repeat("f", 32)
			}
			if name != "evidence" && name != "policy" {
				// Recompute derived identities so refusal proves expected-subject
				// drift, not merely a stale binding/render digest.
				e = suiteTestSubject(t, s, e)
			}
			if _, err := gateSuitePromotion(s, e, plan.Subject, badR, badP); err == nil {
				t.Fatal("altered proof or subject accepted")
			}
		})
	}
	if !reflect.DeepEqual(s.RequiredImages, []string{}) {
		t.Fatal("native member image requirements are not empty")
	}
	result, err := spec.Marshal(map[string]any{"suiteDigest": s.Digest, "receiptDigest": digest, "policyDigest": decision.PolicyDigest, "requiredImages": []string{}, "gateNegatives": 6, "destinationEvaluated": false})
	if err != nil || os.WriteFile(filepath.Join(os.Getenv("KMX_SUITE_NATIVE_PROOF_DIR"), "verified.json"), result, 0600) != nil {
		t.Fatal("payload-free verification output unavailable")
	}
}
