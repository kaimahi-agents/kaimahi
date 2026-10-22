package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	suitecore "github.com/agent-primitives/suite/core"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func multiWorkspace(t *testing.T, members, cases int) (string, string) {
	t.Helper()
	root, cache := createWorkspace(t)
	profile := readFile(t, filepath.Join(root, "build-profiles/default.json"))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "build-profiles/default.json"), string(profile))
	paths := make([]string, members)
	for i := range members {
		id := fmt.Sprintf("member-%02d", i)
		paths[i] = "agents/" + id + "/member.yaml"
		writeFile(t, filepath.Join(root, paths[i]), "id: "+id+"\ndescription: Member "+id+"\ninstructions: instructions.md\nmodel:\n  protocol: openai-compatible\n  model: test-model\n")
		writeFile(t, filepath.Join(root, "agents", id, "instructions.md"), "Answer for "+id+".\n")
		for j := range cases {
			writeFile(t, filepath.Join(root, "eval", id, fmt.Sprintf("case-%03d.yaml", j)), fmt.Sprintf("id: case-%03d\ninput: What is 2+2?\nexpectContains: [\"4\"]\n", j))
		}
	}
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 2\nname: team\nbuildProfile: build-profiles/default.json\nmembers: ["+strings.Join(paths, ", ")+"]\n")
	return root, cache
}

