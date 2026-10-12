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

// Literal offline evidence: no live capability or payload is required to compare
// a receipt, and A2A identities never masquerade as Kubernetes Task identities.
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
 "controller":[{"Kind":"Service","Name":"kagent","Namespace":"kagent","UID":"controller-service","ResourceVersion":"10"},{"Kind":"Deployment","Name":"kagent-controller","Namespace":"kagent","UID":"controller-deploy","ResourceVersion":"11","Generation":1},{"Kind":"ConfigMap","Name":"kagent","Namespace":"kagent","UID":"controller-config","ResourceVersion":"12"}]},
 "snapshot":{"proofs":[{"Kind":"ModelConfig","Name":"sample","Namespace":"orka-system","UID":"model-uid","Generation":1,"SpecDigest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},{"Kind":"Agent","Name":"sample","Namespace":"orka-system","UID":"agent-uid","Generation":1,"SpecDigest":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}],
 "workload":[{"Kind":"Deployment","Name":"sample","Namespace":"orka-system","UID":"workload-deploy","ResourceVersion":"20","Generation":1,"SpecDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"Kind":"Service","Name":"sample","Namespace":"orka-system","UID":"workload-service","ResourceVersion":"21"},{"Kind":"ServiceAccount","Name":"sample","Namespace":"orka-system","UID":"workload-account","ResourceVersion":"22"},{"Kind":"Secret","Name":"sample","Namespace":"orka-system","UID":"workload-secret","ResourceVersion":"23"}],
 "dependencies":[{"Kind":"Secret","Name":"model-key","Namespace":"orka-system","UID":"dependency-secret","ResourceVersion":"30","Key":"api-key"}]}},
 "result":"pass","cases":[{"id":"case-one","verdict":"pass","caseDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","inputDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
 "execution":{"rpcID":"rpc-1","requestMessageID":"message-1","taskID":"native-task-1","contextID":"native-context-1","state":"completed"},
 "answerSHA256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
 "assertions":[{"id":"assert-one","type":"contains","definitionDigest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","verdict":"pass","reason":"assertion satisfied"}]}]}`, "RUN_ID", strings.Repeat("1", 32))

func TestKagentEvaluationOfflineFixture(t *testing.T) {
	path := diffWriteRaw(t, []byte(kagentEvaluationWire))
	n, err := readEvaluationDiffSource(path)
	if err != nil {
		t.Fatalf("qualified kagent offline evidence refused: %v", err)
	}
	if n.Identity.Runtime != "kagent" || !n.IdentityComplete || n.Cases["case-one"].Assertions["assert-one"].Verdict != "pass" {
		t.Fatalf("normalization lost evidence: %+v", n)
	}
	var out bytes.Buffer
	if err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: path}); err != nil || !strings.Contains(out.String(), "\tstable\t") {
		t.Fatalf("offline comparison err=%v out=%s", err, out.String())
	}
}

func kagentWireMutation(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(kagentEvaluationWire), &r); err != nil {
		t.Fatal(err)
	}
	change(r)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func kagentWireTarget(r map[string]any) map[string]any { return r["target"].(map[string]any) }
func kagentWireCase(r map[string]any) map[string]any   { return r["cases"].([]any)[0].(map[string]any) }
func kagentWireExecution(r map[string]any) map[string]any {
	return kagentWireCase(r)["execution"].(map[string]any)
}
func kagentWireRow(r map[string]any) map[string]any {
	return kagentWireCase(r)["assertions"].([]any)[0].(map[string]any)
}
func kagentWirePlatform(r map[string]any) map[string]any {
	return kagentWireTarget(r)["platform"].(map[string]any)
}
func kagentWireSnapshot(r map[string]any) map[string]any {
	return kagentWireTarget(r)["snapshot"].(map[string]any)
}

func TestKagentEvaluationMalformedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{"legacy", func(r map[string]any) { r["schemaVersion"] = 0; delete(r, "runID") }},
		{"runID", func(r map[string]any) { r["runID"] = "bad" }},
		{"portable digest", func(r map[string]any) { r["portableDigest"] = "bad" }},
		{"rendered digest", func(r map[string]any) { r["renderedDigest"] = "" }},
		{"receipt digest", func(r map[string]any) { delete(r, "deploymentReceiptDigest") }},
		{"case-set digest", func(r map[string]any) { r["casesDigest"] = "bad" }},
		{"git commit", func(r map[string]any) { r["gitCommit"] = "PRIVATE_COMMIT_CANARY" }},
		{"full marker omitted", func(r map[string]any) { delete(r, "fullCaseSet") }},
		{"unknown runtime", func(r map[string]any) { kagentWireTarget(r)["runtime"] = "PRIVATE_RUNTIME_CANARY" }},
		{"aggregate", func(r map[string]any) { r["result"] = "fail" }},
		{"case aggregate", func(r map[string]any) { r["result"] = "fail"; kagentWireCase(r)["verdict"] = "fail" }},
		{"empty cases", func(r map[string]any) { r["cases"] = []any{} }},
		{"duplicate case", func(r map[string]any) { r["cases"] = append(r["cases"].([]any), kagentWireCase(r)) }},
		{"missing execution", func(r map[string]any) { delete(kagentWireCase(r), "execution") }},
		{"missing answer", func(r map[string]any) { delete(kagentWireCase(r), "answerSHA256") }},
		{"bad answer digest", func(r map[string]any) { kagentWireCase(r)["answerSHA256"] = "bad" }},
		{"case digest", func(r map[string]any) { kagentWireCase(r)["caseDigest"] = "bad" }},
		{"input digest", func(r map[string]any) { kagentWireCase(r)["inputDigest"] = "bad" }},
		{"case ID", func(r map[string]any) { kagentWireCase(r)["id"] = "PRIVATE\nCASE_CANARY" }},
		{"empty assertions", func(r map[string]any) { kagentWireCase(r)["assertions"] = []any{} }},
		{"duplicate assertion", func(r map[string]any) {
			kagentWireCase(r)["assertions"] = append(kagentWireCase(r)["assertions"].([]any), kagentWireRow(r))
		}},
		{"assertion digest", func(r map[string]any) { kagentWireRow(r)["definitionDigest"] = "bad" }},
		{"assertion reason", func(r map[string]any) { kagentWireRow(r)["reason"] = "PRIVATE_REASON_CANARY" }},
		{"assertion ID", func(r map[string]any) { kagentWireRow(r)["id"] = "PRIVATE\nID_CANARY" }},
		{"detail", func(r map[string]any) { kagentWireCase(r)["detail"] = "PRIVATE_DIAGNOSTIC_CANARY" }},
		{"tool pass", func(r map[string]any) { kagentWireRow(r)["type"] = "toolCalled" }},
		{"negative tool pass", func(r map[string]any) { kagentWireRow(r)["type"] = "toolNotCalled" }},
		{"unknown assertion", func(r map[string]any) { kagentWireRow(r)["type"] = "other" }},
		{"completed failed detail", func(r map[string]any) { kagentWireCase(r)["detail"] = "evaluation execution failed" }},
		{"unknown state", func(r map[string]any) { kagentWireExecution(r)["state"] = "PRIVATE_STATE_CANARY" }},
		{"unpaired task", func(r map[string]any) { delete(kagentWireExecution(r), "taskID") }},
		{"unpaired context", func(r map[string]any) { delete(kagentWireExecution(r), "contextID") }},
		{"unpaired state", func(r map[string]any) { delete(kagentWireExecution(r), "state") }},
		{"failed pass", func(r map[string]any) { kagentWireExecution(r)["state"] = "failed" }},
		{"working pass", func(r map[string]any) { kagentWireExecution(r)["state"] = "working" }},
		{"proofs absent", func(r map[string]any) { delete(kagentWireSnapshot(r), "proofs") }},
		{"workload absent", func(r map[string]any) { delete(kagentWireSnapshot(r), "workload") }},
		{"dependencies absent", func(r map[string]any) { delete(kagentWireSnapshot(r), "dependencies") }},
		{"controller absent", func(r map[string]any) { delete(kagentWirePlatform(r), "controller") }},
		{"model credential key absent", func(r map[string]any) {
			delete(kagentWireSnapshot(r)["dependencies"].([]any)[0].(map[string]any), "Key")
		}},
	}
	for _, field := range []string{"context", "clusterUID", "namespace", "agent", "agentUID", "modelConfigUID", "model", "runtimeVersion", "engine", "agentGeneration", "modelConfigGeneration"} {
		field := field
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"missing target " + field, func(r map[string]any) { delete(kagentWireTarget(r), field) }})
	}
	for field, value := range map[string]any{"runtimeVersion": "v0.10.2", "engine": "python", "agent": "other", "namespace": "../unsafe", "model": strings.Repeat("m", 257), "agentGeneration": -1, "modelConfigGeneration": 0} {
		field, value := field, value
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"invalid target " + field, func(r map[string]any) { kagentWireTarget(r)[field] = value }})
	}
	for _, field := range []string{"rpcID", "requestMessageID", "taskID", "contextID"} {
		for _, bad := range []string{"", strings.Repeat("x", 257), "PRIVATE\nID_CANARY", "../escape"} {
			field, bad := field, bad
			tests = append(tests, struct {
				name   string
				change func(map[string]any)
			}{"unsafe " + field + bad, func(r map[string]any) { kagentWireExecution(r)[field] = bad }})
		}
	}
	for _, part := range []string{"controllerImage", "adkImage"} {
		for field, value := range map[string]any{"os": "darwin", "architecture": "s390x", "digest": strings.Repeat("a", 64)} {
			part, field, value := part, field, value
			tests = append(tests, struct {
				name   string
				change func(map[string]any)
			}{"invalid image " + part + field, func(r map[string]any) { kagentWirePlatform(r)[part].(map[string]any)[field] = value }})
		}
	}
	tests = append(tests, struct {
		name   string
		change func(map[string]any)
	}{"source pin", func(r map[string]any) { kagentWirePlatform(r)["sourceCommit"] = strings.Repeat("a", 40) }})
	for _, section := range []string{"proofs", "workload", "controller"} {
		section := section
		get := func(r map[string]any) map[string]any {
			if section == "controller" {
				return kagentWirePlatform(r)
			}
			return kagentWireSnapshot(r)
		}
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"unordered " + section, func(r map[string]any) { rows := get(r)[section].([]any); rows[0], rows[1] = rows[1], rows[0] }})
		for i := 0; i < map[string]int{"proofs": 2, "workload": 4, "controller": 3}[section]; i++ {
			i := i
			for _, field := range []string{"Kind", "Name", "Namespace", "UID"} {
				field := field
				tests = append(tests, struct {
					name   string
					change func(map[string]any)
				}{"incomplete " + section + string(rune('0'+i)) + field, func(r map[string]any) { delete(get(r)[section].([]any)[i].(map[string]any), field) }})
			}
			tests = append(tests, struct {
				name   string
				change func(map[string]any)
			}{"unsafe identity " + section + string(rune('0'+i)), func(r map[string]any) { get(r)[section].([]any)[i].(map[string]any)["UID"] = "PRIVATE\nUID_CANARY" }})
		}
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"extra " + section, func(r map[string]any) {
			owner := get(r)
			owner[section] = append(owner[section].([]any), owner[section].([]any)[0])
		}})
	}
	for _, section := range []string{"proofs", "workload"} {
		section := section
		for _, field := range []string{"Generation", "SpecDigest"} {
			field := field
			tests = append(tests, struct {
				name   string
				change func(map[string]any)
			}{"missing proof " + section + field, func(r map[string]any) { delete(kagentWireSnapshot(r)[section].([]any)[0].(map[string]any), field) }})
		}
	}
	for _, section := range []string{"dependencies", "workload"} {
		section := section
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"credential hash " + section, func(r map[string]any) {
			rows := kagentWireSnapshot(r)[section].([]any)
			i := 0
			if section == "workload" {
				i = 3
			}
			rows[i].(map[string]any)["SpecDigest"] = strings.Repeat("a", 64)
		}})
	}
	for _, field := range []string{"Kind", "Name", "Namespace", "UID", "ResourceVersion", "Key"} {
		field := field
		tests = append(tests, struct {
			name   string
			change func(map[string]any)
		}{"unsafe dependency " + field, func(r map[string]any) {
			kagentWireSnapshot(r)["dependencies"].([]any)[0].(map[string]any)[field] = "../unsafe"
		}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := kagentWireMutation(t, tc.change)
			var out bytes.Buffer
			err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, raw), After: diffWriteRaw(t, []byte(kagentEvaluationWire))})
			if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
				t.Fatalf("malformed evidence accepted/leaked: %v %s", err, out.String())
			}
			bundle := diffTempDir(t)
			if err := os.Mkdir(filepath.Join(bundle, "receipts"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bundle, "receipts", "eval-invalid.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readBundleGateEvidence(bundle); err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("gate accepted/leaked malformed foreign evidence: %v", err)
			}
		})
	}
}

func TestKagentEvaluationStrictJSONPrivacy(t *testing.T) {
	for _, change := range []struct{ old, new string }{
		{`"schemaVersion":2`, `"schemaVersion":2,"SchemaVersion":2`},
		{`"rpcID":"rpc-1"`, `"rpcID":"rpc-1","RPCID":"hidden"`},
		{`"Key":"api-key"`, `"Key":"api-key","key":"hidden"`},
		{`"state":"completed"`, `"state":null`},
		{`"fullCaseSet":true`, `"fullCaseSet":null`},
		{`"fullCaseSet":true`, `"fullCaseSet":"true"`},
		{`"Generation":1`, `"Generation":"1"`},
		{`"agentGeneration":1`, `"agentGeneration":1.5`},
		{`"execution":{`, `"execution":null,"unknown":{`},
		{`"schemaVersion":2`, `"schemaVersion":2,"input":"PRIVATE_PAYLOAD_CANARY"`},
		{`"execution":{`, `"execution":{"rawResponse":"PRIVATE_PAYLOAD_CANARY",`},
		{`"state":"completed"`, `"state":"completed","taskName":"fake","taskUID":"fake"`},
		{`"verdict":"pass"`, `"verdict":"pass","answer":"PRIVATE_PAYLOAD_CANARY"`},
		{`"type":"contains"`, `"type":"contains","value":"PRIVATE_PAYLOAD_CANARY"`},
		{`"type":"contains"`, `"type":"contains","tool":"PRIVATE_PAYLOAD_CANARY"`},
		{`"Key":"api-key"`, `"Key":"api-key","value":"PRIVATE_CREDENTIAL_CANARY"`},
		{`"ResourceVersion":"30"`, `"ResourceVersion":"30","credentialHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
	} {
		raw := strings.Replace(kagentEvaluationWire, change.old, change.new, 1)
		if raw == kagentEvaluationWire {
			t.Fatalf("test mutation not applied: %s", change.old)
		}
		_, err := readEvaluationDiffSource(diffWriteRaw(t, []byte(raw)))
		if err == nil || err.Error() != "invalid evaluation receipt" {
			t.Fatalf("unsafe JSON accepted/leaked: %v", err)
		}
	}
	for _, raw := range []string{kagentEvaluationWire + ` {"answer":"PRIVATE_PAYLOAD_CANARY"}`, strings.Repeat(" ", maxSessionsReceiptBytes+1), strings.Replace(kagentEvaluationWire, "api-key", "\xff", 1)} {
		if _, err := readEvaluationDiffSource(diffWriteRaw(t, []byte(raw))); err == nil || err.Error() != "invalid evaluation receipt" {
			t.Fatalf("invalid bounded JSON accepted: %v", err)
		}
	}
}

// Removing the foreign-runtime exclusion must poison the otherwise passing
// Orka gate; treating it as eligible must wrongly authorize kagent-only history.
func TestKagentEvaluationLatestCoexistsWithOrkaGate(t *testing.T) {
	_, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, "receipts", "eval-kagent-fixture.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(kagentEvaluationWire), 0600); err != nil {
		t.Fatal(err)
	}
	required, condition := evaluateBundleLiftGate(opt.BundleDir, "sample", digest, bundleGateTarget{ClusterUID: "cluster-uid", Namespace: "orka-system"}, "staging")
	if !required || !strings.Contains(condition, "no evaluation receipt") {
		t.Fatalf("kagent-only history eligible or poisoned: required=%t condition=%s", required, condition)
	}
	gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, currentBundleCasesDigest(opt.BundleDir))
	required, condition = evaluateBundleLiftGate(opt.BundleDir, "sample", digest, bundleGateTarget{ClusterUID: "cluster-uid", Namespace: "orka-system"}, "staging")
	if !required || condition != "" {
		t.Fatalf("foreign receipt poisoned Orka gate: required=%t condition=%s", required, condition)
	}
	evidence, err := readBundleGateEvidence(opt.BundleDir)
	if err != nil || len(evidence) != 1 || evidence[0].Receipt.Target.Runtime != "orka" {
		t.Fatalf("foreign eligibility: %+v %v", evidence, err)
	}
}

func TestKagentEvaluationStatusAndMalformedLatest(t *testing.T) {
	_, opt, _, _ := liftBundleFixture(t)
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	casesDigest := currentBundleCasesDigest(opt.BundleDir)
	r := kagentEvaluationFixture(t)
	r.PortableDigest = digest
	r.CasesDigest = casesDigest
	kagentEvaluationVerdict(&r, "fail", "")
	if err := writeKagentEvaluationReceipt(opt.BundleDir, r); err != nil {
		t.Fatal(err)
	}
	target := bundleTargetStatus{ObservedClusterUID: "staging-uid", Namespace: "orka-system", LiveDigest: digest, Agent: bundleResourceStatus{UID: "agent-uid"}}
	if got := bundleEvaluationStatus(opt.BundleDir, target, digest, casesDigest); got != "none" {
		t.Fatalf("foreign status eligible: %s", got)
	}
	gateReceipt(t, opt.BundleDir, "staging", "staging-uid", "pass", digest, casesDigest)
	if got := bundleEvaluationStatus(opt.BundleDir, target, digest, casesDigest); got != "pass" {
		t.Fatalf("foreign failure poisoned Orka status: %s", got)
	}
	latest := kagentEvaluationLatestPath(opt.BundleDir, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
	raw := strings.Replace(kagentEvaluationWire, `"state":"completed"`, `"state":"PRIVATE_STATE_CANARY"`, 1)
	if err := os.WriteFile(latest, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	_, condition := evaluateBundleLiftGate(opt.BundleDir, "sample", digest, bundleGateTarget{ClusterUID: "cluster-uid", Namespace: "orka-system"}, "staging")
	if !strings.Contains(condition, "invalid evaluation receipt") || strings.Contains(condition, "PRIVATE") {
		t.Fatalf("malformed foreign history did not fail closed privately: %s", condition)
	}
	if got := bundleEvaluationStatus(opt.BundleDir, target, digest, casesDigest); got != "none" {
		t.Fatalf("status trusted malformed history: %s", got)
	}
	if err := os.Remove(latest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evaluationHistoryPath(opt.BundleDir, r.RunID), []byte("invalid archived evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	_, condition = evaluateBundleLiftGate(opt.BundleDir, "sample", digest, bundleGateTarget{ClusterUID: "cluster-uid", Namespace: "orka-system"}, "staging")
	if condition != "" {
		t.Fatalf("archives influenced latest gate: %s", condition)
	}
}

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
		row.Verdict = agentruntime.EvaluationUnknown
		row.Reason = "answer unavailable"
	}
}

func TestKagentEvaluationNativeStateCoherence(t *testing.T) {
	for _, tc := range []struct{ state, detail, verdict string }{
		{"completed", "", "pass"}, {"completed", "", "fail"},
		{"completed", "evaluation result unavailable", "unknown"},
		{"completed", "evaluation execution unavailable", "unknown"},
		{"failed", "evaluation execution failed", "fail"},
		{"canceled", "evaluation execution failed", "fail"},
		{"rejected", "evaluation execution failed", "fail"},
		{"submitted", "evaluation execution unavailable", "unknown"},
		{"working", "evaluation execution unavailable", "unknown"},
		{"input-required", "evaluation execution unavailable", "unknown"},
		{"auth-required", "evaluation execution unavailable", "unknown"},
		{"unknown", "evaluation execution unavailable", "unknown"},
		{"", "evaluation execution unavailable", "unknown"},
	} {
		t.Run(tc.state+tc.detail, func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			kagentEvaluationVerdict(&r, tc.verdict, tc.detail)
			r.Cases[0].Execution.State = tc.state
			if tc.state == "" {
				r.Cases[0].Execution.TaskID = ""
				r.Cases[0].Execution.ContextID = ""
			}
			n, err := readEvaluationDiffSource(diffWrite(t, r))
			if err != nil || n.Result != tc.verdict || n.Cases["case-one"].Verdict != tc.verdict {
				t.Fatalf("native outcome misread: %v %+v", err, n)
			}
			for _, mutate := range []func(*kagentEvaluationReceipt){
				func(r *kagentEvaluationReceipt) { r.Cases[0].Execution.RPCID = "" },
				func(r *kagentEvaluationReceipt) { r.Cases[0].Execution.RequestMessageID = "" },
				func(r *kagentEvaluationReceipt) {
					r.Cases[0].Execution.TaskID = ""
					r.Cases[0].Execution.ContextID = "unexpected"
				},
				func(r *kagentEvaluationReceipt) {
					r.Cases[0].Detail = "evaluation result unavailable"
					if tc.detail == "evaluation result unavailable" {
						r.Cases[0].Detail = "evaluation execution failed"
					}
				},
			} {
				bad := kagentEvaluationFixture(t)
				kagentEvaluationVerdict(&bad, tc.verdict, tc.detail)
				bad.Cases[0].Execution = r.Cases[0].Execution
				mutate(&bad)
				// Completed result-unavailable is coherent only with unknown/no-answer
				// rows; it is already the positive case above, not a malformed mutation.
				if bad.Cases[0].Execution.State == "completed" && bad.Cases[0].Detail == "evaluation result unavailable" && bad.Result == "unknown" {
					continue
				}
				if _, err := readEvaluationDiffSource(diffWrite(t, bad)); err == nil {
					t.Fatalf("native coherence mutation accepted: %+v", bad.Cases[0])
				}
			}
		})
	}
}

func TestKagentEvaluationUniqueExecutionIDs(t *testing.T) {
	for _, field := range []string{"rpc", "message", "task", "context"} {
		t.Run(field, func(t *testing.T) {
			r := kagentEvaluationFixture(t)
			c := r.Cases[0]
			c.ID = "case-two"
			c.Execution = kagentEvaluationExecution{RPCID: "rpc-2", RequestMessageID: "message-2", TaskID: "task-2", ContextID: "context-2", State: "completed"}
			switch field {
			case "rpc":
				c.Execution.RPCID = r.Cases[0].Execution.RPCID
			case "message":
				c.Execution.RequestMessageID = r.Cases[0].Execution.RequestMessageID
			case "task":
				c.Execution.TaskID = r.Cases[0].Execution.TaskID
			case "context":
				c.Execution.ContextID = r.Cases[0].Execution.ContextID
			}
			r.Cases = append(r.Cases, c)
			if _, err := readEvaluationDiffSource(diffWrite(t, r)); err == nil {
				t.Fatal("reused native/request identity accepted")
			}
		})
	}
	r := kagentEvaluationFixture(t)
	for i := range 1000 {
		c := r.Cases[0]
		c.ID = fmt.Sprintf("case-%d", i)
		c.Execution = kagentEvaluationExecution{RPCID: fmt.Sprintf("rpc-%d", i), RequestMessageID: fmt.Sprintf("message-%d", i), TaskID: fmt.Sprintf("task-%d", i), ContextID: fmt.Sprintf("context-%d", i), State: "completed"}
		r.Cases = append(r.Cases, c)
	}
	if _, ok := normalizeKagentEvaluation(r); ok {
		t.Fatal("more than 1000 cases accepted")
	}
}

func TestKagentEvaluationPublicDiffTransitions(t *testing.T) {
	for _, tc := range []struct {
		before, after, status string
		failure               bool
	}{
		{"pass", "pass", "stable", false}, {"fail", "fail", "stable", false},
		{"pass", "fail", "regression", true}, {"fail", "pass", "fix", false},
		{"pass", "unknown", "lostEvidence", true}, {"unknown", "pass", "gainedEvidence", true},
		{"unknown", "unknown", "unknown", true},
	} {
		t.Run(tc.before+"-"+tc.after, func(t *testing.T) {
			before, after := kagentEvaluationFixture(t), kagentEvaluationFixture(t)
			kagentEvaluationVerdict(&before, tc.before, "")
			kagentEvaluationVerdict(&after, tc.after, "")
			for _, r := range []*kagentEvaluationReceipt{&before, &after} {
				if r.Result == "unknown" {
					kagentEvaluationVerdict(r, "unknown", "evaluation execution unavailable")
					r.Cases[0].Execution.TaskID = ""
					r.Cases[0].Execution.ContextID = ""
					r.Cases[0].Execution.State = ""
				}
			}
			// Revision, context label, generations and run/native IDs are not producer identity.
			after.RunID = strings.Repeat("2", 32)
			after.GitCommit = strings.Repeat("3", 40)
			after.Target.Context = "alias"
			after.PortableDigest = strings.Repeat("4", 64)
			after.RenderedDigest = strings.Repeat("5", 64)
			after.DeploymentReceiptDigest = strings.Repeat("6", 64)
			after.Target.AgentGeneration = 2
			after.Target.ModelConfigGeneration = 2
			after.Target.Snapshot.Proofs[0].Generation = 2
			after.Target.Snapshot.Proofs[1].Generation = 2
			after.Cases[0].Execution.RPCID = "rpc-other"
			after.Cases[0].Execution.RequestMessageID = "message-other"
			if after.Cases[0].Execution.TaskID != "" {
				after.Cases[0].Execution.TaskID = "task-other"
				after.Cases[0].Execution.ContextID = "context-other"
			}
			out, err := diffExecute(t, before, after)
			if (err != nil) != tc.failure || !strings.Contains(out, "\t"+tc.status+"\t") || !strings.Contains(out, "not an evaluation-pass claim") {
				t.Fatalf("diff outcome err=%v out=%s", err, out)
			}
		})
	}
}

func TestKagentEvaluationPublicDiffIdentityAndDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		change       func(*kagentEvaluationReceipt)
	}{
		{"cluster", "runtime identity differs", func(r *kagentEvaluationReceipt) { r.Target.ClusterUID = "other" }},
		{"namespace", "runtime identity differs", func(r *kagentEvaluationReceipt) {
			r.Target.Namespace = "other"
			for i := range r.Target.Snapshot.Proofs {
				r.Target.Snapshot.Proofs[i].Namespace = "other"
			}
			for i := range r.Target.Snapshot.Workload {
				r.Target.Snapshot.Workload[i].Namespace = "other"
			}
			for i := range r.Target.Snapshot.Dependencies {
				r.Target.Snapshot.Dependencies[i].Namespace = "other"
			}
		}},
		{"Agent replacement", "runtime identity differs", func(r *kagentEvaluationReceipt) {
			r.Target.AgentUID = "other-agent"
			r.Target.Snapshot.Proofs[1].UID = "other-agent"
		}},
		{"ModelConfig replacement", "runtime identity differs", func(r *kagentEvaluationReceipt) {
			r.Target.ModelConfigUID = "other-model"
			r.Target.Snapshot.Proofs[0].UID = "other-model"
		}},
		{"model route", "runtime identity differs", func(r *kagentEvaluationReceipt) { r.Target.Model = "other/model:2" }},
		{"controller platform", "runtime identity differs", func(r *kagentEvaluationReceipt) {
			r.Target.Platform.ControllerImage = kagentEvaluationImage{OS: "linux", Architecture: "arm64", Digest: "fb3e7c0400d73f9abbba9ffc4552c39be253178f7ddece6a840a0a64415e150c"}
		}},
		{"ADK platform", "runtime identity differs", func(r *kagentEvaluationReceipt) {
			r.Target.Platform.ADKImage = kagentEvaluationImage{OS: "linux", Architecture: "arm64", Digest: "0eb8becbaf323df2c29c8e73a14a775d24bf9c2f9314bd8a12a1da6e57e851fc"}
		}},
		{"partial", "partial case set", func(r *kagentEvaluationReceipt) { r.FullCaseSet = false }},
		{"input", "inputChanged", func(r *kagentEvaluationReceipt) { r.Cases[0].InputDigest = strings.Repeat("1", 64) }},
		{"definition", "definitionChanged", func(r *kagentEvaluationReceipt) { r.Cases[0].Assertions[0].DefinitionDigest = strings.Repeat("1", 64) }},
		{"type", "definitionChanged", func(r *kagentEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "regex" }},
		{"case source", "caseSourceChanged", func(r *kagentEvaluationReceipt) { r.Cases[0].CaseDigest = strings.Repeat("1", 64) }},
		{"case set", "caseSetChanged: true", func(r *kagentEvaluationReceipt) { r.CasesDigest = strings.Repeat("1", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := kagentEvaluationFixture(t), kagentEvaluationFixture(t)
			tc.change(&after)
			out, err := diffExecute(t, before, after)
			if err == nil || !strings.Contains(out, tc.status) {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
	for _, foreign := range []any{diffOrkaFixture(), diffFixture("pass")} {
		out, err := diffExecute(t, kagentEvaluationFixture(t), foreign)
		if err == nil || !strings.Contains(out, "runtime identity differs") {
			t.Fatalf("cross-runtime compared: %v %s", err, out)
		}
	}
}

func TestKagentEvaluationSnapshotAndAnswerAttribution(t *testing.T) {
	for _, mutate := range []func(*kagentEvaluationReceipt){
		func(r *kagentEvaluationReceipt) {
			r.Target.Platform.Controller[0].UID = r.Target.Snapshot.Workload[0].UID
		},
		func(r *kagentEvaluationReceipt) { kagentEvaluationVerdict(r, "unknown", "") },
		func(r *kagentEvaluationReceipt) {
			row := integrationJudgeRow(nil)
			row.Reason = "answer unavailable"
			r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row}
			r.Cases[0].Verdict = "unknown"
			r.Result = "unknown"
		},
	} {
		r := kagentEvaluationFixture(t)
		mutate(&r)
		if _, err := readEvaluationDiffSource(diffWrite(t, r)); err == nil {
			t.Error("contradictory snapshot/answer attribution accepted")
		}
	}
}

func TestKagentEvaluationSnapshotBoundaries(t *testing.T) {
	for _, change := range []func(*kagentEvaluationReceipt){
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Proofs[0].UID = "other" },
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Proofs[1].Generation = 2 },
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[0].SpecDigest = "bad" },
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[1].ResourceVersion = "" },
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[2].ResourceVersion = "" },
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[3].ResourceVersion = "" },
		func(r *kagentEvaluationReceipt) {
			r.Target.Snapshot.Workload[1].UID = r.Target.Snapshot.Workload[0].UID
		},
		func(r *kagentEvaluationReceipt) { r.Target.Snapshot.Workload[3].Key = "credential" },
		func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[0].ResourceVersion = "" },
		func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[1].Generation = 0 },
		func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[2].SpecDigest = strings.Repeat("a", 64) },
		func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[2].Namespace = "other" },
		func(r *kagentEvaluationReceipt) { r.Target.Platform.Controller[2].Name = "other" },
		func(r *kagentEvaluationReceipt) {
			p := r.Target.Snapshot.Dependencies[0]
			p.ResourceVersion = "changed"
			r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, p)
		},
	} {
		r := kagentEvaluationFixture(t)
		change(&r)
		if _, err := readEvaluationDiffSource(diffWrite(t, r)); err == nil {
			t.Fatal("incoherent snapshot accepted")
		}
	}
	r := kagentEvaluationFixture(t)
	r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies,
		agentruntime.ResourceEvidence{Kind: "RemoteMCPServer", Name: "tool-server", Namespace: "orka-system", UID: "mcp-uid", ResourceVersion: "31", Generation: 1, SpecDigest: strings.Repeat("e", 64)},
		agentruntime.ResourceEvidence{Kind: "ConfigMap", Name: "tool-headers", Namespace: "orka-system", UID: "header-uid", ResourceVersion: "32", Key: "header"})
	if _, err := readEvaluationDiffSource(diffWrite(t, r)); err != nil {
		t.Fatalf("complete references refused: %v", err)
	}
	for _, mutate := range []func(*agentruntime.ResourceEvidence){
		func(p *agentruntime.ResourceEvidence) { p.Generation = 0 }, func(p *agentruntime.ResourceEvidence) { p.SpecDigest = "bad" }, func(p *agentruntime.ResourceEvidence) { p.Key = "payload" }, func(p *agentruntime.ResourceEvidence) { p.ResourceVersion = "" }, func(p *agentruntime.ResourceEvidence) { p.Namespace = "other" },
	} {
		bad := kagentEvaluationFixture(t)
		p := r.Target.Snapshot.Dependencies[1]
		mutate(&p)
		bad.Target.Snapshot.Dependencies = append(bad.Target.Snapshot.Dependencies, p)
		if _, err := readEvaluationDiffSource(diffWrite(t, bad)); err == nil {
			t.Fatal("malformed MCP dependency accepted")
		}
	}
}

