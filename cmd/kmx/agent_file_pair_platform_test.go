package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestWindowsPairAdmissionIsFrozenForExecution(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	file, tests := cliPairFiles(t, false)
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.goos = "windows"
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	state := &commandState{deps: deps}
	root := newRootCommand(state)
	cmd, _, err := root.Find([]string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if !windowsCommandAllowed(cmd, []string{file}, state) || state.windowsFilePair == nil {
		t.Fatal("fake/tool-free pair not admitted")
	}
	cmd.SetContext(t.Context())
	admitted := state.windowsFilePair
	if err := runFileCommand(cmd, state, file, fileCommandOptions{Output: "json"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if runner.request.SnapshotRevision != admitted.SnapshotRevision || runner.request.Examples[0].ID != "greeting" {
		t.Fatal("execution did not use admitted pair")
	}
	runner.calls = 0
	out.Reset()
	diag.Reset()
	changed := strings.Replace(cliPairTests, "id: greeting", "id: changed", 1)
	if err := os.WriteFile(tests, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	err = runFileCommand(cmd, state, file, fileCommandOptions{Output: "json"}, "", "")
	if !errors.Is(err, errWindowsOperationUnsupported) || runner.calls != 0 || out.Len() != 0 || diag.Len() != 0 {
		t.Fatalf("edited admitted pair executed: %v %s", err, out.String())
	}
	if state.windowsFilePair != admitted || runner.request.SnapshotRevision != admitted.SnapshotRevision || len(runner.request.Examples) != 1 || runner.request.Examples[0].ID != "greeting" {
		t.Fatal("execution reloaded a different pair after Windows admission")
	}
}
