package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// These declarations test offline consistency only, not runtime qualification.
func profiledWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root, cache := createWorkspace(t)
	setMemberRuntime(t, root, "")
	var bp agentsuite.BuildProfile
	if err := agentsuite.Unmarshal(readFile(t, filepath.Join(root, "build-profiles/default.json")), &bp); err != nil {
		t.Fatal(err)
	}
	rows := ""
	for _, id := range []string{"orka", "sessions"} {
		p := agentsuite.RuntimeProfile{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.MediaTypeRuntimeProfile, ID: id, Runtime: agentsuite.EvidenceImplementation{ID: id, Version: "test-only", Digest: bp.RuntimeBase[0].Image.Digest}, Harness: agentsuite.EvidenceImplementation{ID: "test-harness", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, Renderer: agentsuite.EvidenceImplementation{ID: "test-renderer", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, ConfigurationABI: "test-only", ImageRequired: true}
		raw, err := agentsuite.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "runtime-profiles/"+id+".json"), string(raw))
		rows += fmt.Sprintf("  - runtimeProfile: runtime-profiles/%s.json\n    buildProfile: build-profiles/default.json\n    platform: linux/amd64\n", id)
	}
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\nprofiles:\n"+rows)
	return root, cache
}

// Captured from kmx 29d59f7 with suite v0.3.1, before profile support.
func TestLegacyNativePackageDigest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, instructions, "Answer questions concisely and accurately.\n")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{
		Name: "legacy-agent", Model: "fixed-model", InstructionsFile: instructions,
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:aef6abd3f8020f6eee89c6cdb702e11a6c0b8602ab4a4f1cbab5ca6f36879049"
	if got := prepared.Descriptor.Digest.String(); got != want {
		t.Fatalf("legacy package digest changed: got %s, want %s", got, want)
	}
}

func TestProfiledSelectedLoadPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workspace.LoadSelected(ctx, nil, workspace.Selection{RuntimeProfile: "orka"}); !errors.Is(err, context.Canceled) {
		t.Fatal("selected load hid cancellation", err)
	}
}

func TestProfiledPrepareSelectedExactS(t *testing.T) {
	root, cache := profiledWorkspace(t)
	member := filepath.Join(root, "agents/greeter/member.yaml")
	writeFile(t, member, string(readFile(t, member))+"policy: policy.json\n")
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), networkPolicy(""))
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	again, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("nondeterministic preparation", err)
	}
	a, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "orka"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "sessions", Platform: "linux/amd64", Member: "greeter"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest || a.Digest != p.Descriptor.Digest.String() || a.MemberDigest != b.MemberDigest || !reflect.DeepEqual(a.Cases, b.Cases) || a.InstructionsDigest != b.InstructionsDigest || a.CompositionDigest == b.CompositionDigest || a.ProfileDigest == b.ProfileDigest || a.Profile.ID != "orka" || b.Profile.ID != "sessions" || a.Platform.String() != "linux/amd64" {
		t.Fatal("selection changed S or shared content")
	}
	if a.Policy == nil || b.Policy == nil || !reflect.DeepEqual(a.Policy, b.Policy) {
		t.Fatal("profile selection changed shared policy")
	}
	if a.Runtime != (workspace.RuntimeProfile{}) || a.RendererVersion != "" || a.ExecutionProfile != "" {
		t.Fatal("public declaration manufactured legacy execution facts")
	}
	if err := a.RequireUnprofiled(); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal(err)
	}
	own, err := a.ForCompositionDigest(t.Context(), b.CompositionDigest)
	if err != nil || own.Profile.ID != "sessions" {
		t.Fatal("same-S composition resolution", err)
	}
	a.Instructions[0] = 'x'
	a.Cases[0].Bytes[0] = 'x'
	a.Profile = nil
	if err := a.RequireUnprofiled(); !errors.Is(err, workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("clearing public declaration bypassed private profile marker", err)
	}
	fresh, err := a.ForCompositionDigest(t.Context(), b.CompositionDigest)
	if err != nil || fresh.Profile.ID != "sessions" || !bytes.Equal(fresh.Instructions, b.Instructions) || !reflect.DeepEqual(fresh.Cases, b.Cases) {
		t.Fatal("mutable selected alias", err)
	}
	if _, err := a.ForCompositionDigest(t.Context(), "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("unknown composition accepted")
	}
	for _, load := range []func() error{
		func() error { _, e := workspace.Load(t.Context(), p); return e },
		func() error { _, e := workspace.LoadInventory(t.Context(), p); return e },
	} {
		if err := load(); !errors.Is(err, workspace.ErrProfileSelectionRequired) {
			t.Fatal("unselected accepted", err)
		}
	}
	out := extract(t, p)
	if _, err := os.Stat(filepath.Join(out, "metadata/kmx/native-orka.json")); !os.IsNotExist(err) {
		t.Fatal("singleton runtime metadata")
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	round, err := workspace.LoadArtifactSelected(t.Context(), artifact, workspace.Selection{RuntimeProfile: "sessions"})
	if err != nil || round.Digest != b.Digest || round.CompositionDigest != b.CompositionDigest {
		t.Fatal("artifact roundtrip", err)
	}
	if _, err := workspace.LoadArtifact(t.Context(), artifact); !errors.Is(err, workspace.ErrProfileSelectionRequired) {
		t.Fatal(err)
	}
	if _, err := workspace.LoadArtifactInventory(t.Context(), artifact); !errors.Is(err, workspace.ErrProfileSelectionRequired) {
		t.Fatal(err)
	}
	copy, err := suiteoras.PushLayout(t.Context(), p.Path, filepath.Join(t.TempDir(), "copy"), "copy", p.Descriptor)
	if err != nil || copy.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("transfer changed S", err)
	}
	for _, sel := range []workspace.Selection{{}, {RuntimeProfile: "missing"}, {RuntimeProfile: "orka", Member: "wrong"}, {RuntimeProfile: "orka", Platform: "linux/arm64"}} {
		if _, err := workspace.LoadSelected(t.Context(), p, sel); err == nil {
			t.Fatalf("accepted selector %+v", sel)
		}
	}
}