func TestKagentEvaluationJudgeEvidence(t *testing.T) {
	for _, tc := range []struct {
		scores          []int
		verdict, reason string
	}{
		{[]int{80, 100}, "pass", "assertion satisfied"}, {[]int{0, 20}, "fail", "assertion not satisfied"},
		{[]int{49, 50}, "unknown", "judge verdict split"}, {nil, "unknown", "judge response unavailable"},
	} {
		before, after := kagentEvaluationFixture(t), kagentEvaluationFixture(t)
		row := integrationJudgeRow(tc.scores)
		// Expectations are literal; the existing helper only supplies valid judge identity.
		for _, r := range []*kagentEvaluationReceipt{&before, &after} {
			r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{integrationJudgeRow(tc.scores)}
			r.Cases[0].Verdict = tc.verdict
			r.Result = tc.verdict
		}
		n, err := readEvaluationDiffSource(diffWrite(t, before))
		if err != nil || n.Result != tc.verdict || string(n.Cases["case-one"].Assertions[row.ID].Verdict) != tc.verdict || n.Cases["case-one"].Assertions[row.ID].Reason != tc.reason {
			t.Fatalf("judge evidence wrong: %+v %v", n, err)
		}
		after.Cases[0].Assertions[0].Judge.ModelVersion = "other-version"
		out, err := diffExecute(t, before, after)
		// Both copies must have independent judge identities.
		if err == nil || !strings.Contains(out, "identityChanged") {
			t.Fatalf("judge identity lost: %v %s", err, out)
		}
	}
}

