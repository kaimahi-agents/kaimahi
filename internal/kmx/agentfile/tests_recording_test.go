package agentfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

const testPendingTool = "name: read\n      mode: recorded\n      calls: []"

func recordingTestFence(id, tool string) string {
	return "```yaml agent-test\nid: " + id + "\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks:\n  tools:\n    - " + tool + "\n```\n"
}

func checkTestRecordings(t *testing.T, source, want string, recordings ...Recording) []byte {
	t.Helper()
	original := []byte(source)
	got, err := ApplyTestRecordings(original, recordings)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(original) != source {
		t.Fatal("mutated input bytes")
	}
	if want != "" && string(got) != want {
		t.Fatalf("wrong sparse patch\n got: %q\nwant: %q", got, want)
	}
	suite, err := ParseTests("suite.agent.test.md", got)
	if err != nil {
		t.Fatalf("patched suite: %v", err)
	}
	for _, r := range recordings {
		found := false
		for _, e := range suite.Examples {
			for _, tool := range mocksOf(e) {
				if e.ID == r.ExampleID && tool.Name == r.Tool.Name {
					found = true
					actual, _ := json.Marshal(tool)
					expected, _ := json.Marshal(r.Tool)
					// ParseTests exposes decoded JSON numbers, not their source lexemes.
					var decoded MockTool
					if err := json.Unmarshal(expected, &decoded); err != nil {
						t.Fatal(err)
					}
					expected, _ = json.Marshal(decoded)
					if !bytes.Equal(actual, expected) {
						t.Fatalf("fixture changed: got %s want %s", actual, expected)
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing saved fixture %s/%s", r.ExampleID, r.Tool.Name)
		}
	}
	return got
}

func refuseTestRecordings(t *testing.T, source string, want error, recordings ...Recording) {
	t.Helper()
	original := []byte(source)
	got, err := ApplyTestRecordings(original, recordings)
	if got != nil || !errors.Is(err, want) || string(original) != source {
		t.Fatalf("want %v, nil output, unchanged input; got %v, %d bytes", want, err, len(got))
	}
}

func TestApplyTestRecordingsExactMultiFence(t *testing.T) {
	untouched := recordingTestFence("saved", `{name: read, mode: recorded, description: Read, parameters: {type: object}, calls: [{arguments: {}, result: {n: 1.0, exp: 1e0, zero: -0.0}}]}`)
	block := recordingTestFence("block", testPendingTool+" # keep é 🙂")
	flow := "~~~yaml agent-test\n{input: 'Ādā 🙂 é', id: flow, mocks: {tools: [{name: read, mode: recorded, calls: []}, {name: other, mode: live}]}, assertions: [{id: a, type: contains, value: Ada}]}\n~~~\n"
	prefix := testsHeader + "# Notes é 🙂\n```text\ncalls: []\n```\n" + untouched + "\nBetween cases.\n"
	blockSaved := `description: Read rows
      parameters:
        additionalProperties: false
        properties:
          query:
            type: string
        required:
          - query
        type: object
      calls:
        - arguments:
            query: release
          result:
            complete: true`
	flowSaved := readDefinitions + `, "parameters": ` + readParamsJSON + ", calls: [" + readCallJSON + "]"
	for _, endings := range []string{"LF", "CRLF", "mixed"} {
		t.Run(endings, func(t *testing.T) {
			source := prefix + block + "More prose.\n" + flow + "End.\n"
			// A flow target forces the shared whole-operation JSON fallback.
			want := strings.Replace(source, "      calls: []", "      "+readDefinitions+"\n      \"parameters\": "+readParamsJSON+"\n      calls: ["+readCallJSON+"]", 1)
			want = strings.Replace(want, "calls: []}, {name: other", flowSaved+"}, {name: other", 1)
			if endings != "LF" {
				source = strings.ReplaceAll(source, "\n", "\r\n")
				want = strings.ReplaceAll(want, "\n", "\r\n")
			}
			if endings == "mixed" {
				source = strings.Replace(source, "calls: [] # keep é 🙂\r\n", "calls: [] # keep é 🙂\n", 1)
				want = strings.Replace(want, "      "+readDefinitions+"\r\n      \"parameters\": "+readParamsJSON+"\r\n      calls: ["+readCallJSON+"] # keep é 🙂\r\n", "      "+readDefinitions+"\n      \"parameters\": "+readParamsJSON+"\n      calls: ["+readCallJSON+"] # keep é 🙂\n", 1)
			}
			checkTestRecordings(t, source, want, readRecording("flow", nil), readRecording("block", nil))
		})
	}
	// Without a flow target the readable block form remains preferred.
	checkTestRecordings(t, prefix+block, prefix+strings.Replace(block, "calls: []", blockSaved, 1), readRecording("block", nil))
}

func TestApplyTestRecordingsTaggedCommentFallback(t *testing.T) {
	for _, entry := range []string{
		`!!map {name: read, mode: recorded, !!str calls: !!seq # waiting
        [ # [ ] keep
        ]}`,
		"name: read\n      mode: recorded\n      calls: !<tag:yaml.org,2002:seq> [ ]",
	} {
		source := testsHeader + recordingTestFence("tagged", entry)
		var want string
		if strings.Contains(entry, "!!str calls") {
			want = strings.Replace(source, "!!str calls:", readDefinitions+", \"parameters\": "+readParamsJSON+", !!str calls:", 1)
		} else {
			want = strings.Replace(source, "      calls:", "      "+readDefinitions+"\n      \"parameters\": "+readParamsJSON+"\n      calls:", 1)
		}
		if strings.Contains(entry, "[ # [ ] keep") {
			want = strings.Replace(want, "[ # [ ] keep", "["+readCallJSON+" # [ ] keep", 1)
		} else {
			want = strings.Replace(want, "[ ]", "["+readCallJSON+" ]", 1)
		}
		checkTestRecordings(t, source, want, readRecording("tagged", nil))
	}
}

func TestApplyTestRecordingsNumbersAndDepth(t *testing.T) {
	for _, field := range []string{"arguments", "result"} {
		for _, lexeme := range []string{"1.0", "1e0", "-0.0", "9007199254740992"} {
			t.Run(field+"/"+lexeme, func(t *testing.T) {
				r := readRecording("numbers", nil)
				r.Tool.Parameters = map[string]any{"type": "object"}
				payload := map[string]any{"n": json.Number(lexeme)}
				if field == "arguments" {
					r.Tool.Calls[0].Arguments = payload
				} else {
					r.Tool.Calls[0].Result = payload
				}
				for _, entry := range []string{testPendingTool, "{name: read, mode: recorded, calls: []}"} {
					got := checkTestRecordings(t, testsHeader+recordingTestFence("numbers", entry), "", r)
					if !strings.Contains(string(got), "n: "+lexeme+"\n") && !strings.Contains(string(got), `"n":`+lexeme+"}") {
						t.Fatalf("lost number lexeme %s in %s", lexeme, got)
					}
				}
			})
		}
		for _, depth := range []int{32, 33} {
			r := readRecording("depth", nil)
			r.Tool.Parameters = map[string]any{"type": "object"}
			if field == "arguments" {
				r.Tool.Calls[0].Arguments = payloadAtDepth(depth, true)
			} else {
				r.Tool.Calls[0].Result = payloadAtDepth(depth, true)
			}
			source := testsHeader + recordingTestFence("depth", testPendingTool)
			if depth == 33 {
				refuseTestRecordings(t, source, ErrRecordingInvalid, r)
			} else {
				checkTestRecordings(t, source, "", r)
			}
		}
	}
}

func TestApplyTestRecordingsRefusesTargetsAtomically(t *testing.T) {
	source := testsHeader + recordingTestFence("pending", testPendingTool) + recordingTestFence("live", "{name: read, mode: live}") + recordingTestFence("saved", `{name: read, mode: recorded, description: Read, parameters: {type: object}, calls: [{arguments: {}, result: {n: 1}}]}`) + testFence("plain")
	good := readRecording("pending", nil)
	for _, id := range []string{"missing", "plain", "live", "saved"} {
		t.Run(id, func(t *testing.T) { refuseTestRecordings(t, source, ErrRecordingChanged, good, readRecording(id, nil)) })
	}
	missing := readRecording("pending", nil)
	missing.Tool.Name = "missing"
	refuseTestRecordings(t, source, ErrRecordingChanged, good, missing)
	refuseTestRecordings(t, source, ErrRecordingInvalid, good, good)
	refuseTestRecordings(t, source, ErrRecordingInvalid)
	refuseTestRecordings(t, testsHeader, ErrRecordingChanged, good)
	for _, bad := range []string{
		strings.Replace(source, "schemaVersion: 1", "schemaVersion: 2", 1),
		strings.Replace(source, "mode: recorded", "mode: invalid", 1),
		strings.Replace(source, "calls: []", "calls: null", 1),
		strings.TrimSuffix(source, "```\n"),
		strings.Replace(source, "input: Ada", "input: bad\x00", 1),
		strings.Replace(source, "yaml agent-test", "yml agent-test", 1),
	} {
		refuseTestRecordings(t, bad, ErrRecordingChanged, good)
	}
}

func TestApplyTestRecordingsInvalidPayloads(t *testing.T) {
	source := testsHeader + recordingTestFence("pending", testPendingTool)
	for name, change := range map[string]func(*MockTool){
		"live":               func(m *MockTool) { m.Mode = "live" },
		"no calls":           func(m *MockTool) { m.Calls = nil },
		"multiple calls":     func(m *MockTool) { m.Calls = append(m.Calls, m.Calls[0]) },
		"blank description":  func(m *MockTool) { m.Description = " " },
		"missing schema":     func(m *MockTool) { m.Parameters = nil },
		"invalid schema":     func(m *MockTool) { m.Parameters = map[string]any{"type": "unknown"} },
		"arguments mismatch": func(m *MockTool) { m.Calls[0].Arguments = map[string]any{"query": 42.0} },
		"nil arguments":      func(m *MockTool) { m.Calls[0].Arguments = nil },
		"nil result":         func(m *MockTool) { m.Calls[0].Result = nil },
		"nonfinite":          func(m *MockTool) { m.Calls[0].Result = map[string]any{"n": math.NaN()} },
		"malformed number":   func(m *MockTool) { m.Calls[0].Result = map[string]any{"n": json.Number("01")} },
		"lossy UTF8":         func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "\xff"} },
		"credential value":   func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "ghp_" + strings.Repeat("a", 36)} },
		"credential key":     func(m *MockTool) { m.Calls[0].Result = map[string]any{"ghp_" + strings.Repeat("a", 36): true} },
		"control":            func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "a\x01b"} },
		"separator":          func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "a\u2028b"} },
		"scalar cap":         func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": strings.Repeat("a", MaxInputBytes+1)} },
		"definition cap":     func(m *MockTool) { m.Description = strings.Repeat("a", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			r := readRecording("pending", nil)
			change(&r.Tool)
			refuseTestRecordings(t, source, ErrRecordingInvalid, r)
		})
	}
	for _, field := range []string{"arguments", "result"} {
		r := readRecording("pending", nil)
		r.Tool.Parameters = map[string]any{"type": "object"}
		payload := map[string]any{"n": json.Number("9007199254740993")}
		if field == "arguments" {
			r.Tool.Calls[0].Arguments = payload
		} else {
			r.Tool.Calls[0].Result = payload
		}
		refuseTestRecordings(t, source, ErrRecordingInvalid, r)
	}
}

func TestApplyTestRecordingsCandidateCaps(t *testing.T) {
	source := testsHeader + recordingTestFence("pending", testPendingTool)
	nearLimit := source + strings.Repeat("p", MaxFileBytes-len(source))
	if _, err := ParseTests("suite.md", []byte(nearLimit)); err != nil {
		t.Fatal(err)
	}
	refuseTestRecordings(t, nearLimit, ErrRecordingInvalid, readRecording("pending", nil))
	// Each new recording fits alone, but their aggregate exceeds the suite node cap.
	var all string
	var recordings []Recording
	for i := range 3 {
		id := fmt.Sprintf("case-%d", i)
		all += recordingTestFence(id, testPendingTool)
		rows := make([]any, 11000)
		for j := range rows {
			rows[j] = 0.0
		}
		r := readRecording(id, map[string]any{"rows": rows})
		recordings = append(recordings, r)
	}
	checkTestRecordings(t, testsHeader+recordingTestFence("case-0", testPendingTool), "", recordings[0])
	refuseTestRecordings(t, testsHeader+all, ErrRecordingInvalid, recordings...)
}
