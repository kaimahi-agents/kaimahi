package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func createWorkspace(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	instructions := filepath.Join(dir, "input.md")
	writeFile(t, instructions, "Answer briefly.\n\nExact whitespace.\n")
	opt := workspace.CreateOptions{Name: "greeter", Description: "Public questions", Model: "qwen-3.5-2b", InstructionsFile: instructions}
	if err := workspace.Create(context.Background(), root, opt); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(dir, "cache")
}

func setMemberRuntime(t *testing.T, root, declaration string) {
	t.Helper()
	name := filepath.Join(root, "agents/greeter/member.yaml")
	base := strings.Split(string(readFile(t, name)), "\nruntime:")[0]
	writeFile(t, name, strings.TrimSpace(base)+"\n"+declaration)
}

func extract(t *testing.T, p *workspace.Prepared) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "suite")
	result, err := suiteoras.Pull(context.Background(), p.Path, p.Descriptor.Digest.String(), out)
	if err != nil {
		t.Fatal(err)
	}
	if result.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("pull substituted descriptor")
	}
	return out
}

// rebindEvals edits evals and recomputes public digests so only native checks can refuse.
func rebindEvals(t *testing.T, out string, mutate func(index *spec.EvaluationIndex, firstCase map[string]any)) {
	t.Helper()
	indexName := filepath.Join(out, "evals/greeter/index.json")
	var index spec.EvaluationIndex
	if err := spec.Unmarshal(readFile(t, indexName), &index); err != nil {
		t.Fatal(err)
	}
	caseName := filepath.Join(out, index.Cases[0].Path)
	decoder := json.NewDecoder(bytes.NewReader(readFile(t, caseName)))
	decoder.UseNumber()
	var first map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	mutate(&index, first)
	cb, err := spec.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, caseName, string(cb))
	index.Cases[0].Descriptor.Size = int64(len(cb))
	if index.Cases[0].Descriptor.Digest, err = spec.Digest(cb); err != nil {
		t.Fatal(err)
	}
	ib, err := spec.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, indexName, string(ib))
	rebindAgent(t, out, func(agent *spec.Agent) {
		agent.Evals[0].Descriptor.Size = int64(len(ib))
		if agent.Evals[0].Descriptor.Digest, err = spec.Digest(ib); err != nil {
			t.Fatal(err)
		}
	})
}

// removeEvals leaves a public-valid suite whose member has no evaluations.
func removeEvals(t *testing.T, out string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(out, "evals")); err != nil {
		t.Fatal(err)
	}
	rebindAgent(t, out, func(agent *spec.Agent) { agent.Evals = nil })
}

func rebindAgent(t *testing.T, out string, mutate func(*spec.Agent)) {
	t.Helper()
	agentName := filepath.Join(out, "agents/greeter.json")
	var agent spec.Agent
	if err := spec.Unmarshal(readFile(t, agentName), &agent); err != nil {
		t.Fatal(err)
	}
	mutate(&agent)
	mb, err := spec.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, agentName, string(mb))
	var suite spec.Suite
	if err := spec.Unmarshal(readFile(t, filepath.Join(out, "agentsuite.json")), &suite); err != nil {
		t.Fatal(err)
	}
	suite.Agents[0].Descriptor.Size = int64(len(mb))
	if suite.Agents[0].Descriptor.Digest, err = spec.Digest(mb); err != nil {
		t.Fatal(err)
	}
	sb, err := spec.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(out, "agentsuite.json"), string(sb))
}
func TestCreateOfflineExactInstructionsAndValidCase(t *testing.T) {
	root, _ := createWorkspace(t)
	if got := string(readFile(t, filepath.Join(root, "agents/greeter/instructions.md"))); got != "Answer briefly.\n\nExact whitespace.\n" {
		t.Fatalf("instructions changed: %q", got)
	}
	if _, err := runtime.ParseEvaluationCase(readFile(t, filepath.Join(root, "eval/example.yaml"))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace.yaml", "agents/greeter/member.yaml", "build-profiles/default.json"} {
		readFile(t, filepath.Join(root, name))
	}
}