func authoredTree(t *testing.T, root string) map[string]workspace.SourceFile {
	t.Helper()
	files := map[string]workspace.SourceFile{}
	if err := filepath.WalkDir(root, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = workspace.SourceFile{Data: readFile(t, name), Mode: info.Mode()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestWorkspaceRefusesNonregularDescriptor(t *testing.T) {
	if root := os.Getenv("KMX_TEST_FIFO_ROOT"); root != "" {
		if err := workspace.RequireSingleMember(root); err == nil {
			t.Fatal("preflight accepted FIFO")
		}
		return
	}
	root := t.TempDir()
	tool, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	if out, err := exec.Command(tool, filepath.Join(root, "workspace.yaml")).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceRefusesNonregularDescriptor$")
	cmd.Env = append(os.Environ(), "KMX_TEST_FIFO_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("descriptor preflight blocked on a FIFO instead of refusing before open")
	}
	if err != nil {
		t.Fatal(err, string(out))
	}
}

func TestMultiSourceRefusesNonDirectoryFIFO(t *testing.T) {
	if root := os.Getenv("KMX_TEST_DIRECTORY_FIFO_ROOT"); root != "" {
		if _, err := workspace.Prepare(t.Context(), root, root+"-cache"); err == nil || !strings.Contains(err.Error(), "directory") {
			t.Fatalf("expected directory refusal, got %v", err)
		}
		return
	}
	tool, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	for _, dir := range []string{"agents", "eval"} {
		t.Run(dir, func(t *testing.T) {
			root, _ := multiWorkspace(t, 2, 1)
			if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(tool, filepath.Join(root, dir)).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMultiSourceRefusesNonDirectoryFIFO$")
			cmd.Env = append(os.Environ(), "KMX_TEST_DIRECTORY_FIFO_ROOT="+root)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("offline prepare blocked on a directory FIFO")
			}
			if err != nil {
				t.Fatal(err, string(out))
			}
			if _, err := os.Stat(root + "-cache"); !os.IsNotExist(err) {
				t.Fatal("non-directory source created cache")
			}
		})
	}
}

func TestSingleSourceDescriptorCompatibility(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	// Source v1 stays unchanged; packaged identity follows current public evals,
	// not the pre-#515 private YAML artifact/fingerprint golden.
	member, err := workspace.Load(t.Context(), p)
	if err != nil || len(member.Cases) != 1 || member.Cases[0].Path != "evals/greeter/cases/example.json" {
		t.Fatal("v1 source did not use current public case packaging", err)
	}
	if string(readFile(t, filepath.Join(root, "workspace.yaml"))) != "schemaVersion: 1\nname: greeter\nmember: agents/greeter/member.yaml\nbuildProfile: build-profiles/default.json\n" {
		t.Fatal("Create descriptor output changed")
	}
}

// Reusing singleton eval paths or retaining singleton roots loses a member here.
func TestMultiPrepareDeterministicScopedGraphAndSources(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 2)
	before := authoredTree(t, root)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Verify(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	var suite suitecore.Suite
	if err := suitecore.Unmarshal(readFile(t, filepath.Join(out, "agentsuite.json")), &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Name != "team" || len(suite.Agents) != 2 || len(suite.Compositions) != 2 || suite.Agents[0].ID != "member-00" || suite.Agents[1].ID != "member-01" {
		t.Fatalf("incomplete or unordered graph: %+v", suite)
	}
	for _, id := range []string{"member-00", "member-01"} {
		var agent suitecore.Agent
		if err := suitecore.Unmarshal(readFile(t, filepath.Join(out, "agents", id+".json")), &agent); err != nil {
			t.Fatal(err)
		}
		indexPath := "evals/" + id + "/index.json"
		if len(agent.Evals) != 1 || agent.Evals[0].Path != indexPath {
			t.Fatalf("member %s did not bind its public evaluation index: %+v", id, agent.Evals)
		}
		var index suitecore.EvaluationIndex
		if err := suitecore.Unmarshal(readFile(t, filepath.Join(out, indexPath)), &index); err != nil {
			t.Fatal(err)
		}
		if index.Agent != id || len(index.Cases) != 2 {
			t.Fatalf("incomplete member index: %+v", index)
		}
		for i, c := range index.Cases {
			if c.Path != fmt.Sprintf("evals/%s/cases/case-%03d.json", id, i) {
				t.Fatal("case IDs were not scoped to their member", c.Path)
			}
			got, err := workspace.DecodePackagedCase(readFile(t, filepath.Join(out, c.Path)))
			want, parseErr := runtime.ParseEvaluationCase(readFile(t, filepath.Join(root, "eval", id, fmt.Sprintf("case-%03d.yaml", i))))
			if err != nil || parseErr != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("scoped case did not round-trip", err, parseErr)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(out, "metadata/kmx/eval")); !os.IsNotExist(err) {
		t.Fatal("private case inventory retained", err)
	}
	readFile(t, filepath.Join(out, "metadata/kmx/native-multi.json"))
	for _, name := range []string{"native-orka.json", "native-kagent.json", "native-agentsessions.json"} {
		if _, err := os.Stat(filepath.Join(out, "metadata/kmx", name)); !os.IsNotExist(err) {
			t.Fatal("singleton metadata retained", name)
		}
	}
	got, err := workspace.SourceFiles(t.Context(), root, p)
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatal("source inventory does not cover all members", err)
	}
	if !reflect.DeepEqual(authoredTree(t, root), before) {
		t.Fatal("prepare rewrote source")
	}
	again, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil || again.Path != p.Path || again.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("prepare was not stable", err)
	}
	marker := filepath.Join(root, "workspace.yaml")
	writeFile(t, marker, strings.ReplaceAll(string(before["workspace.yaml"].Data), "agents/member-00/member.yaml, agents/member-01/member.yaml", "agents/member-01/member.yaml, agents/member-00/member.yaml"))
	reordered, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil || reordered.Descriptor.Digest != p.Descriptor.Digest || reordered.Fingerprint == p.Fingerprint {
		t.Fatal("member declaration order changed artifact or did not bind source", err)
	}
	if err := workspace.CheckCurrent(t.Context(), root, p); err == nil {
		t.Fatal("descriptor drift ignored")
	}
	writeFile(t, filepath.Join(root, "agents/member-01/instructions.md"), "Different instructions.\n")
	if err := workspace.CheckCurrent(t.Context(), root, reordered); err == nil {
		t.Fatal("second member drift ignored")
	}
	if _, err := workspace.SourceFiles(t.Context(), root, reordered); err == nil {
		t.Fatal("stale second member returned")
	}
}

// Both the former 135-file ceiling and member-local case counting break this.
func TestMultiPrepareUpperCaseAndMemberBudgets(t *testing.T) {
	for _, tc := range []struct{ members, cases int }{{2, 64}, {16, 8}} {
		t.Run(fmt.Sprintf("%d-members-128-cases", tc.members), func(t *testing.T) {
			root, cache := multiWorkspace(t, tc.members, tc.cases)
			p, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := workspace.LoadInventory(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, member := range inventory.Members {
				total += len(member.Cases)
			}
			if len(inventory.Members) != tc.members || total != 128 {
				t.Fatal("upper-bound inventory lost members or cases")
			}
			wantFiles := 140
			if tc.members == 16 {
				wantFiles = 196
			}
			if got := len(authoredTree(t, extract(t, p))); got != wantFiles {
				t.Fatalf("upper-bound artifact files = %d, want %d", got, wantFiles)
			}
			if tc.members == 16 {
				entries := packagedTarEntries(t, p)
				t.Logf("16-member upper bound: %d files, %d tar entries, %d entries headroom", wantFiles, entries, 256-entries)
				if entries > 256 {
					t.Fatalf("upper-bound tar exceeds 256 entries: %d", entries)
				}
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
			if err != nil {
				t.Fatalf("source-free upper-bound retrieval refused: %v", err)
			}
			loaded, err := workspace.LoadArtifactInventory(t.Context(), artifact)
			if err != nil || !reflect.DeepEqual(loaded, inventory) {
				t.Fatalf("source-free upper-bound inventory differs: %v", err)
			}
			if _, err := workspace.LoadArtifact(t.Context(), artifact); !errors.Is(err, workspace.ErrMultiMemberUnsupported) {
				t.Fatalf("upper-bound artifact reached execution loader: %v", err)
			}
		})
	}
}

func TestMultiSourceLimitsAndConfinement(t *testing.T) {
	for _, change := range []string{"one-member", "17-members", "129-cases", "numeric-member", "mapping-members", "alias-members", "null-members", "v1-members", "empty-cases", "source-bytes", "artifact-bytes", "instruction-traversal", "member-link", "instruction-link", "eval-dir-link", "case-link"} {
		t.Run(change, func(t *testing.T) {
			members, cases := 2, 1
			if change == "one-member" {
				members = 1
			}
			if change == "17-members" {
				members = 17
			}
			if change == "129-cases" {
				cases = 64
			}
			if strings.HasSuffix(change, "bytes") {
				members = 9
				if change == "artifact-bytes" {
					members = 8
				}
			}
			root, cache := multiWorkspace(t, members, cases)
			marker := filepath.Join(root, "workspace.yaml")
			switch change {
			case "129-cases":
				writeFile(t, filepath.Join(root, "eval/member-01/extra.yaml"), "id: extra\ninput: question\nexpectContains: [answer]\n")
			case "numeric-member", "mapping-members", "alias-members", "null-members":
				line := "members: [123, agents/member-01/member.yaml]\n"
				if change == "mapping-members" {
					line = "members: {one: agents/member-00/member.yaml}\n"
				}
				if change == "alias-members" {
					line = "members: &all [agents/member-00/member.yaml, agents/member-01/member.yaml]\n"
				}
				if change == "null-members" {
					line = "members: null\n"
				}
				writeFile(t, marker, strings.Split(string(readFile(t, marker)), "members:")[0]+line)
			case "v1-members":
				writeFile(t, marker, strings.ReplaceAll(string(readFile(t, marker)), "schemaVersion: 2", "schemaVersion: 1")+"member: agents/member-00/member.yaml\n")
			case "empty-cases":
				if err := os.Remove(filepath.Join(root, "eval/member-01/case-000.yaml")); err != nil {
					t.Fatal(err)
				}
			case "source-bytes", "artifact-bytes":
				for i := range members {
					writeFile(t, filepath.Join(root, fmt.Sprintf("agents/member-%02d/instructions.md", i)), strings.Repeat("x", (1<<20)-1024))
				}
				if change == "artifact-bytes" {
					total := 0
					for _, f := range authoredTree(t, root) {
						total += len(f.Data)
					}
					if total > 8<<20 {
						t.Fatalf("fixture exceeded source ceiling: %d", total)
					}
				}
			case "instruction-traversal":
				p := filepath.Join(root, "agents/member-01/member.yaml")
				writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "instructions.md", "../member-00/instructions.md"))
			case "member-link", "instruction-link", "eval-dir-link", "case-link":
				rel := map[string]string{"member-link": "agents/member-01/member.yaml", "instruction-link": "agents/member-01/instructions.md", "eval-dir-link": "eval/member-01", "case-link": "eval/member-01/case-000.yaml"}[change]
				p := filepath.Join(root, rel)
				real := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(p, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, p); err != nil {
					t.Fatal(err)
				}
			}
			_, err := workspace.Prepare(t.Context(), root, cache)
			if err == nil {
				t.Fatal("unsafe or oversized source accepted")
			}
			if change == "source-bytes" && !strings.Contains(err.Error(), "source exceeds 8 MiB") {
				t.Fatal("wrong source budget refusal", err)
			}
			if change == "artifact-bytes" && !strings.Contains(err.Error(), "native artifact exceeds") {
				t.Fatal("wrong artifact budget refusal", err)
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid source wrote cache")
			}
		})
	}
}

func TestMultiClosedArtifactRegeneration(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 2)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"private-case-extra", "index-limits", "agent-evals-missing", "graph-member-omitted", "graph-extra-file", "singleton-metadata", "metadata-v1", "metadata-field", "metadata-member-omitted", "metadata-order", "metadata-profile"} {
		t.Run(change, func(t *testing.T) {
			out := extract(t, p)
			metadataPath := filepath.Join(out, "metadata/kmx/native-multi.json")
			var rec map[string]any
			if err := json.Unmarshal(readFile(t, metadataPath), &rec); err != nil {
				t.Fatal(err)
			}
			members := rec["members"].([]any)
			second := members[1].(map[string]any)
			want := "packaged native suite verification failed"
			switch change {
			case "private-case-extra":
				writeFile(t, filepath.Join(out, "metadata/kmx/eval/member-01/extra.yaml"), "id: extra\ninput: question\nexpectContains: [answer]\n")
			case "index-limits":
				rebindMemberEvals(t, out, "member-01", func(index *suitecore.EvaluationIndex, _ map[string]any) {
					index.Limits.MaxCases = 127
				})
			case "agent-evals-missing":
				if err := os.RemoveAll(filepath.Join(out, "evals/member-01")); err != nil {
					t.Fatal(err)
				}
				rebindMemberAgent(t, out, "member-01", func(agent *suitecore.Agent) { agent.Evals = nil })
				want += ": native member requires exactly one evaluation index"
			case "graph-member-omitted":
				var suite suitecore.Suite
				path := filepath.Join(out, "agentsuite.json")
				if err := suitecore.Unmarshal(readFile(t, path), &suite); err != nil {
					t.Fatal(err)
				}
				suite.Agents = suite.Agents[:1]
				suite.Compositions = suite.Compositions[:1]
				b, err := suitecore.Marshal(suite)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, string(b))
				// Keep the public graph valid while retaining complete private metadata.
				if err := os.RemoveAll(filepath.Join(out, "evals/member-01")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(out, "agents/member-01.json")); err != nil {
					t.Fatal(err)
				}
			case "graph-extra-file":
				writeFile(t, filepath.Join(out, "instructions/extra.md"), "unlisted instructions\n")
			case "singleton-metadata":
				b, err := json.Marshal(second)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(out, "metadata/kmx/native-orka.json"), string(b))
			case "metadata-v1":
				rec["schemaVersion"], rec["rendererVersion"] = 1, "kmx.native-multi/v1"
			case "metadata-field":
				rec["private-metadata-canary"] = "private-content"
			case "metadata-member-omitted":
				rec["members"] = members[:1]
			case "metadata-order":
				members[0], members[1] = members[1], members[0]
			case "metadata-profile":
				second["rendererVersion"] = "unsupported-renderer"
			}
			b, err := suitecore.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, metadataPath, string(b))
			assertMultiInventoryRefused(t, p, out, want)
		})
	}
	manifest := filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded())
	if err := os.Chmod(manifest, 0600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest, "corrupt")
	if _, err := workspace.LoadInventory(t.Context(), p); err == nil {
		t.Fatal("tampered prepared cache loaded")
	}
	if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
		t.Fatal("tampered prepared cache silently repaired")
	}
}

