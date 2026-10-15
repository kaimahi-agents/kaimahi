package agentsuite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedEvaluationsAcceptDirectoryAndOCI(t *testing.T) {
	root, _ := writeEvaluationSuite(t, nil)
	for _, source := range []string{root, writeTestOCILayout(t, root)} {
		if _, err := ValidatePath(source); err != nil {
			t.Fatalf("ValidatePath(%s): %v", source, err)
		}
	}
}

func TestEvaluationReferenceSchemas(t *testing.T) {
	indexSchema := compileReferenceSchema(t, "eval-index.schema.json")
	data, err := os.ReadFile(filepath.Join("testdata", "coordinator-workers", "evals", "writer", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, indexSchema, string(data), true)
	for _, invalid := range []string{
		strings.Replace(string(data), `"maxCases": 128`, `"maxCases": 129`, 1),
		strings.Replace(string(data), `"maxCaseBytes": 4194304`, `"maxCaseBytes": 0`, 1),
		strings.Replace(string(data), `"limits":`, `"results":[],"limits":`, 1),
	} {
		validateSchemaJSON(t, indexSchema, invalid, false)
	}

	agentSchema := compileReferenceSchema(t, "agent.schema.json")
	data, err = os.ReadFile(filepath.Join("testdata", "coordinator-workers", "agents", "writer.json"))
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, agentSchema, string(data), true)
	var agent map[string]any
	if err := json.Unmarshal(data, &agent); err != nil {
		t.Fatal(err)
	}
	agent["evals"] = nil
	invalid, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, agentSchema, string(invalid), false)
}

