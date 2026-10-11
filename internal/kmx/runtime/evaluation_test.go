package runtime

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestParseEvaluationCaseAcceptsTheDocumentedShape(t *testing.T) {
	c, err := ParseEvaluationCase([]byte("# comment\nid: sign-off\ninput: Say hello.\nexpectContains:\n  - hello\n  - Signed\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "sign-off" || c.Input != "Say hello." || !slices.Equal(c.ExpectContains, []string{"hello", "Signed"}) {
		t.Fatalf("decoded %+v", c)
	}
}

func TestParseEvaluationCaseIsStrict(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"unknown field", "id: a\ninput: x\nexpectContains: [y]\nexpect: [z]\n", "field expect not found"},
		{"misspelled assertion", "id: a\ninput: x\nexpectContain: [y]\n", "not found"},
		{"missing id", "input: x\nexpectContains: [y]\n", "id must be"},
		{"unsafe id", "id: \"a b\"\ninput: x\nexpectContains: [y]\n", "id must be"},
		{"blank input", "id: a\ninput: \"  \"\nexpectContains: [y]\n", "input is required"},
		{"no expectations", "id: a\ninput: x\nexpectContains: []\n", "at least one"},
		{"blank expectation", "id: a\ninput: x\nexpectContains: [\"\"]\n", "nonblank"},
		{"multiline expectation", "id: a\ninput: x\nexpectContains: [\"a\\nb\"]\n", "single-line"},
		{"scalar expectation", "id: a\ninput: x\nexpectContains: y\n", "cannot unmarshal"},
		{"two documents", "id: a\ninput: x\nexpectContains: [y]\n---\nid: b\n", "exactly one YAML document"},
		{"duplicate key", "id: a\nid: b\ninput: x\nexpectContains: [y]\n", "duplicate key"},
		{"alias", "id: &n a\ninput: *n\nexpectContains: [y]\n", "alias"},
		{"not a mapping", "- a\n", "single YAML mapping"},
		{"empty", "", "empty or not valid YAML"},
		{"credential", "id: a\ninput: \"use sk-" + "ant-api03-" + strings.Repeat("A", 90) + "\"\nexpectContains: [y]\n", "shaped like"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvaluationCase([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseEvaluationCaseAcceptsTypedAndMixedAssertions(t *testing.T) {
	for _, doc := range []string{
		"id: a\ninput: public\nassertions:\n- id: no-refusal\n  type: notContains\n  value: refuse\n",
		"id: a\ninput: public\nexpectContains: [hello]\nassertions:\n- id: no-refusal\n  type: notContains\n  value: refuse\n",
		"id: a\ninput: public\nassertions:\n- id: format\n  type: regex\n  pattern: '^hello$'\n- id: called\n  type: toolCalled\n  tool: inventory\n- id: absent\n  type: toolNotCalled\n  tool: delete\n",
	} {
		if _, err := ParseEvaluationCase([]byte(doc)); err != nil {
			t.Fatalf("valid typed case rejected: %v", err)
		}
	}
}

func TestParseEvaluationCaseRejectsInvalidTypedAssertions(t *testing.T) {
	for _, tc := range []struct{ name, assertions, want string }{
		{"duplicate ids", "- {id: same, type: contains, value: a}\n- {id: same, type: notContains, value: b}", "duplicate assertion id"},
		{"reserved id", "- {id: expectContains.0, type: contains, value: a}", "reserved"},
		{"reserved namespace", "- {id: expectContains.custom, type: contains, value: a}", "reserved"},
		{"unsafe id", "- {id: 'bad id', type: contains, value: a}", "assertion id"},
		{"missing id", "- {type: contains, value: a}", "assertion id"},
		{"unknown field", "- {id: a, type: contains, value: a, typo: x}", "field typo not found"},
		{"wrong field", "- {id: a, type: contains, value: a, pattern: x}", "field pattern"},
		{"empty wrong field", "- {id: a, type: contains, value: a, tool: ''}", "field tool"},
		{"null wrong field", "- {id: a, type: toolCalled, tool: inventory, value: null}", "field value"},
		{"missing value", "- {id: a, type: contains}", "value"},
		{"blank value", "- {id: a, type: notContains, value: ' '}", "value"},
		{"control value", "- {id: a, type: contains, value: \"a\\nb\"}", "single-line"},
		{"blank tool", "- {id: a, type: toolCalled, tool: ' '}", "tool"},
		{"control tool", "- {id: a, type: toolNotCalled, tool: \"a\\nb\"}", "single-line"},
		{"blank regex", "- {id: a, type: regex, pattern: ' '}", "pattern"},
		{"malformed regex", "- {id: a, type: regex, pattern: '['}", "RE2"},
		{"non RE2 regex", "- {id: a, type: regex, pattern: '(?=hello)'}", "RE2"},
		{"long regex", "- {id: a, type: regex, pattern: '" + strings.Repeat("a", 1025) + "'}", "1024"},
		{"unicode byte limit", "- {id: a, type: regex, pattern: '" + strings.Repeat("é", 513) + "'}", "1024"},
		{"judge", "- {id: a, type: llmJudge, value: a}", "unsupported assertion type"},
		{"legacy judge", "- {id: a, type: llm_judge, value: a}", "unsupported assertion type"},
		{"snake type", "- {id: a, type: not_contains, value: a}", "unsupported assertion type"},
		{"duplicate key", "- {id: a, type: contains, value: a, value: b}", "duplicate key"},
		{"alias", "- &assert {id: a, type: contains, value: a}\n- *assert", "alias"},
		{"merge", "- {id: a, type: contains, value: a, '<<': {tool: x}}", "merge key"},
		{"not sequence", "  {id: a, type: contains, value: a}", "cannot unmarshal"},
		{"null assertion", "- null", "mapping"},
		{"numeric value", "- {id: a, type: contains, value: 123}", "string"},
		{"null value", "- {id: a, type: contains, value: null}", "string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvaluationCase([]byte("id: a\ninput: public\nassertions:\n" + tc.assertions + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	for _, pattern := range []string{strings.Repeat("a", 1024), strings.Repeat("é", 512)} {
		if _, err := ParseEvaluationCase([]byte("id: a\ninput: public\nassertions:\n- {id: a, type: regex, pattern: '" + pattern + "'}\n")); err != nil {
			t.Fatalf("1024-byte pattern rejected: %v", err)
		}
	}
}

func TestParseEvaluationCaseScansTypedAssertionCredentialsBeforeErrors(t *testing.T) {
	secret := "gh" + "p_" + strings.Repeat("A", 36)
	escaped := "\\x67" + secret[1:]
	for _, assertion := range []string{
		"- {id: a, type: contains, value: '" + secret + "'}",
		"- {id: a, type: regex, pattern: \"" + escaped + "\"}",
		"- {id: a, type: contains, value: ok, \"" + escaped + "\": x}",
	} {
		_, err := ParseEvaluationCase([]byte("id: a\ninput: public\nassertions:\n" + assertion + "\n"))
		if err == nil || !strings.Contains(err.Error(), "shaped like") || strings.Contains(err.Error(), secret) {
			t.Fatalf("credential was not safely rejected: %v", err)
		}
	}
}

func TestEvaluationAssertionCountPreflight(t *testing.T) {
	for _, count := range []int{1000, 1001} {
		for _, kind := range []string{"legacy", "explicit", "mixed"} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				var doc strings.Builder
				doc.WriteString("id: one\ninput: hello\n")
				legacy := 0
				if kind == "legacy" {
					legacy = count
				}
				if kind == "mixed" {
					legacy = count - 1
				}
				if legacy > 0 {
					doc.WriteString("expectContains:\n")
					doc.WriteString(strings.Repeat("- hello\n", legacy))
				}
				var assertions []EvaluationAssertion
				if count > legacy {
					doc.WriteString("assertions:\n")
					for i := 0; i < count-legacy; i++ {
						id := fmt.Sprintf("check-%d", i)
						fmt.Fprintf(&doc, "- {id: %s, type: contains, value: hello}\n", id)
						assertions = append(assertions, EvaluationAssertion{ID: id, Type: "contains", Value: "hello"})
					}
				}
				_, err := ParseEvaluationCase([]byte(doc.String()))
				if count == 1000 && err != nil {
					t.Fatalf("at limit rejected: %v", err)
				}
				if count == 1001 && (err == nil || !strings.Contains(err.Error(), "at most 1000")) {
					t.Fatalf("over limit not refused: %v", err)
				}
				if kind == "explicit" {
					err = ValidateEvaluationAssertions(assertions)
					if count == 1000 && err != nil {
						t.Fatal(err)
					}
					if count == 1001 && (err == nil || !strings.Contains(err.Error(), "at most 1000")) {
						t.Fatalf("standalone over limit not refused: %v", err)
					}
				}
			})
		}
	}
}

func TestEvaluationCasesDigestIdentifiesTheSet(t *testing.T) {
	a := EvaluationCaseFile{Name: "a.yaml", Bytes: []byte("id: a\n")}
	b := EvaluationCaseFile{Name: "b.yaml", Bytes: []byte("id: b\n")}
	base := EvaluationCasesDigest([]EvaluationCaseFile{a, b})
	if EvaluationCasesDigest([]EvaluationCaseFile{b, a}) != base {
		t.Fatal("listing order changed the digest")
	}
	for name, files := range map[string][]EvaluationCaseFile{
		"subset":  {a},
		"renamed": {a, {Name: "c.yaml", Bytes: b.Bytes}},
		"edited":  {a, {Name: "b.yaml", Bytes: []byte("id: b \n")}},
	} {
		if EvaluationCasesDigest(files) == base {
			t.Errorf("%s case set has the same digest", name)
		}
	}
	if len(base) != 64 || base == PortableBundleDigest([]byte("id: a\n")) {
		t.Fatal("digest is not a distinct lowercase SHA-256")
	}
}

func TestMatchExpectationsIsExactAndCaseSensitive(t *testing.T) {
	matched, missing := MatchExpectations("Hello. Signed, Sample", []string{"Hello", "hello", "Signed, Sample"})
	if !slices.Equal(matched, []string{"Hello", "Signed, Sample"}) || !slices.Equal(missing, []string{"hello"}) {
		t.Fatalf("matched %v missing %v", matched, missing)
	}
}
