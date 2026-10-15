package workspace_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	godigest "github.com/opencontainers/go-digest"
)

// Public format acceptance alone must not certify native execution semantics.
func TestVerifyRefusesInvalidNativeMetadataAndUnlistedContent(t *testing.T) {
	for _, change := range []string{"null images", "missing images", "nonempty images", "unknown metadata", "duplicate metadata", "renderer", "case runs over limit", "case critical", "case extension assertion", "case expectContains order", "case limits", "case index without evals", "unlisted case", "private file", "secret model", "secret instructions", "model bindings", "kagent both profiles", "kagent missing metadata", "kagent cross runtime", "kagent wrong version", "kagent wrong engine", "kagent missing runtime", "kagent null runtime", "kagent wrong contract", "kagent wrong renderer"} {
		t.Run(change, func(t *testing.T) {
			root, cache := createWorkspace(t)
			metadata := "native-orka.json"
			if strings.HasPrefix(change, "kagent ") {
				setMemberRuntime(t, root, "runtime: {id: kagent, version: v0.10.3, engine: go}\n")
				metadata = "native-kagent.json"
			}
			p, err := workspace.Prepare(context.Background(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			out := extract(t, p)
			name := filepath.Join(out, "metadata/kmx", metadata)
			b := string(readFile(t, name))
			switch change {
			case "null images":
				b = strings.ReplaceAll(b, `"requiredImages":[]`, `"requiredImages":null`)
			case "missing images":
				b = strings.ReplaceAll(b, `,"requiredImages":[]`, "")
			case "nonempty images":
				b = strings.ReplaceAll(b, `"requiredImages":[]`, `"requiredImages":["member-image"]`)
			case "unknown metadata":
				b = strings.TrimSuffix(b, "}") + `,"private-metadata-canary":"private-metadata-content"}`
			case "duplicate metadata":
				b = strings.TrimSuffix(b, "}") + `,"member":"greeter"}`
			case "renderer":
				b = strings.ReplaceAll(b, "kmx.native-orka/v2", "kmx.native-orka/v3")
			case "case runs over limit":
				rebindEvals(t, out, func(_ *spec.EvaluationIndex, c map[string]any) { c["runs"] = 101 })
			case "case critical":
				rebindEvals(t, out, func(_ *spec.EvaluationIndex, c map[string]any) {
					c["assertions"].([]any)[0].(map[string]any)["critical"] = true
				})
			case "case extension assertion":
				rebindEvals(t, out, func(_ *spec.EvaluationIndex, c map[string]any) {
					c["assertions"] = append(c["assertions"].([]any), map[string]any{"id": "tone", "type": "tone-check", "value": "friendly", "critical": false})
				})
			case "case expectContains order":
				rebindEvals(t, out, func(_ *spec.EvaluationIndex, c map[string]any) {
					c["assertions"].([]any)[0].(map[string]any)["id"] = "expectContains.1"
				})
			case "case limits":
				rebindEvals(t, out, func(index *spec.EvaluationIndex, _ map[string]any) { index.Limits.MaxCases = 64 })
			case "case index without evals":
				// A public suite may omit evaluations; native members may not.
				removeEvals(t, out)
			case "kagent both profiles":
				writeFile(t, filepath.Join(out, "metadata/kmx/native-orka.json"), b)
			case "kagent missing metadata":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "kagent cross runtime":
				b = strings.ReplaceAll(b, `"id":"kagent"`, `"id":"orka"`)
			case "kagent wrong version":
				b = strings.ReplaceAll(b, `"version":"v0.10.3"`, `"version":"v0.10.2"`)
			case "kagent wrong engine":
				b = strings.ReplaceAll(b, `"engine":"go"`, `"engine":"python"`)
			case "kagent missing runtime", "kagent null runtime":
				var record map[string]any
				if err := json.Unmarshal([]byte(b), &record); err != nil {
					t.Fatal(err)
				}
				if change == "kagent missing runtime" {
					delete(record, "runtime")
				} else {
					record["runtime"] = nil
				}
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				b = string(raw)
			case "kagent wrong contract":
				b = strings.ReplaceAll(b, "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1", "kmx.unknown/v1")
			case "kagent wrong renderer":
				b = strings.ReplaceAll(b, "kmx.native-kagent/v2", "kmx.native-orka/v2")
			case "unlisted case":
				// The old private case location is not native content.
				writeFile(t, filepath.Join(out, "metadata/kmx/eval/unlisted.yaml"), "id: unlisted\ninput: Public question\nexpectContains: [answer]\n")
			case "private file":
				writeFile(t, filepath.Join(out, "receipts/private.json"), "private execution data")
			case "secret model", "secret instructions", "model bindings":
				// These graph edits must remain valid under the public schema. Recompute
				// its member/instruction graph digests independently of the workspace resolver.
				memberPath := filepath.Join(out, "agents/greeter.json")
				var agent spec.Agent
				if err := spec.Unmarshal(readFile(t, memberPath), &agent); err != nil {
					t.Fatal(err)
				}
				if change == "secret model" {
					agent.Model.Model = "ghp_" + strings.Repeat("x", 36)
				}
				if change == "model bindings" {
					agent.Model.SecretRefs = []string{"provider-secret"}
				}
				if change == "secret instructions" {
					data := "ghp_" + strings.Repeat("x", 36)
					writeFile(t, filepath.Join(out, agent.Instructions.Path), data)
					agent.Instructions.Descriptor.Digest = godigest.FromBytes([]byte(data)).String()
					agent.Instructions.Descriptor.Size = int64(len(data))
				}
				mb, err := spec.Marshal(agent)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, memberPath, string(mb))
				var suite spec.Suite
				if err := spec.Unmarshal(readFile(t, filepath.Join(out, "agentsuite.json")), &suite); err != nil {
					t.Fatal(err)
				}
				suite.Agents[0].Descriptor.Digest, err = spec.Digest(mb)
				if err != nil {
					t.Fatal(err)
				}
				suite.Agents[0].Descriptor.Size = int64(len(mb))
				sb, err := spec.Marshal(suite)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(out, "agentsuite.json"), string(sb))
			}
			if change != "kagent missing metadata" {
				writeFile(t, name, b)
			}
			layout := filepath.Join(t.TempDir(), "layout")
			result, err := suiteoras.Push(context.Background(), out, layout, "modified")
			if err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(layout, func(name string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return os.Chmod(name, 0o700)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			candidate := *p
			candidate.Path, candidate.Descriptor = layout, result.Descriptor
			if _, err := spec.ValidatePath(layout); err != nil {
				t.Fatalf("not a public-valid negative fixture: %v", err)
			}
			if err := workspace.Verify(context.Background(), &candidate); err == nil {
				t.Fatalf("accepted invalid native content %s", change)
			}
			loaded, err := workspace.Load(context.Background(), &candidate)
			if err == nil || loaded != nil {
				t.Fatal("Load returned invalid native content or a partial result")
			}
			if strings.Contains(err.Error(), "private-metadata-canary") || strings.Contains(err.Error(), "private-metadata-content") {
				t.Fatal("Load exposed private metadata in its diagnostic")
			}
		})
	}
}

type observeContext struct {
	context.Context
	observe func() error
}

func (c observeContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	return c.observe()
}

// A stage is not a successful cache entry; source drift must be rechecked after packaging.
func TestPrepareInterruptedStageAndSourceDrift(t *testing.T) {
	for _, test := range []string{"cancel", "source drift"} {
		t.Run(test, func(t *testing.T) {
			root, cache := createWorkspace(t)
			changed := false
			ctx := observeContext{context.Background(), func() error {
				matches, err := filepath.Glob(filepath.Join(cache, ".prepare-*", "layout", "index.json"))
				if err != nil {
					return err
				}
				if len(matches) == 0 {
					return nil
				}
				if test == "cancel" {
					return context.Canceled
				}
				if !changed {
					changed = true
					return os.WriteFile(filepath.Join(root, "agents/greeter/instructions.md"), []byte("Changed while packaging\n"), 0o600)
				}
				return nil
			}}
			_, err := workspace.Prepare(ctx, root, cache)
			if test == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel result: %v", err)
			}
			if test == "source drift" && (err == nil || !strings.Contains(err.Error(), "changed")) {
				t.Fatalf("source drift result: %v", err)
			}
			entries, err := os.ReadDir(cache)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial preparation published or leaked: %v", entries)
			}
		})
	}
}

