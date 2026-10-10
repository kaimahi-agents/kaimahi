package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func gatePolicy(t *testing.T, bundle, destUID, sourceUID string) {
	t.Helper()
	policy := "rules:\n  - destination:\n      clusterUID: " + destUID + "\n      namespace: orka-system\n    evaluated:\n      clusterUID: " + sourceUID + "\n      namespace: orka-system\n"
	if err := os.WriteFile(filepath.Join(bundle, "lift-policy.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "eval"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "eval", "hello.yaml"), []byte(evalHelloCase), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(bundle, "eval", "example.yaml")); err != nil {
		t.Fatal(err)
	}
}

func gateReceipt(t *testing.T, bundle, alias, uid, result, digest, cases string) {
	t.Helper()
	r := bundleEvaluationReceipt{
		Bundle: "sample", PortableDigest: digest, CasesDigest: cases, FullCaseSet: true, Result: result,
		Target: bundleEvaluationTarget{Runtime: agentruntime.Orka, Context: alias, Namespace: "orka-system", Agent: "sample", ClusterUID: uid, AgentUID: "agent-uid"},
		Cases:  []bundleEvaluationResult{{ID: "greet", Verdict: result}},
	}
	if err := writeBundleEvaluationReceipt(bundle, uid, r); err != nil {
		t.Fatal(err)
	}
}

func TestLiftPolicyRefusesSelfEvaluationTarget(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "cluster-uid")
	opt.OverrideGate = "bootstrap"
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "same target") {
		t.Fatalf("self-gating policy accepted: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

// Removing the policy check would let a first production lift write even when
// the evaluation target has never produced a receipt.
func TestLiftPolicyRefusesWithoutEvaluation(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "no evaluation receipt") {
		t.Fatalf("missing evaluation accepted: %v (%s)", err, notes.String())
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestLiftGateReceiptConditions(t *testing.T) {
	for _, tc := range []struct {
		name, uid, result, mismatch, want string
	}{
		{"pass", "staging-uid", "pass", "", ""},
		{"other cluster same context", "other-uid", "pass", "", "no evaluation receipt"},
		{"stale agent", "staging-uid", "pass", "agent", "portable digest"},
		{"changed cases", "staging-uid", "pass", "cases", "case-set digest"},
		{"fail", "staging-uid", "fail", "", "fail"},
		{"unknown", "staging-uid", "unknown", "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
			_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
			if err != nil {
				t.Fatal(err)
			}
			cases := currentBundleCasesDigest(opt.BundleDir)
			if tc.mismatch == "agent" {
				digest = strings.Repeat("a", 64)
			}
			if tc.mismatch == "cases" {
				cases = strings.Repeat("b", 64)
			}
			gateReceipt(t, opt.BundleDir, "staging", tc.uid, tc.result, digest, cases)
			err = a.LiftAgentBundle(opt)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q refusal, got %v", tc.want, err)
			} else {
				for _, call := range orkaCalls(t, dir) {
					if call.Document != nil && !containsDryRun(call.Args) {
						t.Fatalf("gate wrote resources: %+v", call)
					}
				}
			}
		})
	}
}

func TestV2GateRequiresEveryCurrentAssertionDefinition(t *testing.T) {
	raw := []byte("id: greet\ninput: hi\nexpectContains: [hello]\nassertions:\n- id: negative\n  type: notContains\n  value: refuse\n")
	c, err := agentruntime.ParseEvaluationCase(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := []bundleEvaluationCase{{Digest: agentruntime.EvaluationCaseDigest(raw), Case: c}}
	rows := agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{Answer: "hello", AnswerAvailable: true})
	for _, mode := range []string{"pass", "legacy", "missing", "definition", "type", "id", "fail", "duplicate", "unknown version", "missing case digest", "missing input digest", "stale case digest", "stale input digest", "missing task name", "missing task uid", "missing answer digest", "unsafe task name", "unsafe task uid", "bad answer digest", "invalid reason", "execution unavailable", "result unavailable", "execution failed"} {
		t.Run(mode, func(t *testing.T) {
			r := bundleEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("a", 32), Bundle: "sample", PortableDigest: strings.Repeat("b", 64), CasesDigest: strings.Repeat("c", 64), FullCaseSet: true, GitCommit: "uncommitted", Result: "pass", Target: bundleEvaluationTarget{Runtime: agentruntime.Orka, ClusterUID: "staging", Namespace: "orka-system", Agent: "sample", AgentUID: "uid"}, Cases: []bundleEvaluationResult{{ID: "greet", Verdict: "pass", CaseDigest: agentruntime.EvaluationCaseDigest(raw), InputDigest: agentruntime.EvaluationInputDigest(c.Input), TaskName: "sample-eval-one", TaskUID: "task-uid", AnswerSHA256: agentruntime.EvaluationAnswerDigest("hello"), Assertions: append([]agentruntime.EvaluationAssertionResult(nil), rows...)}}}
			switch mode {
			case "legacy":
				r.SchemaVersion = 0
				r.RunID = ""
				r.Cases[0].Assertions = nil
			case "missing":
				r.Cases[0].Assertions = r.Cases[0].Assertions[:1]
			case "definition":
				r.Cases[0].Assertions[1].DefinitionDigest = strings.Repeat("d", 64)
			case "type":
				r.Cases[0].Assertions[1].Type = "contains"
			case "id":
				r.Cases[0].Assertions[1].ID = "other"
			case "fail":
				r.Cases[0].Assertions[1].Verdict = agentruntime.EvaluationFail
			case "duplicate":
				r.Cases[0].Assertions[1] = r.Cases[0].Assertions[0]
			case "unknown version":
				r.SchemaVersion = 3
			case "missing case digest":
				r.Cases[0].CaseDigest = ""
			case "missing input digest":
				r.Cases[0].InputDigest = ""
			case "stale case digest":
				r.Cases[0].CaseDigest = strings.Repeat("f", 64)
			case "stale input digest":
				r.Cases[0].InputDigest = strings.Repeat("f", 64)
			case "missing task name":
				r.Cases[0].TaskName = ""
			case "missing task uid":
				r.Cases[0].TaskUID = ""
			case "missing answer digest":
				r.Cases[0].AnswerSHA256 = ""
			case "unsafe task name":
				r.Cases[0].TaskName = "task\ncanary"
			case "unsafe task uid":
				r.Cases[0].TaskUID = "uid\ncanary"
			case "bad answer digest":
				r.Cases[0].AnswerSHA256 = "bad"
			case "invalid reason":
				r.Cases[0].Assertions[0].Reason = "private-reason-canary"
			case "execution unavailable":
				r.Cases[0].Detail = evaluationExecutionUnavailable
			case "result unavailable":
				r.Cases[0].Detail = evaluationResultUnavailable
			case "execution failed":
				r.Cases[0].Detail = evaluationExecutionFailed
			}
			got := bundleGateCondition([]bundleGateEvidence{{Receipt: r, File: "eval-test.json"}}, bundleGateTarget{ClusterUID: "staging", Namespace: "orka-system"}, "sample", r.PortableDigest, r.CasesDigest, cases)
			if (got == "") != (mode == "pass") {
				t.Fatalf("gate condition=%q", got)
			}
		})
	}
}