func TestKagentEvaluationNullAndUnknownAtEveryScope(t *testing.T) {
	scopes := []struct {
		name string
		get  func(map[string]any) map[string]any
	}{
		{"root", func(r map[string]any) map[string]any { return r }},
		{"target", kagentWireTarget}, {"platform", kagentWirePlatform}, {"snapshot", kagentWireSnapshot}, {"case", kagentWireCase}, {"execution", kagentWireExecution}, {"assertion", kagentWireRow},
		{"controller image", func(r map[string]any) map[string]any {
			return kagentWirePlatform(r)["controllerImage"].(map[string]any)
		}},
		{"ADK image", func(r map[string]any) map[string]any { return kagentWirePlatform(r)["adkImage"].(map[string]any) }},
		{"controller identity", func(r map[string]any) map[string]any {
			return kagentWirePlatform(r)["controller"].([]any)[0].(map[string]any)
		}},
		{"proof", func(r map[string]any) map[string]any {
			return kagentWireSnapshot(r)["proofs"].([]any)[0].(map[string]any)
		}},
		{"workload", func(r map[string]any) map[string]any {
			return kagentWireSnapshot(r)["workload"].([]any)[0].(map[string]any)
		}},
		{"dependency", func(r map[string]any) map[string]any {
			return kagentWireSnapshot(r)["dependencies"].([]any)[0].(map[string]any)
		}},
	}
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			var original map[string]any
			if err := json.Unmarshal([]byte(kagentEvaluationWire), &original); err != nil {
				t.Fatal(err)
			}
			for field := range scope.get(original) {
				raw := kagentWireMutation(t, func(r map[string]any) { scope.get(r)[field] = nil })
				if _, err := readEvaluationDiffSource(diffWriteRaw(t, raw)); err == nil {
					t.Fatalf("null %s accepted", field)
				}
			}
			raw := kagentWireMutation(t, func(r map[string]any) { scope.get(r)["payload"] = "PRIVATE_PAYLOAD_CANARY" })
			var out bytes.Buffer
			err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, raw), After: diffWriteRaw(t, []byte(kagentEvaluationWire))})
			if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
				t.Fatalf("unknown field accepted/leaked: %v %s", err, out.String())
			}
		})
	}
}