func TestMultiInventoryAndSingleOnlyLoaders(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 1)
	before := authoredTree(t, root)
	if err := workspace.RequireSingleMember(root); !errors.Is(err, workspace.ErrMultiMemberUnsupported) || err.Error() != "multi-member workspaces support offline prepare/load only" {
		t.Fatal("preflight did not return safe typed refusal", err)
	}
	if !reflect.DeepEqual(authoredTree(t, root), before) {
		t.Fatal("preflight mutated source")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("preflight created cache")
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := workspace.LoadInventory(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Name != "team" || inventory.Digest != p.Descriptor.Digest.String() || !suitecore.IsValidDigest(inventory.GraphDigest) || len(inventory.Members) != 2 {
		t.Fatalf("invalid inventory: %+v", inventory)
	}
	for i, m := range inventory.Members {
		id := fmt.Sprintf("member-%02d", i)
		if m.MemberID != id || m.Digest != inventory.Digest || m.GraphDigest != inventory.GraphDigest || m.Name != "team" || m.Model != "test-model" || m.Runtime.ID != "orka" || string(m.Instructions) != "Answer for "+id+".\n" || len(m.Cases) != 1 || m.Cases[0].Path != "evals/"+id+"/cases/case-000.json" {
			t.Fatalf("invalid member snapshot: %+v", m)
		}
	}
	if got, err := workspace.Load(t.Context(), p); got != nil || !errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatal("single Load selected first member", err)
	}
	a, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := workspace.LoadArtifact(t.Context(), a); got != nil || !errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatal("single artifact Load selected first member", err)
	}
	artifactInventory, err := workspace.LoadArtifactInventory(t.Context(), a)
	if err != nil || !reflect.DeepEqual(artifactInventory, inventory) {
		t.Fatal("artifact inventory changed snapshot", err)
	}
	inventory.Members[0].Instructions[0] = 'X'
	inventory.Members[1].Cases[0].Bytes[0] = 'X'
	fresh, err := workspace.LoadInventory(t.Context(), p)
	if err != nil || !reflect.DeepEqual(fresh, artifactInventory) {
		t.Fatal("caller mutated retained inventory", err)
	}
	artifactInventory.Members[0].Instructions[0] = 'X'
	artifactInventory.Members[1].Cases[0].Bytes[0] = 'X'
	freshArtifact, err := workspace.LoadArtifactInventory(t.Context(), a)
	if err != nil || !reflect.DeepEqual(freshArtifact, fresh) {
		t.Fatal("caller mutated retained artifact", err)
	}
	// The preflight deliberately does not validate member files or their existence.
	if err := os.RemoveAll(filepath.Join(root, "agents")); err != nil {
		t.Fatal(err)
	}
	if err := workspace.RequireSingleMember(root); !errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatal("preflight read members", err)
	}
}