func TestV2GateRefusesForgedUnsupportedToolPass(t *testing.T) {
	for _, kind := range []string{"toolCalled", "toolNotCalled"} {
		t.Run(kind, func(t *testing.T) {
			c := agentruntime.EvaluationCase{ID: "one", Input: "hi", Assertions: []agentruntime.EvaluationAssertion{{ID: "tool", Type: kind, Tool: "inventory"}}}
			rows := agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{ToolSupported: false})
			rows[0].Verdict = agentruntime.EvaluationPass
			rows[0].Reason = "assertion satisfied"
			digest := agentruntime.EvaluationCaseDigest([]byte("authored tool case"))
			r := bundleEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("a", 32), Bundle: "sample", PortableDigest: strings.Repeat("b", 64), CasesDigest: strings.Repeat("c", 64), GitCommit: "uncommitted", Result: "pass", Cases: []bundleEvaluationResult{{ID: "one", Verdict: "pass", CaseDigest: digest, InputDigest: agentruntime.EvaluationInputDigest(c.Input), TaskName: "task-one", TaskUID: "task-uid", AnswerSHA256: agentruntime.EvaluationAnswerDigest("hello"), Assertions: rows}}}
			if bundleReceiptCaseResultsPass(r, []bundleEvaluationCase{{Digest: digest, Case: c}}) {
				t.Fatal("forged unsupported tool pass qualified gate")
			}
		})
	}
}

