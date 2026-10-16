package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/opencontainers/go-digest"
)

// Omitting native semantics/cases or packing unrelated files must break this test.
func TestPrepareExactNativeArtifactAndReuse(t *testing.T) {
	root, cache := createWorkspace(t)
	writeFile(t, filepath.Join(root, ".git/config"), "private git")
	writeFile(t, filepath.Join(root, "receipts/private.json"), "private receipt")
	writeFile(t, filepath.Join(root, "unrelated.key"), "private key")
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "greeter" || !agentsuite.IsValidDigest(p.Fingerprint) {
		t.Fatalf("invalid prepared identity: %+v", p)
	}
	if err := workspace.Verify(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	if _, err := agentsuite.ValidatePath(out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git", "receipts", "unrelated.key", "workspace.yaml", "agent.yaml"} {
		if _, err := os.Lstat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("packed unexpected path %s", name)
		}
	}
	if got := string(readFile(t, filepath.Join(out, "instructions/greeter.md"))); got != "Answer briefly.\n\nExact whitespace.\n" {
		t.Fatalf("instruction bytes changed: %q", got)
	}
	authored, err := runtime.ParseEvaluationCase(readFile(t, filepath.Join(root, "eval/example.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := workspace.DecodePackagedCase(readFile(t, filepath.Join(out, "evals/greeter/cases/example.json")))
	if err != nil || !reflect.DeepEqual(packaged, authored) {
		t.Fatalf("packaged case does not round-trip the authored case: %+v %v", packaged, err)
	}
	var member agentsuite.Agent
	if err := agentsuite.Unmarshal(readFile(t, filepath.Join(out, "agents/greeter.json")), &member); err != nil {
		t.Fatal(err)
	}
	if member.Model.Protocol != "openai-compatible" || member.Model.Model != "qwen-3.5-2b" {
		t.Fatalf("member model changed: %+v", member.Model)
	}
	var index agentsuite.EvaluationIndex
	if err := agentsuite.Unmarshal(readFile(t, filepath.Join(out, "evals/greeter/index.json")), &index); err != nil {
		t.Fatal(err)
	}
	indexBytes := readFile(t, filepath.Join(out, "evals/greeter/index.json"))
	indexDigest, err := agentsuite.Digest(indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	wantIndex := agentsuite.FileRef{Path: "evals/greeter/index.json", Descriptor: agentsuite.Descriptor{MediaType: agentsuite.MediaTypeEvaluationIndex, Digest: indexDigest, Size: int64(len(indexBytes))}}
	if len(member.Evals) != 1 || !reflect.DeepEqual(member.Evals[0], wantIndex) {
		t.Fatalf("member does not bind its evaluation index: %+v", member.Evals)
	}
	caseBytes := readFile(t, filepath.Join(out, "evals/greeter/cases/example.json"))
	wantCase := agentsuite.FileRef{Path: "evals/greeter/cases/example.json", Descriptor: agentsuite.Descriptor{MediaType: agentsuite.MediaTypeEvaluationCase, Digest: digest.FromBytes(caseBytes).String(), Size: int64(len(caseBytes))}}
	if index.Agent != "greeter" || len(index.Cases) != 1 || !reflect.DeepEqual(index.Cases[0], wantCase) || index.Limits != (agentsuite.EvaluationLimits{MaxCases: 128, MaxCaseBytes: 1 << 20}) {
		t.Fatalf("evaluation index mismatch: %+v", index)
	}
	if _, err := os.Lstat(filepath.Join(out, "metadata/kmx/eval")); !os.IsNotExist(err) {
		t.Fatal("cases still packaged in the private metadata location")
	}
	var metadata map[string]any
	if err := json.Unmarshal(readFile(t, filepath.Join(out, "metadata/kmx/native-orka.json")), &metadata); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"schemaVersion": float64(2), "executionMode": "native-orka", "member": "greeter", "rendererVersion": "kmx.native-orka/v2", "requiredImages": []any{}}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("invalid native metadata: %+v", metadata)
	}
	again, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != p.Path || again.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("unchanged source repackaged differently")
	}
	if err := workspace.CheckCurrent(context.Background(), root, p); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(cache, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("nonprivate cache directory %s: %o", name, info.Mode().Perm())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	workspaceFile := filepath.Join(root, "workspace.yaml")
	writeFile(t, workspaceFile, "# Equivalent source spelling\n"+string(readFile(t, workspaceFile)))
	spelled, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil || spelled.Descriptor.Digest != p.Descriptor.Digest || spelled.Fingerprint == p.Fingerprint {
		t.Fatal("source spelling changed S or preserved the source fingerprint", err)
	}
}

func TestAuthoredEditsChangeDescriptorAndPublicationRefusesStale(t *testing.T) {
	for _, edit := range []string{"instructions", "model", "case", "case-name"} {
		t.Run(edit, func(t *testing.T) {
			root, cache := createWorkspace(t)
			before, err := workspace.Prepare(context.Background(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			switch edit {
			case "instructions":
				writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Different public instructions\n")
			case "model":
				name := filepath.Join(root, "agents/greeter/member.yaml")
				writeFile(t, name, strings.ReplaceAll(string(readFile(t, name)), "qwen-3.5-2b", "other-model"))
			case "case":
				writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
			case "case-name":
				if err := os.Rename(filepath.Join(root, "eval/example.yaml"), filepath.Join(root, "eval/renamed.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			if err := workspace.CheckCurrent(context.Background(), root, before); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("stale publication accepted: %v", err)
			}
			after, err := workspace.Prepare(context.Background(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			if before.Fingerprint == after.Fingerprint {
				t.Fatal("edit did not change the source fingerprint")
			}
			sameGraph := bytes.Equal(readFile(t, filepath.Join(extract(t, before), "agentsuite.json")), readFile(t, filepath.Join(extract(t, after), "agentsuite.json")))
			if edit == "case-name" {
				// Packaged cases are named by case ID, not authored file name.
				if before.Descriptor.Digest != after.Descriptor.Digest || !sameGraph {
					t.Fatal("renaming an authored case file changed the packaged suite")
				}
				return
			}
			if before.Descriptor.Digest == after.Descriptor.Digest || sameGraph {
				t.Fatal("edit did not change S and G")
			}
		})
	}
}

func TestPrepareRejectsInvalidRuntimeBeforeCacheWrites(t *testing.T) {
	for _, declaration := range []string{
		"null", "{}", "{id: unknown}", "{id: kagent}", "{id: kagent, version: v0.10.3}",
		"{id: kagent, version: v0.10.2, engine: go}", "{id: kagent, version: v0.10.3, engine: python}",
		"{id: orka, version: ''}", "{id: orka, engine: ''}", "{id: kagent, version: v0.10.3, engine: go, extra: ignored}",
	} {
		t.Run(declaration, func(t *testing.T) {
			root, cache := createWorkspace(t)
			setMemberRuntime(t, root, "runtime: "+declaration+"\n")
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("invalid runtime accepted")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid runtime wrote cache")
			}
		})
	}
}

func TestPrepareRejectsUnsupportedOrUnsafeSources(t *testing.T) {
	for _, test := range []struct{ name, path, replace string }{
		{"workspace unknown", "workspace.yaml", "target: secret-cluster\n"},
		{"workspace duplicate", "workspace.yaml", "name: other\n"},
		{"multi document", "workspace.yaml", "---\nname: other\n"},
		{"tools", "agents/greeter/member.yaml", "tools: []\n"},
		{"delegation", "agents/greeter/member.yaml", "invokes: []\n"},
		{"model credential", "agents/greeter/member.yaml", ""},
		{"escaped credential", "agents/greeter/member.yaml", ""},
		{"unsupported protocol", "agents/greeter/member.yaml", ""},
		{"instruction escape", "agents/greeter/member.yaml", ""},
		{"blank instructions", "agents/greeter/instructions.md", ""},
		{"case unknown", "eval/example.yaml", "unknown: ignored\n"},
		{"case duplicate", "eval/example.yaml", "id: other\n"},
		{"case secret", "eval/example.yaml", ""},
		{"bad profile", "build-profiles/default.json", ""},
		{"null model", "agents/greeter/member.yaml", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, cache := createWorkspace(t)
			name := filepath.Join(root, test.path)
			original := string(readFile(t, name))
			replacement := original + test.replace
			switch test.name {
			case "model credential":
				replacement = strings.ReplaceAll(original, "qwen-3.5-2b", "ghp_"+strings.Repeat("x", 36))
			case "escaped credential":
				replacement = strings.ReplaceAll(original, "Public questions", `"\x67hp_`+strings.Repeat("x", 36)+`"`)
			case "unsupported protocol":
				replacement = strings.ReplaceAll(original, "openai-compatible", "anthropic")
			case "instruction escape":
				replacement = strings.ReplaceAll(original, "instructions.md", "../../../outside.md")
			case "blank instructions":
				replacement = " \n"
			case "case secret":
				replacement = strings.ReplaceAll(original, "What is 2+2?", "ghp_"+strings.Repeat("x", 36))
			case "bad profile":
				replacement = strings.ReplaceAll(original, "2b4f19dae3a777dfc3b76730bda1e82e1f66ab2a2686fa93ca78edbfb4f04ffe", strings.Repeat("0", 64))
			case "null model":
				replacement = "id: greeter\ninstructions: instructions.md\nmodel: null\n"
			}
			writeFile(t, name, replacement)
			if p, err := workspace.Prepare(context.Background(), root, cache); err == nil {
				t.Fatalf("accepted unsafe source: %+v", p)
			}
		})
	}
	for _, extra := range []string{"eval/nested/case.yaml", "eval/notes.txt", "agents/second/member.yaml", "eval/duplicate.yaml"} {
		t.Run(extra, func(t *testing.T) {
			root, cache := createWorkspace(t)
			writeFile(t, filepath.Join(root, extra), string(readFile(t, filepath.Join(root, "eval/example.yaml"))))
			if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
				t.Fatal("accepted unsupported extra member/case")
			}
		})
	}
}

func TestPrepareRefusesSymlinksAndOverlappingCache(t *testing.T) {
	for _, link := range []string{"workspace.yaml", "agents/greeter/instructions.md", "eval/example.yaml", "build-profiles/default.json", "agents/greeter"} {
		t.Run(link, func(t *testing.T) {
			root, cache := createWorkspace(t)
			name := filepath.Join(root, link)
			real := name + ".real"
			if err := os.Rename(name, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, name); err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
				t.Fatal("accepted symlink")
			}
		})
	}
	root, _ := createWorkspace(t)
	for _, cache := range []string{root, filepath.Join(root, "cache"), filepath.Dir(root)} {
		if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
			t.Fatalf("accepted overlapping cache %s", cache)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Prepare(context.Background(), root, filepath.Join(alias, "cache")); err == nil {
		t.Fatal("accepted symlink cache")
	}
}

func TestCacheCorruptionDescriptorMismatchAndCancellationRefuse(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	altered := *p
	altered.Descriptor.Size++
	if err := workspace.Verify(context.Background(), &altered); err == nil {
		t.Fatal("accepted wrong descriptor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.Prepare(ctx, root, filepath.Join(t.TempDir(), "cache")); err != context.Canceled {
		t.Fatalf("canceled preparation: %v", err)
	}
	if _, err := workspace.Prepare(ctx, root, cache); err != context.Canceled {
		t.Fatalf("canceled reuse: %v", err)
	}
	if err := workspace.Verify(ctx, p); err != context.Canceled {
		t.Fatalf("canceled verification: %v", err)
	}
	if loaded, err := workspace.Load(ctx, p); loaded != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("Load lost cancellation identity", err)
	}
	manifest := filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded())
	if err := os.Chmod(manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest, "corruption")
	if err := workspace.Verify(context.Background(), p); err == nil {
		t.Fatal("accepted corrupt graph")
	}
	if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
		t.Fatal("silently repaired corrupt cache")
	}
}

func TestConcurrentPrepareSameCandidate(t *testing.T) {
	root, cache := createWorkspace(t)
	var wg sync.WaitGroup
	results := make(chan *workspace.Prepared, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			p, err := workspace.Prepare(context.Background(), root, cache)
			if err != nil {
				errs <- err
			} else {
				results <- p
			}
		})
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Error(err)
	}
	var want string
	for p := range results {
		if want != "" && want != p.Descriptor.Digest.String() {
			t.Fatal("concurrent candidate changed")
		}
		want = p.Descriptor.Digest.String()
	}
	if want == "" {
		t.Fatal("no successful preparation")
	}
}
