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
		{[]string{"--context", "kind-x", "local", "up"}, []string{"local", "up"}, "kind-x"},
		{[]string{"local", "up", "--context", "kind-x"}, []string{"local", "up"}, "kind-x"},
		{[]string{"chat", "hello", "hi", "-context=kind-x"}, []string{"chat", "hello", "hi"}, "kind-x"},
	} {
		kept, context, err := extractContext(tc.argv)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(kept, tc.wantKept) || context != tc.wantCtx {
			t.Fatalf("%v -> %v %q", tc.argv, kept, context)
		}
	}
	for _, argv := range [][]string{{"local", "up", "--context"}, {"local", "up", "--context="}, {"local", "up", "--context", " "}} {
		if _, _, err := extractContext(argv); err == nil {
			t.Errorf("empty context accepted: %v", argv)
		}
	}
}

func TestContextExtractionStopsAtEndOfOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		argv     []string
		wantKept []string
		wantCtx  string
	}{
		{
			name:     "message is not a target selector",
			argv:     []string{"chat", "hello", "--", "--context", "other"},
			wantKept: []string{"chat", "hello", "--", "--context", "other"},
		},
		{
			name:     "leading selector survives message data",
			argv:     []string{"--context", "kind-x", "chat", "hello", "--", "--context", "other"},
			wantKept: []string{"chat", "hello", "--", "--context", "other"},
			wantCtx:  "kind-x",
		},
		{
			name:     "interspersed inline selector survives message data",
			argv:     []string{"chat", "hello", "--context=kind-x", "--", "--context=other"},
			wantKept: []string{"chat", "hello", "--", "--context=other"},
			wantCtx:  "kind-x",
		},
		{
			name:     "legacy selectors respect the delimiter",
			argv:     []string{"chat", "hello", "-context=kind-x", "--", "-context", "other"},
			wantKept: []string{"chat", "hello", "--", "-context", "other"},
			wantCtx:  "kind-x",
		},
		{
			name:     "incomplete selector is literal message data",
			argv:     []string{"chat", "hello", "--", "--context"},
			wantKept: []string{"chat", "hello", "--", "--context"},
		},
		{
			name:     "empty inline selector is literal message data",
			argv:     []string{"chat", "hello", "--", "--context="},
			wantKept: []string{"chat", "hello", "--", "--context="},
		},
		{
			name:     "plain trailing delimiter is retained",
			argv:     []string{"chat", "hello", "--"},
			wantKept: []string{"chat", "hello", "--"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, context, err := extractContext(tc.argv)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(kept, tc.wantKept) || context != tc.wantCtx {
				t.Fatalf("%v -> kept %v, context %q; want kept %v, context %q", tc.argv, kept, context, tc.wantKept, tc.wantCtx)
			}
		})
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
			// Retirement stubs and compatibility routes remain parseable but
			// are not advertised alongside canonical commands.
			if child.Name() != "quickstart-wizard" && child.Name() != "aks" && child.Name() != "bundle" && child.Name() != "lift" && child.Name() != "eval" {
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
	if named == 0 || !strings.Contains(out.String(), "quickstart ") || !strings.Contains(out.String(), "create ") {
		t.Fatalf("usage page lost its native front door (%d commands):\n%s", named, out.String())
	}
}
