package main

import (
	"bytes"
	"strings"
	"testing"
)

// Catch reference modes loading Kubernetes config or treating source selection as consent.
func TestAgentSessionReferenceCLIRefusalsAreOffline(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"eval", "from-sessions"}, "from-sessions"},
		{[]string{"eval", "from-sessions", "source-1", "source-2"}, "required"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Setenv("KAIMAHI_CONFIRM", "")
			var out bytes.Buffer
			deps, loads := testDependencies(&out, &out)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 {
				t.Fatalf("wrong preflight: loads=%d err=%v", *loads, err)
			}
		})
	}
}
func TestAgentSessionReferenceHelpDoesNotLoadConfiguration(t *testing.T) {
	for _, args := range [][]string{{"eval", "from-sessions", "--help"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("help loads configuration: %v", err)
		}
		if !strings.Contains(out.String(), "--sessions") {
			t.Fatal("source/destination missing from help")
		}
	}
}
