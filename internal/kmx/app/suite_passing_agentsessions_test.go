package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func sessionsPassingFixture(t *testing.T) (*PreparedSuiteWorkspaceEvaluation, suiteSessionsSubject, []suiteSessionsObservation) {
	t.Helper()
	_, root, _ := gitWorkspaceRepository(t, false)
	member := filepath.Join(root, "agents/greeter/member.yaml")
	raw, err := os.ReadFile(member)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, member, strings.Split(string(raw), "\nruntime:")[0]+"\nruntime: {id: agentsessions, version: "+workspace.AgentSessionsVersion+", engine: chat}\n")
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		t.Fatal(err)
	}
	host := suiteSessionsHostObservation{Mode: "operator-observed-development", HostID: "host-1", JournalID: "journal-1", RuntimeVersion: workspace.AgentSessionsVersion, RuntimeBinaryDigest: "sha256:" + strings.Repeat("a", 64), RuntimeConfigDigest: "sha256:" + strings.Repeat("b", 64), InferenceID: "inference-1", InferenceRevision: "sha256:" + strings.Repeat("c", 64), Model: suite.Model, Harness: "chat", HarnessVersion: "1", Retention: "operator-managed"}
	subject, err := newSuiteSessionsSubject(suite, host)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]suiteSessionsObservation, len(inventory))
	for i, frame := range inventory {
		observations[i] = suiteSessionsObservation{Result: agentsessions.CaseResult{SessionUID: "session-" + frame.Case.ID, ExecutionID: "execution-" + frame.Case.ID, Harness: "chat", HarnessVersion: "1", Model: suite.Model, ConfigDigest: subject.ConfigDigest, Head: agentsessions.JournalHead{Seq: 5, Hash: strings.Repeat("d", 64)}, Output: "4"}, Assertions: agentruntime.EvaluateAssertions(frame.Case, agentruntime.EvaluationEvidence{Answer: "4", AnswerAvailable: true})}
	}
	return &PreparedSuiteWorkspaceEvaluation{prepared: prepared, suite: suite, inventory: inventory}, subject, observations
}

func TestSuiteSessionsPassingRoundTripAndCoherence(t *testing.T) {
	p, subject, observations := sessionsPassingFixture(t)
	r, err := newSuiteSessionsPassingReceipt(t.Context(), p, subject, observations)
	if err != nil {
		t.Fatal(err)
	}
	r.RunID = strings.Repeat("a", 32)
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, p.suite)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "346c72848dcd081cab135aa88342e97fc994e91d0948aa4206d37013e74944ae" {
		t.Fatalf("single-run Sessions byte golden: %s", got)
	}
	if r.suiteDigest() != p.suite.Digest || bytes.Contains(raw, []byte(`"subject":null`)) || bytes.Contains(raw, []byte(`"provider"`)) || bytes.Contains(raw, []byte(`"output"`)) {
		t.Fatalf("incorrect metadata projection: %s", raw)
	}
	var wire map[string]any
	if json.Unmarshal(raw, &wire) != nil {
		t.Fatal("invalid Sessions wire")
	}
	for _, c := range wire["cases"].([]any) {
		for _, a := range c.(map[string]any)["assertions"].([]any) {
			row := a.(map[string]any)
			if len(row) != 5 || row["evaluatorVersion"] != "kmx.text-assertions/v1" || row["judge"] != nil {
				t.Fatal("text-only Sessions assertion wire changed")
			}
		}
	}
	parsed, err := parseSuitePassingReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := canonicalSuitePassingReceipt(t.Context(), parsed, p.suite)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("round trip: %v", err)
	}
	if _, ok := normalizeSuitePassingEvaluation(parsed); ok {
		t.Fatal("Sessions diff accepted")
	}
	parsed.Sessions.Subject.ConfigDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := canonicalSuitePassingReceipt(t.Context(), parsed, p.suite); err == nil {
		t.Fatal("changed config accepted")
	}
}

func TestSuiteSessionsPassingRefusesIncompleteResults(t *testing.T) {
	for _, name := range []string{"partial", "model", "config", "harness", "version", "head", "execution", "failed", "mixed", "assertion"} {
		t.Run(name, func(t *testing.T) {
			p, s, o := sessionsPassingFixture(t)
			switch name {
			case "partial":
				o = nil
			case "model":
				o[0].Result.Model = "wrong"
			case "config":
				o[0].Result.ConfigDigest = ""
			case "harness":
				o[0].Result.Harness = "other"
			case "version":
				o[0].Result.HarnessVersion = ""
			case "head":
				o[0].Result.Head.Seq = 4
			case "execution":
				o[0].Result.ExecutionID = ""
			case "failed":
				o[0].Result.ExecutionFailed = true
			case "mixed":
				o[0].Result.ModelMixed = true
			case "assertion":
				o[0].Assertions[0].Verdict = agentruntime.EvaluationUnknown
			}
			if _, err := newSuiteSessionsPassingReceipt(t.Context(), p, s, o); err == nil {
				t.Fatal("incomplete proof accepted")
			}
		})
	}
}

func TestSuiteSessionsPassingClosedWire(t *testing.T) {
	p, s, o := sessionsPassingFixture(t)
	r, err := newSuiteSessionsPassingReceipt(t.Context(), p, s, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, p.suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"null", "dual", "unknown", "domain", "case-arm"} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "null":
				doc["agentsessions"] = nil
			case "dual":
				doc["kagent"] = map[string]any{}
			case "unknown":
				doc["unknown"] = true
			case "domain":
				doc["domain"] = "kmx.native-suite.passing/v1"
			case "case-arm":
				doc["cases"].([]any)[0].(map[string]any)["taskUID"] = "invented"
			}
			changed, _ := json.Marshal(doc)
			if _, err := parseSuitePassingReceipt(changed); err == nil {
				t.Fatal("invalid wire accepted")
			}
		})
	}
}