func TestProfiledSourceMutationInvalidates(t *testing.T) {
	for _, name := range []string{"workspace.yaml", "runtime-profiles/orka.json", "build-profiles/default.json", "agents/greeter/member.yaml", "agents/greeter/instructions.md", "eval/example.yaml", "agents/greeter/policy.json"} {
		t.Run(name, func(t *testing.T) {
			root, cache := profiledWorkspace(t)
			member := filepath.Join(root, "agents/greeter/member.yaml")
			writeFile(t, member, string(readFile(t, member))+"policy: policy.json\n")
			writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), networkPolicy(""))
			p, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			files, err := workspace.SourceFiles(t.Context(), root, p)
			if err != nil || len(files) != 8 {
				t.Fatalf("source closure %d: %v", len(files), err)
			}
			if _, ok := files[name]; !ok {
				t.Fatal("authored input absent", name)
			}
			writeFile(t, filepath.Join(root, name), string(readFile(t, filepath.Join(root, name)))+"\n")
			if err := workspace.CheckCurrent(t.Context(), root, p); err == nil {
				t.Fatal("mutation reused source")
			}
			next, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil || next.Fingerprint == p.Fingerprint || next.Path == p.Path {
				t.Fatal("mutation reused cache", err)
			}
		})
	}
}

func TestProfiledSingleProfileStillRequiresSelection(t *testing.T) {
	root, cache := profiledWorkspace(t)
	pth := filepath.Join(root, "workspace.yaml")
	writeFile(t, pth, strings.Split(string(readFile(t, pth)), "  - runtimeProfile: runtime-profiles/sessions.json")[0])
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, load := range []func() error{
		func() error { _, e := workspace.Load(t.Context(), p); return e }, func() error { _, e := workspace.LoadInventory(t.Context(), p); return e },
		func() error { _, e := workspace.LoadArtifact(t.Context(), artifact); return e }, func() error { _, e := workspace.LoadArtifactInventory(t.Context(), artifact); return e },
	} {
		if err := load(); !errors.Is(err, workspace.ErrProfileSelectionRequired) {
			t.Fatal("single profile implicitly selected", err)
		}
	}
}

