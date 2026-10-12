package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// One independent wire fixture exercises the kagent decoder, not its writer.
var kagentEvaluationWire = strings.ReplaceAll(`{
 "schemaVersion":2,"runID":"RUN_ID","bundle":"sample",
 "portableDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
 "renderedDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
 "deploymentReceiptDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
 "casesDigest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
 "fullCaseSet":true,"gitCommit":"uncommitted",
 "target":{"runtime":"kagent","runtimeVersion":"v0.10.3","engine":"go","context":"staging","clusterUID":"staging-uid","namespace":"orka-system","agent":"sample","agentUID":"agent-uid","agentGeneration":1,"modelConfigUID":"model-uid","modelConfigGeneration":1,"model":"local/model:1",
 "platform":{"sourceCommit":"8878c39ac64421cb665c8e62ecc6411dd12f2fc2",
 "controllerImage":{"os":"linux","architecture":"amd64","digest":"8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8"},
 "adkImage":{"os":"linux","architecture":"amd64","digest":"7f788d09d5f21ccc3068679310943dcf4f0df19e1a93960423810438f4238b24"},
 "controller":[{"Kind":"Service","Name":"kagent","Namespace":"orka-system","UID":"controller-service","ResourceVersion":"10"},{"Kind":"Deployment","Name":"kagent","Namespace":"orka-system","UID":"controller-deploy","ResourceVersion":"11","Generation":1},{"Kind":"ConfigMap","Name":"kagent","Namespace":"orka-system","UID":"controller-config","ResourceVersion":"12"}]},
 "snapshot":{"proofs":[{"Kind":"ModelConfig","Name":"sample","Namespace":"orka-system","UID":"model-uid","Generation":1,"SpecDigest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},{"Kind":"Agent","Name":"sample","Namespace":"orka-system","UID":"agent-uid","Generation":1,"SpecDigest":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}],
 "workload":[{"Kind":"Deployment","Name":"sample","Namespace":"orka-system","UID":"workload-deploy","ResourceVersion":"20","Generation":1,"SpecDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"Kind":"Service","Name":"sample","Namespace":"orka-system","UID":"workload-service","ResourceVersion":"21"},{"Kind":"ServiceAccount","Name":"sample","Namespace":"orka-system","UID":"workload-account","ResourceVersion":"22"},{"Kind":"Secret","Name":"sample","Namespace":"orka-system","UID":"workload-secret","ResourceVersion":"23"}],
 "dependencies":[{"Kind":"Secret","Name":"model-key","Namespace":"orka-system","UID":"dependency-secret","ResourceVersion":"30","Key":"api-key"}]}},
 "result":"pass","cases":[{"id":"case-one","verdict":"pass","caseDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","inputDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
 "execution":{"rpcID":"rpc-1","requestMessageID":"message-1","taskID":"native-task-1","contextID":"native-context-1","state":"completed"},
 "answerSHA256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
 "assertions":[{"id":"assert-one","type":"contains","definitionDigest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","verdict":"pass","reason":"assertion satisfied"}]}]}`, "RUN_ID", strings.Repeat("1", 32))