func TestEmbeddedEvaluationsRejectInvalidGraph(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *EvaluationIndex)
		want   string
	}{
		{"wrong agent", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Agent = "reviewer" }, "owning agent"},
		{"empty cases", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Cases = nil }, "case count"},
		{"too many cases", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Limits.MaxCases = 1 }, "case count"},
		{"invalid count limit", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Limits.MaxCases = 129 }, "evaluation limits"},
		{"invalid size limit", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Limits.MaxCaseBytes = maxJSONBytes + 1 }, "evaluation limits"},
		{"case too large", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Limits.MaxCaseBytes = 1 }, "maxCaseBytes"},
		{"duplicate reference", func(_ *testing.T, _ string, index *EvaluationIndex) { index.Cases[1] = index.Cases[0] }, "duplicate evaluation case"},
		{"case digest", func(_ *testing.T, _ string, index *EvaluationIndex) {
			index.Cases[0].Digest = "sha256:" + strings.Repeat("0", 64)
		}, "digest mismatch"},
		{"cross agent path", func(_ *testing.T, _ string, index *EvaluationIndex) {
			index.Cases[0].Path = "evals/reviewer/cases/capital.json"
		}, "owning agent"},
		{"duplicate ids", func(t *testing.T, root string, index *EvaluationIndex) {
			data := evaluationFixture(t, "capital")
			index.Cases[1].Digest = mustWriteJSONBytes(t, root, index.Cases[1].Path, data)
		}, "id is invalid or duplicated"},
		{"missing case", func(t *testing.T, root string, index *EvaluationIndex) {
			if err := os.Remove(filepath.Join(root, index.Cases[0].Path)); err != nil {
				t.Fatal(err)
			}
		}, "is missing"},
		{"orphan agent", func(t *testing.T, root string, _ *EvaluationIndex) {
			mustWrite(t, root, "evals/missing/index.json", []byte("{}"))
		}, "orphaned"},
		{"unreferenced case", func(t *testing.T, root string, _ *EvaluationIndex) {
			mustWrite(t, root, "evals/writer/cases/extra.json", evaluationFixture(t, "capital"))
		}, "unreferenced"},
		{"embedded result", func(t *testing.T, root string, _ *EvaluationIndex) {
			mustWrite(t, root, "evals/writer/results/result.json", []byte("{}"))
		}, "unreferenced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := writeEvaluationSuite(t, func(root string, index *EvaluationIndex) { tt.mutate(t, root, index) })
			_, err := ValidatePath(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEmbeddedEvaluationAgentBinding(t *testing.T) {
	for _, tt := range []struct {
		name string
		refs func(FileRef) any
		want string
	}{
		{"null", func(FileRef) any { return nil }, "must be an array"},
		{"wrong path", func(ref FileRef) any {
			ref.Path = "evals/reviewer/index.json"
			return []FileRef{ref}
		}, "eval index must be"},
		{"bad index digest", func(ref FileRef) any {
			ref.Digest = "sha256:" + strings.Repeat("0", 64)
			return []FileRef{ref}
		}, "digest mismatch"},
		{"multiple indexes", func(ref FileRef) any { return []FileRef{ref, ref} }, "at most one"},
		{"unbound tree", func(FileRef) any { return []FileRef{} }, "unreferenced"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, ref := writeEvaluationSuite(t, nil)
			bindEvaluationAgent(t, root, tt.refs(ref))
			_, err := ValidatePath(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvaluationAssertionsMatchSchemasAndSemantics(t *testing.T) {
	schema := compileReferenceSchema(t, "eval-case.schema.json")
	tests := []struct {
		name      string
		assertion string
		valid     bool
	}{
		{"contains", `{"id":"a","type":"contains","value":"Paris"}`, true},
		{"notContains", `{"id":"a","type":"notContains","value":"London"}`, true},
		{"regex", `{"id":"a","type":"regex","pattern":"\\b4\\b"}`, true},
		{"toolCalled", `{"id":"a","type":"toolCalled","tool":"search"}`, true},
		{"toolNotCalled", `{"id":"a","type":"toolNotCalled","tool":"delete"}`, true},
		{"judge", `{"id":"a","type":"llmJudge","rubric":"Be correct.","minScore":80,"judgeModel":"example","judgeVersion":"v1"}`, true},
		{"extension", `{"id":"a","type":"example.custom","critical":false,"value":"policy-v1"}`, true},
		{"known non-critical", `{"id":"a","type":"contains","critical":false,"value":"Paris"}`, true},
		{"unknown critical", `{"id":"a","type":"example.custom","critical":true,"value":"policy-v1"}`, false},
		{"unknown implicit critical", `{"id":"a","type":"example.custom","value":"policy-v1"}`, false},
		{"unknown property", `{"id":"a","type":"contains","value":"Paris","token":"not-a-credential"}`, false},
		{"irrelevant empty field", `{"id":"a","type":"contains","value":"Paris","pattern":""}`, false},
		{"null critical", `{"id":"a","type":"contains","value":"Paris","critical":null}`, false},
		{"blank value", `{"id":"a","type":"contains","value":" "}`, false},
		{"control value", `{"id":"a","type":"contains","value":"Paris\n"}`, false},
		{"line separator value", `{"id":"a","type":"contains","value":"Paris\u2028London"}`, false},
		{"paragraph separator value", `{"id":"a","type":"notContains","value":"Paris\u2029London"}`, false},
		{"line separator tool", `{"id":"a","type":"toolCalled","tool":"search\u2028results"}`, false},
		{"paragraph separator tool", `{"id":"a","type":"toolNotCalled","tool":"search\u2029results"}`, false},
		{"missing id", `{"type":"contains","value":"Paris"}`, false},
		{"missing value", `{"id":"a","type":"contains"}`, false},
		{"missing judge version", `{"id":"a","type":"llmJudge","rubric":"Be correct.","minScore":80,"judgeModel":"example"}`, false},
		{"bad judge score", `{"id":"a","type":"llmJudge","rubric":"Be correct.","minScore":101,"judgeModel":"example","judgeVersion":"v1"}`, false},
		{"non-ASCII judge identity", `{"id":"a","type":"llmJudge","rubric":"Be correct.","minScore":80,"judgeModel":"mod\u00e8le","judgeVersion":"v1"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(`{"schemaVersion":"1.0.0-draft","mediaType":"application/vnd.agentsuite.eval.case.v1+json","id":"case","input":"Question","assertions":[` + tt.assertion + `]}`)
			validateSchemaJSON(t, schema, string(data), tt.valid)
			root, _ := writeEvaluationSuite(t, func(root string, index *EvaluationIndex) {
				index.Cases = []FileRef{{Path: "evals/writer/cases/case.json", Digest: mustWriteJSONBytes(t, root, "evals/writer/cases/case.json", data)}}
				for _, name := range []string{"capital", "arithmetic"} {
					if err := os.Remove(filepath.Join(root, "evals/writer/cases/"+name+".json")); err != nil {
						t.Fatal(err)
					}
				}
			})
			_, err := ValidatePath(root)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidatePath error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestEvaluationCaseRunPolicy(t *testing.T) {
	schema := compileReferenceSchema(t, "eval-case.schema.json")
	tests := []struct {
		name        string
		fields      string
		schemaValid bool
		valid       bool
	}{
		{"default single run", "", true, true},
		{"all runs must pass", `,"runs":3`, true, true},
		{"minimum passes", `,"runs":3,"minPasses":2`, true, true},
		{"one run one pass", `,"runs":1,"minPasses":1`, true, true},
		{"zero runs", `,"runs":0`, false, false},
		{"null runs", `,"runs":null`, false, false},
		{"min passes without runs", `,"minPasses":1`, false, false},
		{"zero min passes", `,"runs":3,"minPasses":0`, false, false},
		{"null min passes", `,"runs":3,"minPasses":null`, false, false},
		{"min passes exceeds runs", `,"runs":2,"minPasses":3`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(`{"schemaVersion":"1.0.0-draft","mediaType":"application/vnd.agentsuite.eval.case.v1+json","id":"case","input":"Question"` + tt.fields + `,"assertions":[{"id":"answer","type":"contains","value":"yes"}]}`)
			validateSchemaJSON(t, schema, string(data), tt.schemaValid)
			root, _ := writeEvaluationSuite(t, func(root string, index *EvaluationIndex) {
				index.Cases = []FileRef{{Path: "evals/writer/cases/case.json", Digest: mustWriteJSONBytes(t, root, "evals/writer/cases/case.json", data)}}
				for _, name := range []string{"capital", "arithmetic"} {
					if err := os.Remove(filepath.Join(root, "evals/writer/cases/"+name+".json")); err != nil {
						t.Fatal(err)
					}
				}
			})
			_, err := ValidatePath(root)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidatePath error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestEvaluationCaseStrictness(t *testing.T) {
	base := string(evaluationFixture(t, "capital"))
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{"invalid regex", strings.Replace(base, `{"id": "capital", "type": "contains", "value": "Paris"}`, `{"id":"capital","type":"regex","pattern":"["}`, 1), "RE2"},
		{"duplicate assertions", strings.Replace(base, `"id": "not-london"`, `"id": "capital"`, 1), "assertion id"},
		{"duplicate JSON keys", strings.Replace(base, `"input":`, `"input":"duplicate","input":`, 1), "duplicate JSON key"},
		{"unknown field", strings.Replace(base, `"input":`, `"transcript":[],"input":`, 1), "unknown field"},
		{"empty input", strings.Replace(base, "What is the capital of France?", " ", 1), "requires input"},
		{"wrong case id", strings.Replace(base, `"id": "capital"`, `"id":"other"`, 1), "filename"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := writeEvaluationSuite(t, func(root string, index *EvaluationIndex) {
				// Use a raw-byte digest for malformed JSON, which cannot have a JCS identity.
				mustWrite(t, root, index.Cases[0].Path, []byte(tt.data))
				digest, err := canonicalDigest([]byte(tt.data))
				if err != nil {
					digest = digestBytes([]byte(tt.data))
				}
				index.Cases[0].Digest = digest
			})
			_, err := ValidatePath(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvaluationIdentityCommitsToCasesAndOrder(t *testing.T) {
	root, before := writeEvaluationSuite(t, nil)
	afterRoot, after := writeEvaluationSuite(t, func(root string, index *EvaluationIndex) {
		data := strings.Replace(string(evaluationFixture(t, "capital")), "Paris", "PARIS", 1)
		index.Cases[0].Digest = mustWriteJSONBytes(t, root, index.Cases[0].Path, []byte(data))
	})
	if before.Digest == after.Digest {
		t.Fatal("case edit did not change index identity")
	}
	report, err := DiffPaths(root, afterRoot)
	if err != nil {
		t.Fatal(err)
	}
	assertAffectedAgentReason(t, report, "writer", "agent definition modified")
	content, err := loadDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	var index EvaluationIndex
	data, err := content.data(before.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index.Cases[0], index.Cases[1] = index.Cases[1], index.Cases[0]
	swapped := mustWriteJSON(t, root, before.Path, index)
	if swapped == before.Digest {
		t.Fatal("case order did not change index identity")
	}
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("stale agent binding accepted: %v", err)
	}
}

func TestEvaluationJCSIdentityIgnoresJSONFormatting(t *testing.T) {
	root, _ := writeEvaluationSuite(t, nil)
	var c EvaluationCase
	if err := Unmarshal(evaluationFixture(t, "capital"), &c); err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, "evals/writer/cases/capital.json", data)
	if _, err := ValidatePath(root); err != nil {
		t.Fatalf("JCS-equivalent formatting invalidated the binding: %v", err)
	}
}

func writeEvaluationSuite(t *testing.T, mutate func(string, *EvaluationIndex)) (string, FileRef) {
	t.Helper()
	root := writeMinimalSuite(t)
	index := EvaluationIndex{
		SchemaVersion: SpecVersion, MediaType: MediaTypeEvaluationIndex, Agent: "writer",
		Limits: EvaluationLimits{MaxCases: 128, MaxCaseBytes: maxJSONBytes},
	}
	for _, name := range []string{"capital", "arithmetic"} {
		path := "evals/writer/cases/" + name + ".json"
		index.Cases = append(index.Cases, FileRef{Path: path, Digest: mustWriteJSONBytes(t, root, path, evaluationFixture(t, name))})
	}
	if mutate != nil {
		mutate(root, &index)
	}
	ref := FileRef{Path: "evals/writer/index.json"}
	ref.Digest = mustWriteJSON(t, root, ref.Path, index)
	bindEvaluationAgent(t, root, []FileRef{ref})
	return root, ref
}

func bindEvaluationAgent(t *testing.T, root string, refs any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "agents/writer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var agent map[string]any
	if err := json.Unmarshal(data, &agent); err != nil {
		t.Fatal(err)
	}
	agent["evals"] = refs
	digest := mustWriteJSON(t, root, "agents/writer.json", agent)
	data, err = os.ReadFile(filepath.Join(root, "agentsuite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite Suite
	if err := Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	suite.Agents[0].Digest = digest
	mustWriteJSON(t, root, "agentsuite.json", suite)
}

func evaluationFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "coordinator-workers", "evals", "writer", "cases", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustWriteJSONBytes(t *testing.T, root, path string, data []byte) string {
	t.Helper()
	mustWrite(t, root, path, data)
	digest, err := canonicalDigest(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
