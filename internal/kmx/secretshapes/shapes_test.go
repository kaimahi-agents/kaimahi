package secretshapes

import (
	"os"
	"strings"
	"testing"
)

// Every shape has to be proven twice: that it fires on the thing it names,
// and that it does not fire on something that merely looks like it. A
// regex that quietly stopped matching is the failure this whole list
// exists to prevent, and it has no symptom — every document reads clean.
func TestEveryShapeMatchesItsExampleAndNotItsCounterexample(t *testing.T) {
	for _, s := range All() {
		if !s.Re.MatchString(s.Example) {
			t.Errorf("%s: does not match its own example %q", s.Name, s.Example)
		}
		if s.Counterexample == "" {
			t.Errorf("%s: has no counterexample, so nothing pins how wide it is", s.Name)
			continue
		}
		if s.Re.MatchString(s.Counterexample) {
			t.Errorf("%s: matches its counterexample %q, so it is wider than it says", s.Name, s.Counterexample)
		}
	}
}

// The list is loaded, not written, so "how many shapes are there" is a
// question with a runtime answer — and a list that lost its entries would
// report every document clean. init() refuses an empty file; this refuses
// a list that has quietly shrunk to a handful.
func TestTheListIsNotEmptyAndNamesTheCredentialsThisProjectActuallyHandles(t *testing.T) {
	all := All()
	if len(all) < 10 {
		t.Fatalf("shapes.json declares %d shapes; it named twelve when this was written, and a list "+
			"that shrank is a list that stopped refusing something", len(all))
	}
	// The seams this repository has actually run, named so that deleting
	// one is a test failure rather than a silent narrowing.
	for _, name := range []string{
		"anthropic-api-key", "openai-api-key", "kaimahi-credential",
		"github-token", "github-fine-grained-token",
		"slack-token", "slack-app-token", "private-key",
	} {
		if !has(all, name) {
			t.Errorf("shapes.json no longer declares %q", name)
		}
	}
}

func has(all []Shape, name string) bool {
	for _, s := range all {
		if s.Name == name {
			return true
		}
	}
	return false
}

// shapes.json is read by the tree scan like any other file, so an example
// written out whole would be a credential shape committed to the tree —
// the exact thing being forbidden. The parts form is what keeps that from
// happening, and this is what keeps the parts form.
func TestTheFileItselfCarriesNoCredentialShape(t *testing.T) {
	raw, err := os.ReadFile("shapes.json")
	if err != nil {
		t.Fatal(err)
	}
	if s, at := MatchIndex(string(raw)); s != nil {
		t.Fatalf("shapes.json itself contains something shaped like %s at byte %d — write the example "+
			"as parts so the file carries no whole one", s.What, at)
	}
}

// Match reports the FIRST shape, and a refusal that named the wrong
// credential would send someone to revoke the wrong thing.
func TestMatchNamesTheShapeItFound(t *testing.T) {
	for _, s := range All() {
		got := Match("some prose, then " + s.Example + ", then more prose")
		if got == nil {
			t.Errorf("%s: Match found nothing in a document containing its example", s.Name)
			continue
		}
		// Some examples satisfy more than one shape by construction (an
		// OpenAI project key is also an OpenAI key). What must never
		// happen is finding nothing, or finding a shape whose own regex
		// does not match.
		if !got.Re.MatchString(s.Example) {
			t.Errorf("%s: Match returned %s, which does not match %q", s.Name, got.Name, s.Example)
		}
	}
	if got := Match("nothing here but ordinary prose and a kubectl command"); got != nil {
		t.Errorf("Match found %s in ordinary prose", got.Name)
	}
}

// The regexes are compiled by Go here and by Python in
// scripts/check-secret-shapes.py. Anything that means different things in
// the two engines would make the tree scan and the scaffolder disagree
// about the same document, so the file sticks to the constructs both read
// the same way.
func TestTheRegexesStayPortableBetweenGoAndPython(t *testing.T) {
	raw, err := os.ReadFile("shapes.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, unportable := range []string{"(?<", "(?=", "(?!", "\\p{", "\\A", "\\Z"} {
		if strings.Contains(string(raw), unportable) {
			t.Errorf("shapes.json uses %q, which Go's RE2 and Python's re do not agree on", unportable)
		}
	}
}
