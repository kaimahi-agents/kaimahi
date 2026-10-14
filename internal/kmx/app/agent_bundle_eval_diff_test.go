package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diffWrite(t *testing.T, receipt any) string {
	t.Helper()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return diffWriteRaw(t, raw)
}

func diffTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func diffWriteRaw(t *testing.T, raw []byte) string {
	t.Helper()
	dir := diffTempDir(t)
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func diffExecute(t *testing.T, before, after any) (string, error) {
	t.Helper()
	var out bytes.Buffer
	a := &App{Out: &out, Err: &out} // Deliberately no configuration or runner.
	err := a.DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWrite(t, before), After: diffWrite(t, after)})
	return out.String(), err
}

type evaluationDiffFailWriter struct{}

func (evaluationDiffFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("PRIVATE_WRITER_CANARY")
}

// Complete historical kagent evidence is refused, not normalized or migrated.
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

func TestEvaluationDiffRefusesHistoricalKagentReceipt(t *testing.T) {
	var out bytes.Buffer
	path := diffWriteRaw(t, []byte(kagentEvaluationWire))
	err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: path})
	if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
		t.Fatalf("old evidence accepted or disclosed: %v %s", err, out.String())
	}
}

func TestEvaluationDiffRefusesHistoricalSessionsReceipt(t *testing.T) {
	// Literal formerly passing evidence; no old schema, writer, or parser is retained.
	const receipt = `{"schemaVersion":2,"runID":"RUN_ID","bundle":"sample","portableDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","casesDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","fullCaseSet":true,"gitCommit":"uncommitted","target":{"runtime":"agentsessions","identity":{"version":1,"provenance":"host-reported","address":"Example.COM.:08080","harness":"chat","model":"local/model:1"}},"result":"pass","cases":[{"id":"case-one","verdict":"pass","caseDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","inputDigest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","sessionUID":"session-1","harness":"chat","model":"local/model:1","journalHead":{"seq":5,"hash":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},"answerSHA256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","assertions":[{"id":"assert-one","type":"contains","definitionDigest":"0000000000000000000000000000000000000000000000000000000000000000","verdict":"pass","reason":"assertion satisfied"}]}]}`
	var out bytes.Buffer
	path := diffWriteRaw(t, []byte(strings.ReplaceAll(receipt, "RUN_ID", strings.Repeat("1", 32))))
	err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: path})
	if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
		t.Fatalf("old evidence accepted or disclosed: %v %s", err, out.String())
	}
}