func TestGateReceiptJSONShape(t *testing.T) {
	for _, tc := range []struct {
		name, root, member string
		valid              bool
	}{
		{name: "omitted legacy fields", valid: true},
		{name: "old nullable arrays", member: `"matched":null,"missing":null,`, valid: true},
		{name: "null version", root: `"schemaVersion":null,`},
		{name: "null run id", root: `"runID":null,`},
		{name: "null discriminants", root: `"schemaVersion":null,"runID":null,`},
		{name: "legacy empty case digest", member: `"caseDigest":"",`},
		{name: "legacy null input digest", member: `"inputDigest":null,`},
		{name: "legacy empty assertions", member: `"assertions":[],`},
		{name: "legacy null assertions", member: `"assertions":null,`},
		{name: "unknown root field", root: `"unknown":true,`},
		{name: "unknown case field", member: `"unknown":true,`},
		{name: "wrong assertions type", member: `"assertions":{},`},
		{name: "wrong cases type", root: `"cases":{},`},
		{name: "null case", root: `"cases":[null],`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			if err := os.Mkdir(filepath.Join(bundle, "receipts"), 0700); err != nil {
				t.Fatal(err)
			}
			r := bundleEvaluationReceipt{Bundle: "sample", Target: bundleEvaluationTarget{Runtime: agentruntime.Orka}, Cases: []bundleEvaluationResult{{ID: "one", Verdict: "pass"}}}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(string(raw), `"id":"one"`, tc.member+`"id":"one"`, 1)
			body = "{" + tc.root + body[1:]
			if err := os.WriteFile(filepath.Join(bundle, "receipts", "eval-test.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = readBundleGateEvidence(bundle)
			if tc.valid && err != nil {
				t.Fatalf("legacy receipt refused: %v", err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), "invalid evaluation receipt")) {
				t.Fatalf("malformed receipt accepted: %v", err)
			}
		})
	}
}

func TestLegacyGateRefusesV2Members(t *testing.T) {
	for _, member := range []string{"caseDigest", "inputDigest", "assertions"} {
		r := bundleEvaluationReceipt{Cases: []bundleEvaluationResult{{ID: "one", Verdict: "pass"}}}
		switch member {
		case "caseDigest":
			r.Cases[0].CaseDigest = strings.Repeat("a", 64)
		case "inputDigest":
			r.Cases[0].InputDigest = strings.Repeat("a", 64)
		case "assertions":
			r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{{ID: "check"}}
		}
		if bundleReceiptCaseResultsPass(r, []bundleEvaluationCase{{Case: agentruntime.EvaluationCase{ID: "one", ExpectContains: []string{"hello"}}}}) {
			t.Errorf("legacy gate accepted %s", member)
		}
	}
}

func containsDryRun(args []string) bool {
	for _, arg := range args {
		if arg == "--dry-run=server" {
			return true
		}
	}
	return false
}

func TestLiftExplicitGateUsesReceiptContextOnlyToSelectIdentity(t *testing.T) {
	a, opt, _, _ := liftBundleFixture(t)
	if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", "example.yaml"), []byte(evalHelloCase), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	opt.RequireEvaluated = "staging"
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
}

func TestLiftGateRejectsSingleCaseEvenWhenItIsOnlyCase(t *testing.T) {
	a, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	path := bundleEvaluationReceiptPath(opt.BundleDir, "staging", "orka-system", "staging-uid")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleEvaluationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.FullCaseSet = false
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "single-case") {
		t.Fatalf("single-case accepted: %v", err)
	}
}

func TestLiftGateRequiresPassingResultForEachCurrentCase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cases []bundleEvaluationResult
	}{
		{"no results", nil},
		{"wrong case", []bundleEvaluationResult{{ID: "different", Verdict: "pass"}}},
		{"failed case despite overall pass", []bundleEvaluationResult{{ID: "greet", Verdict: "fail"}}},
		{"unknown case despite overall pass", []bundleEvaluationResult{{ID: "greet", Verdict: "unknown"}}},
		{"duplicate case", []bundleEvaluationResult{{ID: "greet", Verdict: "pass"}, {ID: "greet", Verdict: "pass"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
			_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
			if err != nil {
				t.Fatal(err)
			}
			gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
			path := bundleEvaluationReceiptPath(opt.BundleDir, "staging", "orka-system", "staging-uid")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt bundleEvaluationReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.Cases = append([]bundleEvaluationResult{}, tc.cases...)
			raw, err = json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "case results") {
				t.Fatalf("invalid case results accepted: %v", err)
			}
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && !containsDryRun(call.Args) {
					t.Fatalf("gate wrote resource: %+v", call)
				}
			}
		})
	}
}

func TestLiftGateRefusesRepointedDestinationDuringPreflight(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	t.Setenv("KMX_LIFT_REPOINT_AFTER_PREFLIGHT", "1")
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "destination cluster identity changed") {
		t.Fatalf("repointed context was used for deployment: %v", err)
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !containsDryRun(call.Args) {
			t.Fatalf("gate wrote resource to repointed context: %+v", call)
		}
	}
	if _, err := os.Stat(bundleReceiptPath(opt.BundleDir, "kind-test", "orka-system", "cluster-uid")); !os.IsNotExist(err) {
		t.Fatalf("lift receipt written for the wrong cluster: %v", err)
	}
}

