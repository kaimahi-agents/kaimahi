package runtime

import (
	"errors"
	"fmt"
	"testing"
)

// Adapter identities here are deliberately anonymous: the lifecycle contract
// must read identically for every runtime.
const (
	testRuntime  ID = "example"
	otherRuntime ID = "other"
)

func TestUnsupportedVerbErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		runtime ID
		verb    string
		want    string
	}{
		{testRuntime, VerbRender, "runtime example does not support render"},
		{testRuntime, VerbDeploy, "runtime example does not support deploy"},
		{otherRuntime, VerbStatus, "runtime other does not support status"},
		{otherRuntime, VerbEvaluate, "runtime other does not support evaluate"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			got := (&UnsupportedVerbError{Runtime: tc.runtime, Verb: tc.verb}).Error()
			if got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Callers recover the declined verb with errors.As, never by matching text, so
// the shared error must survive the wrapping every caller adds.
func TestUnsupportedVerbErrorRecoveredThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("deploy agent: %w", fmt.Errorf("adapter: %w", &UnsupportedVerbError{Runtime: testRuntime, Verb: VerbEvaluate}))
	var unsupported *UnsupportedVerbError
	if !errors.As(wrapped, &unsupported) {
		t.Fatalf("errors.As did not recover *UnsupportedVerbError from %v", wrapped)
	}
	if unsupported.Runtime != testRuntime || unsupported.Verb != VerbEvaluate {
		t.Fatalf("recovered %+v, want runtime %q verb %q", unsupported, testRuntime, VerbEvaluate)
	}
}