func TestPrepareRefusesVersionCoercionAndEncodedSourcePaths(t *testing.T) {
	for _, version := range []string{"1.9", "1.0", "true", "\"1\""} {
		t.Run("version "+version, func(t *testing.T) {
			root, cache := createWorkspace(t)
			p := filepath.Join(root, "workspace.yaml")
			writeFile(t, p, strings.Replace(string(readFile(t, p)), "schemaVersion: 1", "schemaVersion: "+version, 1))
			if _, err := workspace.Prepare(context.Background(), root, cache); err == nil || !strings.Contains(err.Error(), "schemaVersion") {
				t.Fatalf("coerced source version should refuse: %v", err)
			}
		})
	}
	root, cache := createWorkspace(t)
	p := filepath.Join(root, "workspace.yaml")
	credential := "ghp_" + strings.Repeat("x", 36)
	encoded := base64.StdEncoding.EncodeToString([]byte(credential))
	writeFile(t, p, strings.Replace(string(readFile(t, p)), "agents/greeter/member.yaml", "!!binary "+encoded, 1))
	_, err := workspace.Prepare(context.Background(), root, cache)
	if err == nil || strings.Contains(err.Error(), credential) || !strings.Contains(err.Error(), "source") {
		t.Fatal("encoded source path must refuse without leaking its decoded value")
	}
}

