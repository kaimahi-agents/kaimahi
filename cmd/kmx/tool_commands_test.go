package main

import (
	"bytes"
	"testing"
)

func TestToolCommandHelpAndInvalidScopeAreOffline(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	for _, args := range [][]string{{"tools", "--help"}, {"tools", "suggest", "--help"}, {"tools", "review", "--help"}, {"tools", "approve", "--help"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("%v err=%v loads=%d", args, err, *loads)
		}
	}
	for _, args := range [][]string{{"tools", "suggest", "missing.agent.md", "--repo", "owner/*"}, {"tools", "review", "../../outside"}, {"tools", "review", "p-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--yes"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err == nil || *loads != 0 {
			t.Fatalf("%v err=%v loads=%d", args, err, *loads)
		}
	}
}
