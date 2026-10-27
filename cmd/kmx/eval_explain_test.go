package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func TestSuiteFailureExplainCommand(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "private-state")
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	if err := os.WriteFile(filepath.Join(workspace, "workspace.yaml"), []byte("schemaVersion: 1\nname: fictional\nmember: agents/fictional/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	id := "f-" + strings.Repeat("a", 32)
	path := filepath.Join(home, "eval-failures", fmt.Sprintf("%x", sha256.Sum256([]byte(workspace))))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"schemaVersion": 1, "sequence": 1, "detail": map[string]any{
		"diagnosticID": id, "operationID": "op-" + strings.Repeat("b", 32), "suiteDigest": "sha256:" + strings.Repeat("c", 64), "runtime": "orka", "memberID": "fictional", "caseID": "first", "run": 1, "runs": 1, "minPasses": 1, "verdict": "fail", "complete": true, "attributed": true, "answerStatus": "complete", "answer": "fictional failed answer", "assertions": []any{map[string]any{"id": "expected", "type": "contains", "verdict": "fail", "excerpt": "fictional failed answer", "excerptKind": "leading"}},
	}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "00000000000000000001-"+id+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		verb string
		show bool
	}{{"eval", false}, {"eval", true}, {"package test", false}, {"package test", true}} {
		show := tc.show
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		args := append(strings.Fields(tc.verb), "explain", id, "--workspace", workspace)
		if show {
			args = append(args, "--show-content")
		}
		err := execute(args, deps)
		if err != nil || *loads != 0 || !strings.Contains(out.String(), id) || strings.Contains(out.String(), "fictional failed answer") != show {
			t.Fatalf("offline explain: %v loads=%d output=%s", err, *loads, out.String())
		}
		wantDiagnostics := ""
		if tc.verb == "eval" {
			wantDiagnostics = legacyEvalNotice
		}
		if diagnostics.String() != wantDiagnostics {
			t.Fatalf("%s explain notice: %q", tc.verb, &diagnostics)
		}
	}
	for _, tc := range []struct {
		args   []string
		reason string
	}{
		{[]string{"eval", "explain", "--clear"}, "requires explicit --workspace"},
		{[]string{"eval", "explain", "--workspace", ""}, "--workspace requires a directory"},
		{[]string{"eval", "explain", "--clear", "--show-content", "--workspace", workspace}, "mutually exclusive"},
		{[]string{"eval", "explain", "../../passing", "--clear", "--workspace", workspace}, "invalid evaluation failure ID"},
		{[]string{"--context", "kind-test", "eval", "explain", "--workspace", workspace}, "offline and refuses operational selectors"},
		{[]string{"eval", "explain", "--container-engine", "docker", "--workspace", workspace}, "offline and refuses operational selectors"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(tc.args, deps); err == nil || !strings.Contains(err.Error(), tc.reason) || *loads != 0 {
			t.Fatalf("wrong refusal: %v want=%q loads=%d", err, tc.reason, *loads)
		}
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"eval", "explain", id, "--workspace", workspace, "--clear"}, deps); err != nil || *loads != 0 || !strings.Contains(out.String(), "Cleared 1") {
		t.Fatal("offline clear", err, *loads)
	}
}

func TestSuiteFailureExplainHelpAndCompletion(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "absent")
	t.Setenv("KMX_HOME", home)
	for _, args := range [][]string{{"eval", "explain", "--help"}, {"__complete", "eval", "explain", ""}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatal("help/completion not inert", err, *loads)
		}
		if args[0] == "eval" && !strings.Contains(out.String(), "--clear") {
			t.Fatal("explain help missing clear option")
		}
		if _, err := os.Lstat(home); !os.IsNotExist(err) {
			t.Fatal("help/completion created state", err)
		}
	}
}

func TestFileFailureExplainCommand(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(root, "private-state"))
	file := cliPreviewFile(t)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.fileRunner = &cliPreviewFake{verdict: "fail"}
	err = execute([]string{"test", file, "-o", "json"}, deps)
	var previewErr *app.FilePreviewError
	if !errors.As(err, &previewErr) || previewErr.ExitCode() != 2 || *loads != 0 {
		t.Fatalf("file test = %v loads=%d", err, *loads)
	}
	var result app.FilePreviewResult
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 1 {
		t.Fatalf("retention result = %s", out.String())
	}
	id := result.Retention.Diagnostics[0].ID
	loadsBefore := *loads
	for _, verb := range []string{"test", "package test", "eval"} {
		out.Reset()
		diagnostics.Reset()
		args := append(strings.Fields(verb), "explain", id, "--file", file)
		if err := execute(args, deps); err != nil || *loads != loadsBefore || !strings.Contains(out.String(), id) || !strings.Contains(out.String(), result.File.Revision) || strings.Contains(out.String(), "PRIVATE ANSWER") {
			t.Fatalf("%s file explain = %q, %v loads=%d", verb, out.String(), err, *loads)
		}
		if verb == "eval" && diagnostics.String() != legacyEvalNotice {
			t.Fatalf("legacy notice = %q", diagnostics.String())
		}
	}
	for _, tc := range []struct {
		args   []string
		reason string
	}{
		{[]string{"package", "test", "explain", "--workspace", root, "--file", file}, "mutually exclusive"},
		{[]string{"package", "test", "explain", "--file", ""}, "--file requires"},
		{[]string{"package", "test", "explain", "--clear"}, "requires explicit --workspace DIR or --file FILE"},
	} {
		if err := execute(tc.args, deps); err == nil || !strings.Contains(err.Error(), tc.reason) || *loads != loadsBefore {
			t.Fatalf("file explain refusal = %v want %q", err, tc.reason)
		}
	}
}
