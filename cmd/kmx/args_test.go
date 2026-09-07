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
// command missing from that page is a command that effectively does not
// exist. The list is taken from the tree rather than written out, because a
// written-out list cannot notice a command that was hidden by accident.
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
			t.Errorf("command %q is hidden, so the usage page cannot name it", child.Name())
			continue
		}
		if !strings.Contains(out.String(), child.Name()+" ") {
			t.Errorf("usage page does not name %q:\n%s", child.Name(), out.String())
		}
		named++
	}
	// An empty tree would satisfy every check above without naming anything.
	if named < 20 {
		t.Fatalf("usage page named only %d commands; the tree should have far more", named)
	}
}
