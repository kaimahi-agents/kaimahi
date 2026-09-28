package runtime

import (
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