func kagentEvaluationFixture(t *testing.T) kagentEvaluationReceipt {
	t.Helper()
	var r kagentEvaluationReceipt
	if err := json.Unmarshal([]byte(kagentEvaluationWire), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func kagentEvaluationVerdict(r *kagentEvaluationReceipt, verdict, detail string) {
	r.Result, r.Cases[0].Verdict, r.Cases[0].Detail = verdict, verdict, detail
	row := &r.Cases[0].Assertions[0]
	row.Verdict = agentruntime.EvaluationVerdict(verdict)
	row.Reason = map[string]string{"pass": "assertion satisfied", "fail": "assertion not satisfied", "unknown": "answer unavailable"}[verdict]
	if detail != "" {
		r.Cases[0].AnswerSHA256 = ""
		row.Verdict, row.Reason = agentruntime.EvaluationUnknown, "answer unavailable"
	}
}

func TestKagentEvaluationRejectsIncompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*kagentEvaluationReceipt)
	}{
		{"legacy", func(r *kagentEvaluationReceipt) { r.SchemaVersion = 0 }},
		{"run ID", func(r *kagentEvaluationReceipt) { r.RunID = "bad" }},
		{"portable digest", func(r *kagentEvaluationReceipt) { r.PortableDigest = "bad" }},
		{"rendered digest", func(r *kagentEvaluationReceipt) { r.RenderedDigest = "" }},
		{"deployment digest", func(r *kagentEvaluationReceipt) { r.DeploymentReceiptDigest = "" }},
		{"case digest", func(r *kagentEvaluationReceipt) { r.Cases[0].CaseDigest = "bad" }},
		{"runtime", func(r *kagentEvaluationReceipt) { r.Target.Runtime = "other" }},
		{"version", func(r *kagentEvaluationReceipt) { r.Target.RuntimeVersion = "v0.10.2" }},
		{"Python", func(r *kagentEvaluationReceipt) { r.Target.Engine = "python" }},
		{"model", func(r *kagentEvaluationReceipt) { r.Target.Model = "" }},
		{"generation", func(r *kagentEvaluationReceipt) { r.Target.AgentGeneration = 0 }},
		{"platform", func(r *kagentEvaluationReceipt) { r.Target.Platform.ADKImage.Architecture = "s390x" }},
		{"image pin", func(r *kagentEvaluationReceipt) { r.Target.Platform.ControllerImage.Digest = strings.Repeat("a", 64) }},
		{"source pin", func(r *kagentEvaluationReceipt) { r.Target.Platform.SourceCommit = "bad" }},
		{"proof", func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Proofs = nil }},
		{"workload", func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[0].SpecDigest = "" }},
		{"dependency", func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Dependencies[0].Key = "" }},
		{"credential hash", func(r *kagentEvaluationReceipt) {
			r.Target.Snapshot.Dependencies[0].SpecDigest = strings.Repeat("a", 64)
		}},
		{"aggregate", func(r *kagentEvaluationReceipt) { r.Result = "fail" }},
		{"assertion", func(r *kagentEvaluationReceipt) { r.Cases[0].Assertions[0].DefinitionDigest = "bad" }},
		{"toolCalled pass", func(r *kagentEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "toolCalled" }},
		{"toolNotCalled pass", func(r *kagentEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "toolNotCalled" }},
		{"RPC ID", func(r *kagentEvaluationReceipt) { r.Cases[0].Execution.RPCID = "" }},
		{"message ID", func(r *kagentEvaluationReceipt) { r.Cases[0].Execution.RequestMessageID = "../unsafe" }},
		{"unpaired task", func(r *kagentEvaluationReceipt) { r.Cases[0].Execution.TaskID = "" }},
		{"answer", func(r *kagentEvaluationReceipt) { r.Cases[0].AnswerSHA256 = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			tc.change(&r)
			out, err := diffExecute(t, r, r)
			if err == nil || err.Error() != "invalid evaluation receipt" || out != "" {
				t.Fatalf("invalid evidence accepted or disclosed: %v %s", err, out)
			}
		})
	}
}

func TestKagentEvaluationJSONPrivacy(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{`"schemaVersion":2`, `"schemaVersion":2,"SchemaVersion":2`},
		{`"rpcID":"rpc-1"`, `"rpcID":"rpc-1","RPCID":"hidden"`},
		{`"fullCaseSet":true,`, ``},
		{`"fullCaseSet":true`, `"fullCaseSet":null`},
		{`"fullCaseSet":true`, `"fullCaseSet":"true"`},
		{`"Generation":1`, `"Generation":null`},
		{`"agentGeneration":1`, `"agentGeneration":1.5`},
		{`"state":"completed"`, `"state":null`},
		{`"schemaVersion":2`, `"schemaVersion":2,"input":"PRIVATE_INPUT"`},
		{`"verdict":"pass"`, `"verdict":"pass","answer":"PRIVATE_ANSWER"`},
		{`"type":"contains"`, `"type":"contains","value":"PRIVATE_OPERAND"`},
		{`"execution":{`, `"execution":{"rawResponse":"PRIVATE_RESPONSE",`},
		{`"state":"completed"`, `"state":"completed","taskName":"fake","taskUID":"fake"`},
		{`"Key":"api-key"`, `"Key":"api-key","value":"PRIVATE_CREDENTIAL"`},
		{`"ResourceVersion":"30"`, `"ResourceVersion":"30","credentialHash":"PRIVATE_HASH"`},
		{`"api-key"`, "\"\xffPRIVATE_UTF8\""},
	} {
		t.Run(tc.replacement, func(t *testing.T) {
			raw := strings.Replace(kagentEvaluationWire, tc.old, tc.replacement, 1)
			var out bytes.Buffer
			err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, []byte(raw)), After: diffWriteRaw(t, []byte(kagentEvaluationWire))})
			if raw == kagentEvaluationWire || err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
				t.Fatalf("unsafe JSON accepted or disclosed: %v %s", err, out.String())
			}
		})
	}
}

func TestKagentEvaluationNativeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		state, detail, verdict string
		valid                  bool
	}{
		{"completed", "", "pass", true}, {"completed", "", "fail", true},
		{"completed", "evaluation result unavailable", "unknown", true},
		{"completed", "evaluation execution unavailable", "unknown", true},
		{"completed", "", "unknown", false},
		{"", "evaluation execution unavailable", "unknown", true},
		{"failed", "evaluation execution failed", "fail", true},
		{"canceled", "evaluation execution failed", "fail", true},
		{"rejected", "evaluation execution failed", "fail", true},
		{"failed", "evaluation execution unavailable", "unknown", true},
		{"canceled", "evaluation execution unavailable", "unknown", true},
		{"rejected", "evaluation execution unavailable", "unknown", true},
		{"working", "evaluation execution unavailable", "unknown", true},
		{"input-required", "evaluation execution unavailable", "unknown", true},
		{"auth-required", "evaluation execution unavailable", "unknown", true},
		{"failed", "", "pass", false}, {"working", "", "pass", false},
		{"other", "evaluation execution unavailable", "unknown", false},
		{"failed", "evaluation execution failed", "unknown", false},
	} {
		t.Run(tc.state+"/"+tc.detail+"/"+tc.verdict, func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			kagentEvaluationVerdict(&r, tc.verdict, tc.detail)
			r.Cases[0].Execution.State = tc.state
			if tc.state == "" {
				r.Cases[0].Execution.TaskID, r.Cases[0].Execution.ContextID = "", ""
			}
			n, valid := normalizeKagentEvaluation(r)
			if valid != tc.valid || valid && n.Result != tc.verdict {
				t.Fatalf("valid=%t result=%s, want %t/%s", valid, n.Result, tc.valid, tc.verdict)
			}
		})
	}
}

func TestKagentEvaluationSnapshotCoherence(t *testing.T) {
	mcp := agentruntime.ResourceEvidence{Kind: "RemoteMCPServer", Name: "tool-server", Namespace: "orka-system", UID: "mcp-uid", ResourceVersion: "31", Generation: 1, SpecDigest: strings.Repeat("e", 64)}
	for _, tc := range []struct {
		name   string
		change func(*kagentEvaluationReceipt)
		valid  bool
	}{
		{"shared reference", func(r *kagentEvaluationReceipt) {
			p := r.Target.Platform.Controller[2]
			p.Key = "header"
			r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, p)
		}, true},
		{"MCP reference", func(r *kagentEvaluationReceipt) {
			r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, mcp)
		}, true},
		{"MCP spec", func(r *kagentEvaluationReceipt) {
			p := mcp
			p.SpecDigest = "bad"
			r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, p)
		}, false},
		{"secret conflict", func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Dependencies[0].Name = "sample" }, false},
		{"UID reused", func(r *kagentEvaluationReceipt) {
			r.Target.Platform.Controller[0].UID = r.Target.Snapshot.Workload[0].UID
		}, false},
		{"controller binding", func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[1].Name = "foreign" }, false},
		{"proof generation", func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Proofs[1].Generation++ }, false},
		{"reference conflict", func(r *kagentEvaluationReceipt) {
			p := r.Target.Snapshot.Dependencies[0]
			p.ResourceVersion = "changed"
			r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, p)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			tc.change(&r)
			if _, valid := normalizeKagentEvaluation(r); valid != tc.valid {
				t.Fatalf("snapshot valid=%t, want %t", valid, tc.valid)
			}
		})
	}
}

func TestKagentEvaluationExecutionIDsUnique(t *testing.T) {
	for _, field := range []func(*kagentEvaluationExecution) *string{
		func(e *kagentEvaluationExecution) *string { return &e.RPCID }, func(e *kagentEvaluationExecution) *string { return &e.RequestMessageID },
		func(e *kagentEvaluationExecution) *string { return &e.TaskID }, func(e *kagentEvaluationExecution) *string { return &e.ContextID },
	} {
		r := kagentEvaluationFixture(t)
		c := r.Cases[0]
		c.ID, c.Execution = "case-two", kagentEvaluationExecution{RPCID: "rpc-2", RequestMessageID: "message-2", TaskID: "task-2", ContextID: "context-2", State: "completed"}
		*field(&c.Execution) = *field(&r.Cases[0].Execution)
		r.Cases = append(r.Cases, c)
		if _, valid := normalizeKagentEvaluation(r); valid {
			t.Fatal("reused execution identity accepted")
		}
	}
}