func TestProfiledPlatformAmbiguity(t *testing.T) {
	root, cache := profiledWorkspace(t)
	file := filepath.Join(root, "build-profiles/default.json")
	var bp agentsuite.BuildProfile
	if err := agentsuite.Unmarshal(readFile(t, file), &bp); err != nil {
		t.Fatal(err)
	}
	base, harness := bp.RuntimeBase[0], bp.Harness[0]
	base.Platform.Architecture = "arm64"
	harness.Platform = base.Platform
	bp.RuntimeBase = append(bp.RuntimeBase, base)
	bp.Harness = append(bp.Harness, harness)
	b, err := agentsuite.Marshal(bp)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, string(b))
	file = filepath.Join(root, "workspace.yaml")
	writeFile(t, file, string(readFile(t, file))+"  - runtimeProfile: runtime-profiles/orka.json\n    buildProfile: build-profiles/default.json\n    platform: linux/arm64\n")
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "orka"}); err == nil || !strings.Contains(err.Error(), "--platform") {
		t.Fatal("ambiguous platform selected", err)
	}
	arm, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "orka", Platform: "linux/arm64"})
	if err != nil || arm.Platform.String() != "linux/arm64" {
		t.Fatal(err)
	}
	if _, err := workspace.LoadSelected(t.Context(), p, workspace.Selection{RuntimeProfile: "sessions"}); err != nil {
		t.Fatal("single candidate not inferred", err)
	}
}

func TestProfiledRuntimeIDCannotEscapePreparationStage(t *testing.T) {
	root, cache := profiledWorkspace(t)
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(cache, "sentinel.json")
	writeFile(t, sentinel, "unchanged\n")
	file := filepath.Join(root, "runtime-profiles/orka.json")
	var rp agentsuite.RuntimeProfile
	if err := agentsuite.Unmarshal(readFile(t, file), &rp); err != nil {
		t.Fatal(err)
	}
	rp.ID = "../../../sentinel"
	raw, err := agentsuite.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, string(raw))
	if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
		t.Error("traversing runtime profile ID accepted")
	}
	if got := string(readFile(t, sentinel)); got != "unchanged\n" {
		t.Fatalf("Prepare overwrote cache sentinel: %q", got)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel.json" {
		t.Fatal("invalid profile mutated cache", err)
	}
}

func TestProfiledEscapedCredentialsRefusedBeforeCache(t *testing.T) {
	// Generated fake shape only; never put a complete credential shape in source.
	canary := "gh" + "p_" + strings.Repeat("privacycanary", 3)
	for _, name := range []string{"build ID", "build image reference", "runtime configuration ABI", "runtime ID"} {
		t.Run(name, func(t *testing.T) {
			root, cache := profiledWorkspace(t)
			file := filepath.Join(root, "build-profiles/default.json")
			var raw []byte
			var err error
			want := "invalid build profile JSON"
			if strings.HasPrefix(name, "runtime") {
				file = filepath.Join(root, "runtime-profiles/orka.json")
				var rp agentsuite.RuntimeProfile
				if err := agentsuite.Unmarshal(readFile(t, file), &rp); err != nil {
					t.Fatal(err)
				}
				if name == "runtime ID" {
					rp.ID = canary
				} else {
					rp.ConfigurationABI = canary
				}
				raw, err = agentsuite.Marshal(rp)
				want = "invalid runtime profile JSON"
			} else {
				var bp agentsuite.BuildProfile
				if err := agentsuite.Unmarshal(readFile(t, file), &bp); err != nil {
					t.Fatal(err)
				}
				if name == "build ID" {
					bp.ID = canary
					// Public validation previously echoed the decoded ID on this error.
					bp.Harness = []agentsuite.PlatformImage{}
				} else {
					bp.RuntimeBase[0].ImageRef = canary
				}
				raw, err = agentsuite.Marshal(bp)
			}
			if err != nil {
				t.Fatal(err)
			}
			escaped := strings.ReplaceAll(string(raw), canary, `\u0067`+canary[1:])
			writeFile(t, file, escaped)
			_, err = workspace.Prepare(t.Context(), root, cache)
			if err == nil {
				t.Error("credential-shaped decoded profile accepted")
			} else {
				if strings.Contains(err.Error(), canary) {
					t.Error("preparation error disclosed decoded credential canary")
				}
				if err.Error() != want {
					t.Error("preparation did not return a generic profile refusal")
				}
			}
			if _, err := os.Lstat(cache); !os.IsNotExist(err) {
				t.Error("credential-shaped profile reached cache creation")
			}
		})
	}
}

