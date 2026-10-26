package agentfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func githubReadmeRecording() Recording {
	return Recording{ExampleID: "greeting", Tool: MockTool{
		Name: "get_file_contents", Mode: "recorded",
		Description: "Get the contents of a file or directory from a GitHub repository",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"},
			"path": map[string]any{"type": "string", "default": "/"},
			"ref":  map[string]any{"type": "string"}, "sha": map[string]any{"type": "string"},
		}, "required": []any{"owner", "repo"}},
		Calls: []MockCall{{Arguments: map[string]any{"owner": "octocat", "repo": "Hello-World", "path": "README"},
			Result: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "successfully downloaded text file (SHA: 980a0d5f19a64b4b30a87d4206aade58726b60e3)"},
				map[string]any{"type": "resource", "resource": map[string]any{
					"mimeType": "text/plain; charset=utf-8", "text": "Hello World!\n",
					"uri": "repo://octocat/Hello-World/sha/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d/contents/README",
				}},
			}},
		}},
	}}
}

// The resource response is the real public README shape, not a flat text stub.
// Both source layouts must save its complete JSON meaning without losing bytes
// from other examples or changing the resource's trailing newline.
func TestApplyRecordingsGitHubReadme(t *testing.T) {
	for _, layout := range []string{"single-example", "three-example-release-notes"} {
		t.Run(layout, func(t *testing.T) {
			r := githubReadmeRecording()
			source := strings.ReplaceAll(recordingSource(t, pendingTool), "read", "get_file_contents")
			if layout == "three-example-release-notes" {
				prefix := strings.Split(sample(t), "## Examples")[0]
				source = prefix + "## Tools\n- get_file_contents\n\n## Examples\n```yaml\nexamples:\n" +
					"  - id: features-and-fixes\n    input: Add dark mode; Fix search\n    assertions: [{id: heading, type: contains, value: '## Features'}]\n" +
					"  - id: titles-without-numbers\n    input: Add CSV export\n    assertions: [{id: heading, type: contains, value: '## Features'}]\n" +
					"  - id: reads-a-public-README\n    input: Read octocat/Hello-World README and write release notes.\n" +
					"    assertions: [{id: heading, type: contains, value: '## Features'}]\n" +
					"    mocks:\n      tools:\n        - name: get_file_contents\n          mode: recorded\n          calls: []\n```\n"
				r.ExampleID = "reads-a-public-README"
			}
			for _, eol := range []string{"\n", "\r\n"} {
				original := []byte(strings.ReplaceAll(source, "\n", eol))
				for _, block := range []bool{true, false} {
					got, err := applyRecordings(original, []Recording{r}, block)
					if err != nil {
						t.Fatalf("block=%t eol=%q: %v", block, eol, err)
					}
					checkSaved(t, got, []Recording{r})
					before := strings.Split(string(original), "    mocks:")[0]
					if !strings.HasPrefix(string(got), before) {
						t.Fatal("recording changed preceding examples or prose")
					}
				}
				got, err := ApplyRecordings(original, []Recording{r})
				if err != nil {
					t.Fatal(err)
				}
				checkSaved(t, got, []Recording{r})
			}
		})
	}
}

// Root is depth zero; each object value or array element adds one level.
func payloadAtDepth(depth int, arrays bool) map[string]any {
	var value any = "leaf"
	for i := 1; i < depth; i++ {
		if arrays && i%2 == 0 {
			value = []any{value}
		} else {
			value = map[string]any{"next": value}
		}
	}
	return map[string]any{"next": value}
}

func TestRecordingPayloadDepth(t *testing.T) {
	for _, field := range []string{"arguments", "result"} {
		for _, arrays := range []bool{false, true} {
			for _, depth := range []int{32, 33} {
				t.Run(fmt.Sprintf("%s/arrays=%t/depth=%d", field, arrays, depth), func(t *testing.T) {
					r := readRecording("greeting", nil)
					r.Tool.Parameters = map[string]any{"type": "object"}
					if field == "arguments" {
						r.Tool.Calls[0].Arguments = payloadAtDepth(depth, arrays)
					} else {
						r.Tool.Calls[0].Result = payloadAtDepth(depth, arrays)
					}
					raw, err := json.Marshal(r.Tool)
					if err != nil {
						t.Fatal(err)
					}
					_, err = Parse("depth.agent.md", []byte(recordingSource(t, string(raw))))
					if depth == 33 {
						var refusal *Error
						if !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "32 recorded payload levels") {
							t.Fatalf("wrong depth refusal: %v", err)
						}
						refused(t, recordingSource(t, pendingTool), ErrRecordingInvalid, r)
						return
					}
					if err != nil {
						t.Fatalf("depth32 did not parse: %v", err)
					}
					got, err := ApplyRecordings([]byte(recordingSource(t, pendingTool)), []Recording{r})
					if err != nil {
						t.Fatalf("depth32 did not save: %v", err)
					}
					checkSaved(t, got, []Recording{r})
				})
			}
		}
	}
}

func TestRecordingBudgetDoesNotResetInsidePayload(t *testing.T) {
	r := readRecording("greeting", map[string]any{"examples": []any{map[string]any{
		"mocks": map[string]any{"tools": []any{map[string]any{"calls": []any{map[string]any{"result": payloadAtDepth(32, false)}}}}},
	}}})
	raw, _ := json.Marshal(r.Tool)
	_, err := Parse("depth.agent.md", []byte(recordingSource(t, string(raw))))
	var refusal *Error
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "32 recorded payload levels") {
		t.Fatalf("nested lookalike reset the budget: %v", err)
	}
}

func TestRecordingSchemaKeepsStructuralDepth(t *testing.T) {
	r := readRecording("greeting", nil)
	r.Tool.Parameters = map[string]any{"type": "object", "properties": map[string]any{"result": payloadAtDepth(10, false)}}
	raw, _ := json.Marshal(r.Tool)
	_, err := Parse("depth.agent.md", []byte(recordingSource(t, string(raw))))
	var refusal *Error
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "12 levels") {
		t.Fatalf("schema got a payload exemption: %v", err)
	}
}

func TestRecordingPayloadsShareNodeBudget(t *testing.T) {
	prefix := strings.Split(sample(t), "## Examples")[0] + "## Tools\n- read\n\n## Examples\n```yaml\nexamples:\n"
	var source strings.Builder
	source.WriteString(prefix)
	for i := range 3 {
		fmt.Fprintf(&source, "  - id: case-%d\n    input: Ada\n    assertions: [{id: a, type: contains, value: hello}]\n    mocks:\n      tools:\n      - {name: read, mode: recorded, description: Read, parameters: {type: object}, calls: [{arguments: {}, result: {rows: [%s0]}}]}\n", i, strings.Repeat("0,", 12000))
	}
	source.WriteString("```\n")
	_, err := Parse("nodes.agent.md", []byte(source.String()))
	var refusal *Error
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "32768") {
		t.Fatalf("payloads bypassed shared node count: %v", err)
	}
}
