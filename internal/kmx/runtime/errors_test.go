package runtime

import (
	"errors"
	"fmt"
	"testing"
)

func TestUnsupportedVerbErrorMessageNamesRuntimeAndVerb(t *testing.T) {
	cases := []struct {
		runtime ID
		verb    string
		want    string
	}{
		{Kagent, VerbRender, "runtime kagent does not support render"},
		{Orka, VerbEvaluate, "runtime orka does not support evaluate"},
		{KagentV1, VerbDeploy, "runtime kagent-v1 does not support deploy"},
		{KagentV1, VerbList, "runtime kagent-v1 does not support list"},
		{Kagent, VerbShow, "runtime kagent does not support show"},
	}
	for _, tc := range cases {
		err := &UnsupportedVerbError{Runtime: tc.runtime, Verb: tc.verb}
		if got := err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

// UnsupportedVerbError must be a plain typed error a caller can recover with
// errors.As, not a sentinel string a caller has to compare with
// strings.Contains — DESIGN.md §1 calls for "one shared typed runtime error
// naming its ID and verb".
func TestUnsupportedVerbErrorIsTypedAndUnwrappable(t *testing.T) {
	wrapped := fmt.Errorf("create failed: %w", &UnsupportedVerbError{Runtime: Kagent, Verb: VerbRender})

	var target *UnsupportedVerbError
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As could not extract *UnsupportedVerbError from %v", wrapped)
	}
	if target.Runtime != Kagent || target.Verb != VerbRender {
		t.Errorf("extracted error = %+v", target)
	}
}

// DESIGN.md §1: "W94 does not add a second error/registry model" — every
// verb shares the exact same error type, not one type per verb.
func TestUnsupportedVerbErrorIsOneSharedTypeAcrossVerbs(t *testing.T) {
	for _, verb := range []string{VerbRender, VerbDeploy, VerbStatus, VerbEvaluate} {
		var err error = &UnsupportedVerbError{Runtime: Kagent, Verb: verb}
		var target *UnsupportedVerbError
		if !errors.As(err, &target) {
			t.Errorf("verb %q: not an *UnsupportedVerbError", verb)
		}
	}
}
