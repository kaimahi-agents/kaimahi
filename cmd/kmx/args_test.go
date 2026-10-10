package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestContextIsExtractedFromAnywhere(t *testing.T) {
	for _, tc := range []struct {
		argv     []string
		wantKept []string
		wantCtx  string
	}{
		{[]string{"--context", "kind-x", "up"}, []string{"up"}, "kind-x"},
		{[]string{"up", "--context", "kind-x"}, []string{"up"}, "kind-x"},
		{[]string{"agent", "chat", "hello", "hi", "-context=kind-x"}, []string{"agent", "chat", "hello", "hi"}, "kind-x"},
	} {
		kept, context, err := extractContext(tc.argv)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(kept, tc.wantKept) || context != tc.wantCtx {
			t.Fatalf("%v -> %v %q", tc.argv, kept, context)
		}
	}
	for _, argv := range [][]string{{"up", "--context"}, {"up", "--context="}, {"up", "--context", " "}} {
		if _, _, err := extractContext(argv); err == nil {
			t.Errorf("empty context accepted: %v", argv)
		}
	}
}

func TestChatMessageIsJoined(t *testing.T) {
	if got := joinArgs([]string{"what", "pods", "run?"}); got != "what pods run?" {
		t.Fatalf("message=%q", got)
	}
}

// `kmx` with no arguments is how an operator finds out what kmx does, so a
// public command missing from that page is effectively undiscoverable. The
// list is taken from the tree rather than written out, because a written-out
// list cannot notice a command that was hidden by accident.
func TestBareUsageNamesEveryTopLevelCommand(t *testing.T) {
	var out bytes.Buffer
	root := newRootCommand(&commandState{deps: productionDependencies()})
	root.SetOut(&out)
	if err := root.Help(); err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, child := range root.Commands() {
		if child.Hidden {
			// Retired spellings keep useful errors; aks keeps working as a
			// compatibility route. Neither belongs in the public root help.
			if child.Name() != "quickstart-wizard" && child.Name() != "aks" {
				t.Errorf("command %q is unexpectedly hidden", child.Name())
			}
			continue
		}
		// Cobra omits deprecated commands from the available-command list.
		if child.Deprecated != "" {
			continue
		}
		if !strings.Contains(out.String(), child.Name()+" ") {
			t.Errorf("usage page does not name %q:\n%s", child.Name(), out.String())
		}
		named++
	}
	// The behavioral front-door checks pin the retained surface independently;
	// this guard keeps the derived help check from passing on an empty tree.
	if named == 0 || !strings.Contains(out.String(), "quickstart ") || !strings.Contains(out.String(), "agent ") {
		t.Fatalf("usage page lost its native front door (%d commands):\n%s", named, out.String())
	}
}