func TestProfiledValidEscapesPreserveRawSourceFingerprint(t *testing.T) {
	for _, name := range []string{"runtime-profiles/orka.json", "build-profiles/default.json"} {
		t.Run(name, func(t *testing.T) {
			root, cache := profiledWorkspace(t)
			before, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, name)
			original := string(readFile(t, file))
			idStart := strings.Index(original, `"id":"`) + len(`"id":"`)
			raw := original[:idStart] + fmt.Sprintf(`\u%04x`, original[idStart]) + original[idStart+1:]
			writeFile(t, file, raw)
			after, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			if after.Fingerprint == before.Fingerprint || after.Path == before.Path {
				t.Error("raw escape spelling did not change source identity")
			}
			if after.Descriptor.Digest != before.Descriptor.Digest {
				t.Error("equivalent decoded profile changed packaged content")
			}
			files, err := workspace.SourceFiles(t.Context(), root, after)
			if err != nil || string(files[name].Data) != raw || string(readFile(t, file)) != raw {
				t.Error("preparation did not preserve raw source spelling", err)
			}
		})
	}
}

func TestProfiledAggregateSourceLimit(t *testing.T) {
	root, cache := profiledWorkspace(t)
	raw := string(readFile(t, filepath.Join(root, "runtime-profiles/orka.json"))) + strings.Repeat(" ", 950000)
	rows := ""
	for i := range 9 {
		file := fmt.Sprintf("runtime-profiles/p%d.json", i)
		writeFile(t, filepath.Join(root, file), raw)
		rows += "  - runtimeProfile: " + file + "\n    buildProfile: build-profiles/default.json\n    platform: linux/amd64\n"
	}
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\nprofiles:\n"+rows)
	if _, err := workspace.Prepare(t.Context(), root, cache); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatal("aggregate limit not enforced", err)
	}
}

func TestProfiledSourceRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string){
		"missing profiles": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), "schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\n")
		},
		"empty profiles": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), "schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\nprofiles: []\n")
		},
		"null profiles": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), "schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\nprofiles: null\n")
		},
		"legacy build field": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), string(readFile(t, filepath.Join(r, "workspace.yaml")))+"buildProfile: ''\n")
		},
		"mixed member": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "workspace.yaml"), string(readFile(t, filepath.Join(r, "workspace.yaml")))+"members: []\n")
		},
		"legacy profiles": func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.Replace(string(readFile(t, p)), "schemaVersion: 3", "schemaVersion: 1\nbuildProfile: build-profiles/default.json", 1))
		},
		"runtime":      func(t *testing.T, r string) { setMemberRuntime(t, r, "runtime: {id: orka}\n") },
		"null runtime": func(t *testing.T, r string) { setMemberRuntime(t, r, "runtime: null\n") },
		"unknown row": func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.Replace(string(readFile(t, p)), "platform: linux/amd64", "unknown: field\n    platform: linux/amd64", 1))
		},
		"escape": func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "runtime-profiles/orka.json", "../orka.json"))
		},
		"symlink": func(t *testing.T, r string) {
			p := filepath.Join(r, "runtime-profiles/orka.json")
			data := readFile(t, p)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(r, "runtime-profiles/real.json"), string(data))
			if err := os.Symlink("real.json", p); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, r string) {
			p := filepath.Join(r, "runtime-profiles/orka.json")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"oversize": func(t *testing.T, r string) {
			writeFile(t, filepath.Join(r, "runtime-profiles/orka.json"), strings.Repeat(" ", 1<<20+1))
		},
		"duplicate tuple": func(t *testing.T, r string) {
			p := filepath.Join(r, "workspace.yaml")
			writeFile(t, p, strings.ReplaceAll(string(readFile(t, p)), "runtime-profiles/sessions.json", "runtime-profiles/orka.json"))
		},
		"conflicting id": func(t *testing.T, r string) {
			p := filepath.Join(r, "runtime-profiles/sessions.json")
			writeFile(t, p, strings.Replace(string(readFile(t, p)), `"id":"sessions"`, `"id":"orka"`, 1))
		},
		"harness mismatch": func(t *testing.T, r string) {
			p := filepath.Join(r, "runtime-profiles/orka.json")
			var v agentsuite.RuntimeProfile
			if err := agentsuite.Unmarshal(readFile(t, p), &v); err != nil {
				t.Fatal(err)
			}
			v.Harness.Digest = "sha256:" + strings.Repeat("0", 64)
			b, err := agentsuite.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, p, string(b))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := profiledWorkspace(t)
			mutate(t, r)
			if _, err := workspace.Prepare(t.Context(), r, c); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
