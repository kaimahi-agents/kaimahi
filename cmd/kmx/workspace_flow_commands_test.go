package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func flowCommandInputs(t *testing.T) []string {
	t.Helper()
	in := t.TempDir()
	root := filepath.Join(t.TempDir(), "team")
	for name, raw := range map[string]string{
		"instructions.md": "Write or review grounded facts.\n",
		"facts.yaml":      "id: facts\ninput: grounded facts\nexpectContains: [facts]\n",
		"members.json":    `{"schemaVersion":"kmx.members/v1alpha1","members":[{"id":"writer","model":"exact-writer","instructions":"instructions.md","cases":["facts.yaml"]},{"id":"reviewer","model":"exact-reviewer","instructions":"instructions.md","cases":["facts.yaml"]}]}`,
		"flow.json":       `{"schemaVersion":"kmx.flow/v1alpha1","id":"release","mode":"pipeline","entry":"writer","edgeRequirements":[],"steps":[{"id":"draft","member":"writer","input":{"source":"run.input"}},{"id":"review","member":"reviewer","input":{"source":"draft.output","context":"run.input"}}],"cases":[{"id":"review","input":"grounded facts","assertions":[{"step":"review","check":{"id":"verdict","type":"contains","value":"PASS"}}]}]}`,
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"package", "create", "team", "--workspace", root, "--members", filepath.Join(in, "members.json"), "--flow", filepath.Join(in, "flow.json")}
}

func TestFlowCreateCommandOffline(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CONTAINER_ENGINE", "not-an-engine")
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	args := flowCommandInputs(t)
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || diagnostics.Len() != 0 || !strings.Contains(out.String(), "offline") || !strings.Contains(out.String(), "not deployed") {
		t.Fatalf("unexpected effects/output: loads%d, %q, %q", *loads, &out, &diagnostics)
	}
	p, err := workspace.PrepareFlow(context.Background(), args[4], filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := workspace.LoadFlowInventory(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Flow.ID != "release" || len(inv.Members) != 2 {
		t.Fatal("CLI did not author full flow")
	}
	for _, operation := range [][]string{{"eval", "--workspace", args[4]}, {"package", "push", "--workspace", args[4], "--unevaluated", "team:v1"}, {"lift", "--workspace", args[4], "--to", "missing"}} {
		out.Reset()
		diagnostics.Reset()
		if err := execute(operation, deps); err == nil {
			t.Fatalf("enabled operation %v", operation)
		}
		if *loads != 0 {
			t.Fatalf("flow operation loaded config: %v", operation)
		}
	}
}

func TestFlowCreateCommandConflictsBeforeWrites(t *testing.T) {
	for _, flags := range [][]string{{"--model=exact"}, {"--instructions=missing"}, {"--description="}, {"--runtime=orka"}, {"--runtime-version=v0.10.3"}, {"--kagent-runtime=go"}, {"--context=unused"}, {"--members="}, {"--flow="}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			args := flowCommandInputs(t)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(append(args, flags...), deps); err == nil {
				t.Fatal("accepted conflicting/empty flag")
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatal("invalid flow mode had operational effects")
			}
			if _, err := os.Stat(args[4]); !os.IsNotExist(err) {
				t.Fatal("invalid flow create wrote root")
			}
		})
	}
}

func TestFlowCreateCommandRequiresBothFilesAndExactWorkspace(t *testing.T) {
	for _, omit := range []string{"--members", "--flow", "--workspace"} {
		t.Run(omit, func(t *testing.T) {
			original := flowCommandInputs(t)
			args := []string{}
			for i := 0; i < len(original); i++ {
				if original[i] == omit {
					i++
					continue
				}
				args = append(args, original[i])
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err == nil {
				t.Fatal("accepted incomplete flow input")
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatal("incomplete create had effects")
			}
			if _, err := os.Stat(original[4]); !os.IsNotExist(err) {
				t.Fatal("incomplete create wrote root")
			}
		})
	}
}