func TestLiftGateConflictingAliasesRefuse(t *testing.T) {
	a, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "ci", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	gateReceipt(t, opt.BundleDir, "developer", "staging-uid", "pass", strings.Repeat("c", 64), currentBundleCasesDigest(opt.BundleDir))
	err = a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "ci") || !strings.Contains(err.Error(), "developer") {
		t.Fatalf("conflicting aliases accepted: %v", err)
	}
}

func TestCreateRerunPreservesLiftPolicy(t *testing.T) {
	_, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	agent, err := os.ReadFile(filepath.Join(opt.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := os.ReadFile(filepath.Join(opt.BundleDir, "bindings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOrkaBundle(opt.BundleDir, agent, bindings); err != nil {
		t.Fatalf("create rerun discarded policy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundleDir, "lift-policy.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestLiftPolicyInvalidCannotBeOverridden(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	path := filepath.Join(opt.BundleDir, "lift-policy.yaml")
	if err := os.WriteFile(path, []byte("rules:\n  - destination:\n      context: prod\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opt.OverrideGate = "emergency"
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "invalid lift-policy.yaml") {
		t.Fatalf("invalid policy bypassed: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestLiftGateStatusReportsPolicyResult(t *testing.T) {
	a, opt, dir, _, name := bundleStatusFixture(t)
	seedBundleNamespace(t, dir, "cluster-uid")
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	op := BundleStatusOptions{BundleDir: opt.BundleDir, Context: "kind-test", Namespace: "orka-system"}
	report, err := a.bundleStatusReport(op)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.Targets[0].Gate, "no evaluation receipt") {
		t.Fatalf("status hid gate: %+v", report.Targets[0])
	}
	gateReceipt(t, opt.BundleDir, "ci-alias", "staging-uid", "pass", report.PortableDigest, currentBundleCasesDigest(opt.BundleDir))
	report, err = a.bundleStatusReport(op)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].Gate != "pass" {
		t.Fatalf("status gate = %q, bundle %s", report.Targets[0].Gate, name)
	}
}

func TestLiftGateConflictingAgentIdentityListsAliases(t *testing.T) {
	a, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "developer", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	gateReceipt(t, opt.BundleDir, "ci", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	path := bundleEvaluationReceiptPath(opt.BundleDir, "ci", "orka-system", "staging-uid")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleEvaluationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Target.AgentUID = "different-agent-uid"
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "ci") || !strings.Contains(err.Error(), "developer") {
		t.Fatalf("conflicting Agent identities accepted: %v", err)
	}
}

func TestLiftGateConflictingCaseSetsListAliases(t *testing.T) {
	a, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "developer", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	gateReceipt(t, opt.BundleDir, "ci", "staging-uid", "pass", digest, strings.Repeat("a", 64))
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "ci") || !strings.Contains(err.Error(), "developer") || !strings.Contains(err.Error(), "case-set") {
		t.Fatalf("case-set conflict accepted: %v", err)
	}
}

func TestLiftGatePlanCannotOverrideFailure(t *testing.T) {
	a, opt, _, notes := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	opt.Plan, opt.OverrideGate = true, "incident 123"
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "no evaluation receipt") || !strings.Contains(notes.String(), "Gate:") {
		t.Fatalf("override made failed plan succeed: %v; %s", err, notes.String())
	}
}

func TestLiftGatePlanAndOverride(t *testing.T) {
	a, opt, _, notes := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(notes.String(), "Gate:") {
		t.Fatalf("plan hid failing gate: %v; %s", err, notes.String())
	}
	opt.Plan = false
	opt.OverrideGate = "incident 123"
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "incident 123") || !strings.Contains(notes.String(), "no evaluation receipt") {
		t.Fatalf("stderr omitted override: %s", notes.String())
	}
	raw, err := os.ReadFile(bundleReceiptPath(opt.BundleDir, "kind-test", "orka-system", "cluster-uid"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["gateOverrideReason"] != "incident 123" || !strings.Contains(saved["gateFailedCondition"].(string), "no evaluation receipt") {
		t.Fatalf("missing override audit: %+v", saved)
	}
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	gateReceipt(t, opt.BundleDir, "ci", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	opt.OverrideGate = ""
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(bundleReceiptPath(opt.BundleDir, "kind-test", "orka-system", "cluster-uid"))
	if err != nil {
		t.Fatal(err)
	}
	saved = map[string]any{}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved["gateOverrideReason"]; ok {
		t.Fatalf("override persisted after next lift: %+v", saved)
	}
}
