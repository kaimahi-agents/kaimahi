package agentfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests catch lossy projection, volatile paths, changed hash preimages,
// invented target results and accepting files canonical Run/Test would refuse.
func TestExportRunProfilePreservesCanonicalSnapshot(t *testing.T) {
	source := strings.Replace(sample(t), "input: Ada", "input: '<Ada>&'", 1)
	source = strings.Replace(source, "        value: Hello, Ada\n", "        value: Hello, Ada\n      - id: absent\n        type: notContains\n        value: Goodbye\n      - id: shape\n        type: regex\n        pattern: '^Hello, '\n      - id: judge\n        type: llmJudge\n        rubric: Is this a greeting?\n        minScore: 80\n        judgeModel: authored-model\n        judgeVersion: v1\n  - id: second\n    input: Grace\n    assertions:\n      - id: greeting-text\n        type: contains\n        value: Grace\n", 1)
	path := exportSource(t, source)
	data, err := ExportRunProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ExportRunProfile(path)
	if err != nil || !bytes.Equal(data, again) || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("export is not deterministic complete newline-terminated JSON", err)
	}
	var got struct {
		SchemaVersion  int                           `json:"schemaVersion"`
		Kind           string                        `json:"kind"`
		SourceRevision string                        `json:"sourceRevision"`
		AgentID        string                        `json:"agentId"`
		Instructions   struct{ Text, SHA256 string } `json:"instructions"`
		Model          struct{ ID, SHA256 string }   `json:"model"`
		Examples       []struct {
			ID, Input, SHA256, TargetStatus string
			Assertions                      []json.RawMessage
		} `json:"examples"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	instructions := strings.Split(source, "## Examples")[0]
	if got.SchemaVersion != 1 || got.Kind != "kmx.agent-run-profile" || got.AgentID != "greeter" || got.SourceRevision != exportedDigest([]byte(source)) {
		t.Fatal("wrong source-file identity")
	}
	if got.Instructions.Text != instructions || got.Instructions.SHA256 != exportedDigest([]byte(instructions)) || got.Model.ID != "fake" || got.Model.SHA256 != exportedDigest([]byte("fake")) {
		t.Fatal("instructions/model changed or have wrong decoded-byte hashes")
	}
	if len(got.Examples) != 2 || got.Examples[0].ID != "greeting" || got.Examples[0].Input != "<Ada>&" || got.Examples[1].ID != "second" || got.Examples[1].Input != "Grace" || len(got.Examples[0].Assertions) != 4 {
		t.Fatal("example ordering or authored data changed")
	}
	// Literal compact preimage pins field order, assertion IDs, HTML escaping,
	// judge identity/threshold and the exclusion of target status/digest.
	preimage := `{"id":"greeting","input":"\u003cAda\u003e\u0026","assertions":[{"id":"greeting-text","type":"contains","value":"Hello, Ada"},{"id":"absent","type":"notContains","value":"Goodbye"},{"id":"shape","type":"regex","pattern":"^Hello, "},{"id":"judge","type":"llmJudge","rubric":"Is this a greeting?","minScore":80,"judgeModel":"authored-model","judgeVersion":"v1"}]}`
	if got.Examples[0].SHA256 != exportedDigest([]byte(preimage)) {
		t.Fatal("example hash lost definition fields or changed JSON encoding")
	}
	for _, example := range got.Examples {
		if example.TargetStatus != "not_run" {
			t.Fatal("export invented target execution")
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 7 {
		t.Fatal("unexpected envelope fields", err)
	}
	if bytes.Contains(data, []byte(path)) || bytes.Contains(data, []byte(`"Location"`)) || bytes.Contains(data, []byte(`"verdict"`)) {
		t.Fatal("export includes source locations or execution claims")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != source {
		t.Fatal("export changed its source", err)
	}
}

func TestExportRunProfileCRLFOnlyChangesRawRevision(t *testing.T) {
	lf, err := ExportRunProfile(exportSource(t, sample(t)))
	if err != nil {
		t.Fatal(err)
	}
	crlf, err := ExportRunProfile(exportSource(t, strings.ReplaceAll(sample(t), "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal(lf, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(crlf, &b); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a["sourceRevision"], b["sourceRevision"]) {
		t.Fatal("raw-byte revision normalized CRLF")
	}
	delete(a, "sourceRevision")
	delete(b, "sourceRevision")
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(aa, bb) {
		t.Fatal("canonical payload differs for CRLF source")
	}
}

func TestExportRunProfileCanonicalRefusals(t *testing.T) {
	for _, row := range []struct{ source, field string }{
		{"# Display-only\n\nNot a runnable canonical charter.\n", "Sections"},
		{strings.Replace(sample(t), "## Model", "## Tools\nPRIVATE_EXPORT_CANARY\n\n## Model", 1), "Sections"},
		{strings.Replace(sample(t), "**Preferred:** fake", "**Preferred:** fake\n- **Fallback:** PRIVATE_EXPORT_CANARY", 1), "Model"},
		{strings.Replace(sample(t), "input: Ada", "input: Ada\n    PRIVATE_EXPORT_CANARY: value", 1), "Examples"},
	} {
		data, err := ExportRunProfile(exportSource(t, row.source))
		var refusal *Error
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Field, row.field) || len(data) != 0 || strings.Contains(err.Error(), "PRIVATE_EXPORT_CANARY") {
			t.Fatal("canonical refusal lost or authored data disclosed", err)
		}
	}
}

func TestExportRunProfileHashesBindAuthoredDefinitions(t *testing.T) {
	base := sample(t)
	decode := func(source string) map[string]json.RawMessage {
		t.Helper()
		data, err := ExportRunProfile(exportSource(t, source))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	original := decode(base)
	for _, change := range []struct{ name, old, value, field string }{
		{"example ID", "id: greeting\n", "id: revised\n", "examples"},
		{"input", "input: Ada", "input: Grace", "examples"},
		{"assertion ID", "id: greeting-text", "id: revised-check", "examples"},
		{"operand", "value: Hello, Ada", "value: Hello, Grace", "examples"},
		{"authored model", "**Preferred:** fake", "**Preferred:** exact-model-v2", "model"},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := decode(strings.Replace(base, change.old, change.value, 1))
			if bytes.Equal(original["sourceRevision"], changed["sourceRevision"]) {
				t.Fatal("source change unbound")
			}
			var before, after struct{ SHA256 string }
			if change.field == "examples" {
				var a, b []struct{ SHA256 string }
				if err := json.Unmarshal(original["examples"], &a); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(changed["examples"], &b); err != nil {
					t.Fatal(err)
				}
				before.SHA256, after.SHA256 = a[0].SHA256, b[0].SHA256
			} else {
				if err := json.Unmarshal(original[change.field], &before); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(changed[change.field], &after); err != nil {
					t.Fatal(err)
				}
			}
			if before.SHA256 == after.SHA256 {
				t.Fatal("authored definition changed without its hash")
			}
		})
	}
	commented := decode(strings.Replace(base, "examples:", "# example-comment-canary\nexamples:", 1))
	if bytes.Equal(original["sourceRevision"], commented["sourceRevision"]) || !bytes.Equal(original["instructions"], commented["instructions"]) || !bytes.Equal(original["examples"], commented["examples"]) {
		t.Fatal("Examples YAML comments entered runtime payload or escaped raw identity")
	}
}

func TestExportRunProfileRetainsHighlyEscapedBoundedSource(t *testing.T) {
	source := strings.Replace(sample(t), "Reply with exactly `Hello, ` followed by the input name.", strings.Repeat("<>&", 80000), 1)
	data, err := ExportRunProfile(exportSource(t, source))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Instructions struct{ Text string } }
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Instructions.Text != strings.Split(source, "## Examples")[0] || len(data) > 2*1024*1024 {
		t.Fatal("bounded source was truncated or output exceeded its ceiling")
	}
}

func exportSource(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(realTemp(t), "greeter.agent.md")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func exportedDigest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