func TestPrepareRefusesCredentialFilenamesAndNonprivateCacheEntries(t *testing.T) {
	t.Run("credential filename", func(t *testing.T) {
		root, cache := createWorkspace(t)
		writeFile(t, filepath.Join(root, "eval", "ghp_"+strings.Repeat("x", 36)+".yaml"), "id: public\ninput: Question\nexpectContains: [answer]\n")
		if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
			t.Fatal("packaged credential-shaped case filename")
		}
	})
	t.Run("cache entry permissions", func(t *testing.T) {
		root, cache := createWorkspace(t)
		p, err := workspace.Prepare(context.Background(), root, cache)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(p.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
			t.Fatal("reused nonprivate cache entry")
		}
	})
}

func TestPrepareRefusesIncompleteAndReplacedCache(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	// A cache completion record without the layout must refuse, never rebuild.
	if err := os.RemoveAll(p.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
		t.Fatal("accepted incomplete cache")
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("silently rebuilt incomplete cache")
	}
}

func TestSourceBoundsAndMissingCases(t *testing.T) {
	for _, change := range []string{"large instructions", "no cases", "too many cases", "YAML alias", "YAML merge"} {
		t.Run(change, func(t *testing.T) {
			root, cache := createWorkspace(t)
			switch change {
			case "large instructions":
				writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), strings.Repeat("x", (1<<20)+1))
			case "no cases":
				if err := os.Remove(filepath.Join(root, "eval/example.yaml")); err != nil {
					t.Fatal(err)
				}
			case "too many cases":
				for i := 0; i < 128; i++ {
					writeFile(t, filepath.Join(root, "eval", fmt.Sprintf("case-%03d.yaml", i)), fmt.Sprintf("id: c%d\ninput: Question\nexpectContains: [answer]\n", i))
				}
			case "YAML alias":
				writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 1\nname: &name greeter\nmember: *name\nbuildProfile: build-profiles/default.json\n")
			case "YAML merge":
				writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 1\nname: greeter\nmember: agents/greeter/member.yaml\nbuildProfile: build-profiles/default.json\n<<: {name: greeter}\n")
			}
			if _, err := workspace.Prepare(context.Background(), root, cache); err == nil {
				t.Fatalf("accepted unsupported %s", change)
			}
		})
	}
}