func TestKagentEvaluationCaseLimit(t *testing.T) {
	r := kagentEvaluationFixture(t)
	c := r.Cases[0]
	r.Cases = nil
	for i := range 1001 {
		c.ID = fmt.Sprintf("case-%d", i)
		c.Execution = kagentEvaluationExecution{RPCID: fmt.Sprintf("rpc-%d", i), RequestMessageID: fmt.Sprintf("message-%d", i), TaskID: fmt.Sprintf("task-%d", i), ContextID: fmt.Sprintf("context-%d", i), State: "completed"}
		r.Cases = append(r.Cases, c)
		if i >= 999 {
			if _, valid := normalizeKagentEvaluation(r); valid != (i == 999) {
				t.Fatalf("case count %d valid=%t", i+1, valid)
			}
		}
	}
}

func TestKagentEvaluationDiffProducerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*kagentEvaluationReceipt)
	}{
		{"cluster", func(r *kagentEvaluationReceipt) { r.Target.ClusterUID = "other" }},
		{"Agent", func(r *kagentEvaluationReceipt) {
			r.Target.AgentUID = "other"
			r.Target.Snapshot.Proofs[1].UID = "other"
		}},
		{"ModelConfig", func(r *kagentEvaluationReceipt) {
			r.Target.ModelConfigUID = "other"
			r.Target.Snapshot.Proofs[0].UID = "other"
		}},
		{"model", func(r *kagentEvaluationReceipt) { r.Target.Model = "other/model" }},
		{"controller", func(r *kagentEvaluationReceipt) {
			r.Target.Platform.ControllerImage.Digest = "b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"
		}},
		{"ADK", func(r *kagentEvaluationReceipt) {
			r.Target.Platform.ADKImage.Digest = "5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := kagentEvaluationFixture(t), kagentEvaluationFixture(t)
			tc.change(&after)
			if out, err := diffExecute(t, before, after); err == nil || !strings.Contains(out, "runtime identity differs") {
				t.Fatalf("producer change not incompatible: %v %s", err, out)
			}
		})
	}
	for _, foreign := range []any{diffOrkaFixture(), diffFixture("pass")} {
		if out, err := diffExecute(t, kagentEvaluationFixture(t), foreign); err == nil || !strings.Contains(out, "runtime identity differs") {
			t.Fatalf("cross-runtime comparison: %v %s", err, out)
		}
	}
}

func TestKagentEvaluationDiffIgnoresRunMetadata(t *testing.T) {
	before, after := kagentEvaluationFixture(t), kagentEvaluationFixture(t)
	after.RunID, after.GitCommit, after.Target.Context = strings.Repeat("2", 32), strings.Repeat("3", 40), "alias"
	after.PortableDigest, after.RenderedDigest, after.DeploymentReceiptDigest = strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	after.Target.AgentGeneration, after.Target.Snapshot.Proofs[1].Generation = 2, 2
	after.Cases[0].Execution = kagentEvaluationExecution{RPCID: "other-rpc", RequestMessageID: "other-message", TaskID: "other-task", ContextID: "other-context", State: "completed"}
	if out, err := diffExecute(t, before, after); err != nil || !strings.Contains(out, "\tstable\t") {
		t.Fatalf("run metadata selected a producer: %v %s", err, out)
	}
}

func TestKagentEvaluationAssertionEvidence(t *testing.T) {
	for _, tc := range []struct {
		kind, verdict string
		scores        []int
		valid         bool
	}{
		{"toolCalled", "unknown", nil, true}, {"toolNotCalled", "unknown", nil, true},
		{"llmJudge", "pass", []int{80, 100}, true}, {"llmJudge", "fail", []int{0, 20}, true},
		{"llmJudge", "unknown", []int{49, 50}, true}, {"llmJudge", "pass", []int{101, 100}, false},
	} {
		t.Run(tc.kind+"/"+tc.verdict+fmt.Sprint(tc.scores), func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			r.Result, r.Cases[0].Verdict = tc.verdict, tc.verdict
			row := &r.Cases[0].Assertions[0]
			if tc.kind == "llmJudge" {
				*row = integrationJudgeRow(tc.scores)
			} else {
				row.Type, row.Verdict, row.Reason = tc.kind, agentruntime.EvaluationUnknown, "runtime does not support tools"
			}
			if _, valid := normalizeKagentEvaluation(r); valid != tc.valid {
				t.Fatalf("assertion evidence valid=%t, want %t", valid, tc.valid)
			}
		})
	}
}

