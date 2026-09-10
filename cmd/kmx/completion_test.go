package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCobraCompletionIncludesCompleteCommandTree(t *testing.T) {
	var out, errOut bytes.Buffer
	deps := productionDependencies()
	deps.stdout, deps.stderr = &out, &errOut
	root := newRootCommand(&commandState{deps: deps})
	root.SetArgs([]string{"__complete", "to"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tools", ":4"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("completion output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCompletionScriptsUseCobraProtocol(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps := productionDependencies()
			deps.stdout, deps.stderr = &out, &errOut
			root := newRootCommand(&commandState{deps: deps})
			root.SetArgs([]string{"completion", shell})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "__complete") {
				t.Fatalf("%s script lacks Cobra completion protocol", shell)
			}
		})
	}
}

func TestStaticCompletionIsSortedAndFiltered(t *testing.T) {
	got, directive := staticCompletion([]string{"zeta", "alpha", "beta"})(nil, nil, "b")
	if len(got) != 1 || got[0] != "beta" || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("completion=%v directive=%v", got, directive)
	}
}

func TestContextFlagCompletionAcceptsIncompleteValues(t *testing.T) {
	for _, args := range [][]string{{"__complete", "--context", ""}, {"__complete", "--context=ki"}} {
		var out, errOut bytes.Buffer
		deps := productionDependencies()
		deps.stdout, deps.stderr = &out, &errOut
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(errOut.String(), "needs a context name") {
			t.Fatalf("completion ran operational context validation: %s", errOut.String())
		}
		if !strings.Contains(out.String(), ":4") {
			t.Fatalf("context completion did not disable file completion:\n%s", out.String())
		}
	}
}

func TestAuditCompletionIsPositionAware(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		want      []string
		forbidden []string
	}{
		{[]string{"__complete", "audit", ""}, []string{"tool", "approval", "inbound"}, nil},
		{[]string{"__complete", "audit", "tool", ""}, []string{":4"}, []string{"tool", "approval", "inbound"}},
	} {
		var out, errOut bytes.Buffer
		deps := productionDependencies()
		deps.stdout, deps.stderr = &out, &errOut
		if err := execute(tc.args, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%v completion missing %q:\n%s", tc.args, want, out.String())
			}
		}
		for _, forbidden := range tc.forbidden {
			if strings.Contains(out.String(), forbidden) {
				t.Fatalf("%v completion contains %q:\n%s", tc.args, forbidden, out.String())
			}
		}
	}
}