func TestKagentEvaluationToolsAndJudgeRefusals(t *testing.T) {
	for _, kind := range []string{"toolCalled", "toolNotCalled"} {
		r := kagentEvaluationFixture(t)
		tool := r.Cases[0].Assertions[0]
		tool.ID = "tool-one"
		tool.Type = kind
		tool.Verdict = agentruntime.EvaluationUnknown
		tool.Reason = "runtime does not support tools"
		r.Cases[0].Assertions = append(r.Cases[0].Assertions, tool)
		r.Cases[0].Verdict = "unknown"
		r.Result = "unknown"
		if _, err := readEvaluationDiffSource(diffWrite(t, r)); err != nil {
			t.Fatalf("unknown tool evidence refused: %v", err)
		}
		r.Cases[0].Assertions[0].Verdict = agentruntime.EvaluationFail
		r.Cases[0].Assertions[0].Reason = "assertion not satisfied"
		r.Cases[0].Verdict = "fail"
		r.Result = "fail"
		if n, err := readEvaluationDiffSource(diffWrite(t, r)); err != nil || n.Result != "fail" {
			t.Fatalf("failure-over-unknown lost: %v %+v", err, n)
		}
	}
	for _, mutate := range []func(*agentruntime.EvaluationAssertionResult){
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge = nil },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Model = "PRIVATE\nMODEL_CANARY" },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.ModelVersion = "" },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Provenance = "provider-attested" },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.EvaluatorDigest = strings.Repeat("f", 64) },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.ConfigurationDigest = "bad" },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.MinScore = 101 },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Scores = []int{100} },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Scores = []int{101, 100} },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Scores = []int{0, 20} },
		func(row *agentruntime.EvaluationAssertionResult) { row.Judge.Scores = nil },
		func(row *agentruntime.EvaluationAssertionResult) { row.Type = "contains" },
	} {
		r := kagentEvaluationFixture(t)
		row := integrationJudgeRow([]int{80, 100})
		mutate(&row)
		r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row}
		out, err := diffExecute(t, r, r)
		if err == nil || err.Error() != "invalid evaluation receipt" || out != "" {
			t.Fatalf("judge forgery accepted/leaked: %v %s", err, out)
		}
	}
	r := kagentEvaluationFixture(t)
	r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{integrationJudgeRow([]int{80, 100})}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{`"minScore":null`, `"minScore":"50"`, `"minScore":50,"scores":[null,null]`} {
		changed := strings.Replace(string(raw), `"minScore":50`, replacement, 1)
		if _, err := readEvaluationDiffSource(diffWriteRaw(t, []byte(changed))); err == nil {
			t.Fatal("judge raw type/null/duplicate hazard accepted")
		}
	}
	changed := strings.Replace(string(raw), `"minScore":50,`, "", 1)
	if _, err := readEvaluationDiffSource(diffWriteRaw(t, []byte(changed))); err == nil {
		t.Fatal("missing mandatory judge threshold accepted")
	}
}