func TestKagentEvaluationOrkaEvidenceExclusion(t *testing.T) {
	for _, tc := range []struct {
		name              string
		orka, malformed   bool
		condition, status string
	}{
		{"foreign only", false, false, "no evaluation receipt", "none"},
		{"alongside Orka", true, false, "", "pass"},
		{"malformed foreign", true, true, "invalid evaluation receipt", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, opt, _, _ := liftBundleFixture(t)
			gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
			_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
			if err != nil {
				t.Fatal(err)
			}
			casesDigest := currentBundleCasesDigest(opt.BundleDir)
			r := kagentEvaluationFixture(t)
			r.PortableDigest, r.CasesDigest = digest, casesDigest
			kagentEvaluationVerdict(&r, "fail", "")
			if err := writeKagentEvaluationReceipt(opt.BundleDir, r); err != nil {
				t.Fatal(err)
			}
			if tc.orka {
				gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, casesDigest)
			}
			if tc.malformed {
				path := kagentEvaluationLatestPath(opt.BundleDir, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
				if err := os.WriteFile(path, []byte(strings.Replace(kagentEvaluationWire, "completed", "PRIVATE_STATE", 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			required, condition := evaluateBundleLiftGate(opt.BundleDir, "sample", digest, bundleGateTarget{ClusterUID: "cluster-uid", Namespace: "orka-system"}, "staging")
			if !required || (tc.condition == "" && condition != "") || !strings.Contains(condition, tc.condition) || strings.Contains(condition, "PRIVATE") {
				t.Fatalf("Orka gate condition: %q", condition)
			}
			target := bundleTargetStatus{ObservedClusterUID: "staging-uid", Namespace: "orka-system", LiveDigest: digest, Agent: bundleResourceStatus{UID: "agent-uid"}}
			if got := bundleEvaluationStatus(opt.BundleDir, target, digest, casesDigest); got != tc.status {
				t.Fatalf("Orka evaluation status=%s, want %s", got, tc.status)
			}
		})
	}
}

func TestKagentEvaluationPublicationNamespace(t *testing.T) {
	bundle, r := diffTempDir(t), kagentEvaluationFixture(t)
	orka := bundleEvaluationReceiptPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
	if err := os.MkdirAll(filepath.Dir(orka), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orka, []byte("Orka sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeKagentEvaluationReceipt(bundle, r); err != nil {
		t.Fatal(err)
	}
	latest := kagentEvaluationLatestPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
	for _, path := range []string{latest, evaluationHistoryPath(bundle, r.RunID)} {
		if n, err := readEvaluationDiffSource(path); err != nil || n.RunID != r.RunID {
			t.Fatalf("published evidence: %v", err)
		}
	}
	if raw, err := os.ReadFile(orka); err != nil || string(raw) != "Orka sentinel" {
		t.Fatal("kagent overwrote Orka latest")
	}
	for _, target := range []struct{ context, namespace, cluster string }{{"alias", r.Target.Namespace, r.Target.ClusterUID}, {r.Target.Context, "other", r.Target.ClusterUID}, {r.Target.Context, r.Target.Namespace, "other"}} {
		if latest == kagentEvaluationLatestPath(bundle, target.context, target.namespace, target.cluster) {
			t.Fatal("target path collision")
		}
	}
}

func TestKagentEvaluationValidationPrecedesWrites(t *testing.T) {
	bundle, r := diffTempDir(t), kagentEvaluationFixture(t)
	r.Cases[0].Execution.TaskID = ""
	if err := writeKagentEvaluationReceipt(bundle, r); err == nil || err.Error() != "invalid kagent evaluation receipt" {
		t.Fatalf("invalid receipt publication: %v", err)
	}
	if entries, err := os.ReadDir(bundle); err != nil || len(entries) != 0 {
		t.Fatal("validation refusal wrote files")
	}
}