func TestCreateAuthorsExplicitRuntime(t *testing.T) {
	for _, profile := range []workspace.RuntimeProfile{{}, {ID: "orka"}, {ID: "kagent", Version: "v0.10.3", Engine: "go"}} {
		t.Run(profile.ID, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			input := filepath.Join(t.TempDir(), "instructions.md")
			writeFile(t, input, "Answer briefly.\n")
			if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input, Runtime: profile}); err != nil {
				t.Fatal(err)
			}
			member := string(readFile(t, filepath.Join(root, "agents/greeter/member.yaml")))
			id := profile.ID
			if id == "" {
				id = "orka"
			}
			if !strings.Contains(member, "runtime:\n    id: "+id) {
				t.Fatal("new authoring omitted explicit runtime", member)
			}
		})
	}
}

func TestCreateRefusesInvalidRuntimeBeforeWrites(t *testing.T) {
	for _, profile := range []workspace.RuntimeProfile{{ID: "unknown"}, {ID: "kagent"}, {ID: "orka", Engine: "go"}, {ID: "kagent", Version: "v0.10.2", Engine: "go"}, {ID: "kagent", Version: "v0.10.3", Engine: "python"}} {
		root := filepath.Join(t.TempDir(), "workspace")
		err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: "missing-file", Runtime: profile})
		if err == nil || !strings.Contains(err.Error(), "runtime") {
			t.Fatalf("runtime not validated before input read: %v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid runtime wrote workspace")
		}
	}
}

// Creation must not merge into an old bundle, second member, or overwrite files.
func TestCreateRefusesExistingArtifacts(t *testing.T) {
	for _, collision := range []string{"workspace.yaml", "agent.yaml", "agents/other/member.yaml", "eval/example.yaml", "build-profiles/default.json"} {
		t.Run(collision, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, collision), "do not replace")
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, "Public instructions")
			err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input})
			if err == nil {
				t.Fatal("accepted existing artifact")
			}
			if got := string(readFile(t, filepath.Join(root, collision))); got != "do not replace" {
				t.Fatalf("overwritten: %q", got)
			}
		})
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	credential := "ghp_" + strings.Repeat("x", 36)
	for _, test := range []struct{ name, model, text string }{
		{"", "model", "instructions"}, {"../escape", "model", "instructions"},
		{"with.dots", "model", "instructions"}, {"with_underscore", "model", "instructions"}, {"trailing-", "model", "instructions"},
		{"greeter", "", "instructions"}, {"greeter", " model ", "instructions"},
		{"greeter", "model", "  \n"}, {"greeter", "model", credential},
		{credential, "model", "instructions"},
	} {
		t.Run(strings.ReplaceAll(test.name, credential, "credential-name")+test.model, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, test.text)
			root := filepath.Join(t.TempDir(), "workspace")
			err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: test.name, Model: test.model, InstructionsFile: input})
			if err == nil {
				t.Fatal("accepted invalid input")
			}
			if strings.Contains(err.Error(), credential) {
				t.Fatal("error echoed credential-shaped input")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid input created workspace directory")
			}
		})
	}
}

func TestNativeSourceRefusesTextTheOrkaRendererCannotRepresent(t *testing.T) {
	for _, tc := range []struct{ name, description, instructions, field string }{
		{"NUL instructions", "brief", "Answer briefly.\x00", "instructions"},
		{"CRLF instructions", "brief", "Answer briefly.\r\n", "instructions"},
		{"multiline description", "line one\nline two", "Answer briefly.\n", "description"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, tc.instructions)
			root := filepath.Join(t.TempDir(), "workspace")
			err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Description: tc.description, Model: "test-model", InstructionsFile: input})
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("unrenderable %s should refuse before writing: %v", tc.field, err)
			}
			if _, err := os.Stat(filepath.Join(root, "workspace.yaml")); !os.IsNotExist(err) {
				t.Fatal("invalid native text published source")
			}
		})
	}
	root, cache := createWorkspace(t)
	writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Edited\x00instructions")
	if _, err := workspace.Prepare(context.Background(), root, cache); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("edited unrenderable instructions should refuse preparation: %v", err)
	}
}

func TestCreatePreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git/config"), "git state")
	input := filepath.Join(root, "input.md")
	writeFile(t, input, "Public instructions")
	if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(root, ".git/config"))); got != "git state" {
		t.Fatal("Git state changed")
	}
}