func TestKagentEvaluationReceiptSizeBoundaries(t *testing.T) {
	raw := []byte(kagentEvaluationWire)
	path := diffWriteRaw(t, append(raw, bytes.Repeat([]byte(" "), maxSessionsReceiptBytes-len(raw))...))
	if _, err := readEvaluationDiffSource(path); err != nil {
		t.Fatalf("exact 1MiB receipt refused: %v", err)
	}
	bundle := diffTempDir(t)
	if err := os.Mkdir(filepath.Join(bundle, "receipts"), 0700); err != nil {
		t.Fatal(err)
	}
	latest := filepath.Join(bundle, "receipts", "eval-bounded.json")
	if err := os.WriteFile(latest, append(raw, bytes.Repeat([]byte(" "), maxSessionsReceiptBytes-len(raw))...), 0600); err != nil {
		t.Fatal(err)
	}
	if evidence, err := readBundleGateEvidence(bundle); err != nil || len(evidence) != 0 {
		t.Fatalf("exact-bound foreign evidence poisoned gate: %v", err)
	}
	if err := os.WriteFile(latest, append(raw, bytes.Repeat([]byte(" "), maxSessionsReceiptBytes-len(raw)+1)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBundleGateEvidence(bundle); err == nil {
		t.Fatal("over-bound latest accepted")
	}
	r := kagentEvaluationFixture(t)
	for i := 1; i < 300; i++ {
		row := r.Cases[0].Assertions[0]
		row.ID = fmt.Sprintf("assert-%d", i)
		r.Cases[0].Assertions = append(r.Cases[0].Assertions, row)
	}
	first := r.Cases[0]
	r.Cases = nil
	for i := range 20 {
		c := first
		c.ID = fmt.Sprintf("case-%d", i)
		c.Execution = kagentEvaluationExecution{RPCID: fmt.Sprintf("rpc-%d", i), RequestMessageID: fmt.Sprintf("message-%d", i), TaskID: fmt.Sprintf("task-%d", i), ContextID: fmt.Sprintf("context-%d", i), State: "completed"}
		r.Cases = append(r.Cases, c)
	}
	if _, ok := normalizeKagentEvaluation(r); !ok {
		t.Fatal("oversized fixture is not semantically valid")
	}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil || len(encoded) <= maxSessionsReceiptBytes {
		t.Fatalf("oversized test did not exceed bound: %d %v", len(encoded), err)
	}
	cleanBundle := diffTempDir(t)
	if err := writeKagentEvaluationReceipt(cleanBundle, r); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized evidence persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cleanBundle, "receipts")); !os.IsNotExist(err) {
		t.Fatal("size refusal mutated disk")
	}
}