func TestMultiInventoryPreservesMemberRuntimeAndCancellation(t *testing.T) {
	root, cache := multiWorkspace(t, 3, 1)
	for i, runtime := range []string{"{id: orka}", "{id: kagent, version: v0.10.3, engine: go}", "{id: agentsessions, version: " + sessionsVersion + ", engine: chat}"} {
		p := filepath.Join(root, fmt.Sprintf("agents/member-%02d/member.yaml", i))
		writeFile(t, p, string(readFile(t, p))+"runtime: "+runtime+"\n")
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := workspace.LoadInventory(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []workspace.RuntimeProfile{{ID: "orka"}, {ID: "kagent", Version: "v0.10.3", Engine: "go"}, {ID: "agentsessions", Version: sessionsVersion, Engine: "chat"}} {
		if inventory.Members[i].Runtime != want {
			t.Fatal("member runtime was substituted", inventory.Members[i])
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := workspace.LoadInventory(ctx, p); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("inventory lost cancellation", err)
	}
	if got, err := workspace.LoadArtifactInventory(ctx, nil); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("artifact inventory lost cancellation", err)
	}
	if got, err := workspace.LoadInventory(t.Context(), nil); got != nil || err == nil {
		t.Fatal("inventory accepted nil prepared snapshot")
	}
	if got, err := workspace.LoadArtifactInventory(t.Context(), nil); got != nil || err == nil {
		t.Fatal("inventory accepted nil artifact")
	}
}

func TestSingleInventoryCompatibility(t *testing.T) {
	root, cache := createWorkspace(t)
	if err := workspace.RequireSingleMember(root); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	single, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := workspace.LoadInventory(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Name != single.Name || inventory.Digest != single.Digest || inventory.GraphDigest != single.GraphDigest || len(inventory.Members) != 1 || !reflect.DeepEqual(inventory.Members[0], *single) {
		t.Fatal("single inventory changed legacy load")
	}
}

func TestMultiSourceRejectsInvalidFormsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"mixed", func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), string(readFile(t, filepath.Join(r, "workspace.yaml")))+"member: agents/member-00/member.yaml\n")
		}},
		{"mixed-empty", func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), string(readFile(t, filepath.Join(r, "workspace.yaml")))+"member: ''\n")
		}},
		{"duplicate", func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "agents/member-01/member.yaml", "agents/member-00/member.yaml"))
		}},
		{"unresolved", func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "member-01", "missing"))
		}},
		{"traversal", func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "agents/member-01/member.yaml", "agents/../member.yaml"))
		}},
		{"wrong-id", func(t *testing.T, r string) {
			p := filepath.Join(r, "agents/member-01/member.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "id: member-01", "id: other"))
		}},
		{"extra-agent", func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "agents/extra/member.yaml"), "id: extra\n")
		}},
		{"extra-eval", func(t *testing.T, r string) { writeFile(t, filepath.Join(r, "eval/extra/case.yaml"), "id: extra\n") }},
		{"missing-eval", func(t *testing.T, r string) {
			if err := os.RemoveAll(filepath.Join(r, "eval/member-01")); err != nil {
				t.Fatal(err)
			}
		}},
		{"flat-eval", func(t *testing.T, r string) { writeFile(t, filepath.Join(r, "eval/case.yaml"), "id: extra\n") }},
		{"duplicate-case", func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "eval/member-01/duplicate.yaml"), string(readFile(t, filepath.Join(r, "eval/member-01/case-000.yaml"))))
		}},
		{"tools", func(t *testing.T, r string) {
			p := filepath.Join(r, "agents/member-01/member.yaml")
			writeFile(t, p, string(readFile(t, p))+"tools: []\n")
		}},
		{"invokes", func(t *testing.T, r string) {
			p := filepath.Join(r, "agents/member-01/member.yaml")
			writeFile(t, p, string(readFile(t, p))+"invokes: []\n")
		}},
		{"unsupported-root", func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, string(readFile(t, p))+"bindings: []\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, cache := multiWorkspace(t, 2, 1)
			tc.mutate(t, root)
			before := authoredTree(t, root)
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("invalid multi source accepted")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid source wrote cache")
			}
			if !reflect.DeepEqual(authoredTree(t, root), before) {
				t.Fatal("failed prepare rewrote source")
			}
		})
	}
}