func TestKagentEvaluationPersistence(t *testing.T) {
	bundle := diffTempDir(t)
	r := kagentEvaluationFixture(t)
	orkaPath := bundleEvaluationReceiptPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
	if err := os.Mkdir(filepath.Dir(orkaPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orkaPath, []byte("Orka latest must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeKagentEvaluationReceipt(bundle, r); err != nil {
		t.Fatal(err)
	}
	latest := kagentEvaluationLatestPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
	if latest == orkaPath || !strings.HasPrefix(filepath.Base(latest), "eval-kagent-v0.10.3-") {
		t.Fatalf("latest namespace collision: %s", latest)
	}
	archive := evaluationHistoryPath(bundle, r.RunID)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	latestRaw, err := os.ReadFile(latest)
	if err != nil || !bytes.Equal(raw, latestRaw) {
		t.Fatalf("archive/latest differ: %v", err)
	}
	if _, err := readEvaluationDiffSource(archive); err != nil {
		t.Fatal(err)
	}
	if old, err := os.ReadFile(orkaPath); err != nil || string(old) != "Orka latest must survive" {
		t.Fatal("Orka latest overwritten")
	}
	for _, path := range []string{filepath.Join(bundle, "receipts"), filepath.Join(bundle, "receipts", "runs"), archive, latest} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("nonprivate %s: %v", path, info.Mode())
		}
	}
	kagentEvaluationVerdict(&r, "fail", "")
	if err := writeKagentEvaluationReceipt(bundle, r); err == nil {
		t.Fatal("archive overwritten")
	}
	again, _ := os.ReadFile(archive)
	if !bytes.Equal(raw, again) {
		t.Fatal("immutable archive changed")
	}
	r.RunID = strings.Repeat("2", 32)
	if err := writeKagentEvaluationReceipt(bundle, r); err != nil {
		t.Fatal(err)
	}
	n, err := readEvaluationDiffSource(latest)
	if err != nil || n.Result != "fail" {
		t.Fatalf("latest not advanced: %v %+v", err, n)
	}
	for _, key := range []struct{ context, namespace, cluster string }{{"alias", r.Target.Namespace, r.Target.ClusterUID}, {r.Target.Context, "other", r.Target.ClusterUID}, {r.Target.Context, r.Target.Namespace, "other"}} {
		if latest == kagentEvaluationLatestPath(bundle, key.context, key.namespace, key.cluster) {
			t.Fatal("target identity path collision")
		}
	}
}

func TestKagentEvaluationPersistenceFailsClosed(t *testing.T) {
	t.Run("validation before writes", func(t *testing.T) {
		bundle := diffTempDir(t)
		r := kagentEvaluationFixture(t)
		r.Cases[0].Execution.TaskID = ""
		if err := writeKagentEvaluationReceipt(bundle, r); err == nil {
			t.Fatal("invalid receipt persisted")
		}
		if _, err := os.Stat(filepath.Join(bundle, "receipts")); !os.IsNotExist(err) {
			t.Fatal("invalid evidence mutated disk")
		}
	})
	for _, failure := range []string{"latest link", "archive exists", "runs link"} {
		t.Run(failure, func(t *testing.T) {
			bundle := diffTempDir(t)
			r := kagentEvaluationFixture(t)
			if err := os.MkdirAll(filepath.Join(bundle, "receipts", "runs"), 0700); err != nil {
				t.Fatal(err)
			}
			latest := kagentEvaluationLatestPath(bundle, r.Target.Context, r.Target.Namespace, r.Target.ClusterUID)
			archive := evaluationHistoryPath(bundle, r.RunID)
			outside := filepath.Join(diffTempDir(t), "outside.json")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "latest link":
				if err := os.Symlink(outside, latest); err != nil {
					t.Fatal(err)
				}
			case "archive exists":
				if err := os.WriteFile(archive, []byte("existing archive"), 0600); err != nil {
					t.Fatal(err)
				}
			case "runs link":
				if err := os.Remove(filepath.Dir(archive)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(archive)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeKagentEvaluationReceipt(bundle, r); err == nil {
				t.Fatal("unsafe publication succeeded")
			}
			if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
				t.Fatal("outside file changed")
			}
			if failure == "latest link" {
				if _, err := readEvaluationDiffSource(archive); err != nil {
					t.Fatalf("completed archive lost after latest failure: %v", err)
				}
			}
			if failure == "archive exists" {
				if raw, err := os.ReadFile(archive); err != nil || string(raw) != "existing archive" {
					t.Fatal("archive changed")
				}
				if _, err := os.Stat(latest); !os.IsNotExist(err) {
					t.Fatal("latest updated despite archive failure")
				}
			}
		})
	}
}
