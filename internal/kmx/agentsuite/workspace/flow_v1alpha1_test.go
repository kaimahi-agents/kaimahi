package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const authoredFlow = `{"schemaVersion":"kmx.flow/v1alpha1","id":"release","mode":"pipeline","entry":"writer","edgeRequirements":[],"steps":[{"id":"draft","member":"writer","input":{"source":"run.input"}},{"id":"review","member":"reviewer","input":{"source":"draft.output","context":"run.input"},"approval":"policy-controlled"}],"cases":[{"id":"facts","input":"frozen sources","runs":3,"minPasses":2,"assertions":[{"step":"review","check":{"id":"verdict","type":"contains","value":"Verdict: PASS"}}]}]}`

func flowCreateInputs(t *testing.T) (string, workspace.FlowCreateOptions) {
	t.Helper()
	in := t.TempDir()
	writeFile(t, filepath.Join(in, "writer.md"), "Write grounded release notes.\n")
	writeFile(t, filepath.Join(in, "reviewer.md"), "Review the actual draft.\n")
	writeFile(t, filepath.Join(in, "facts.yaml"), "id: facts\ninput: frozen sources\nexpectContains: [facts]\n")
	writeFile(t, filepath.Join(in, "members.json"), `{"schemaVersion":"kmx.members/v1alpha1","members":[{"id":"writer","model":"test-writer","instructions":"writer.md","cases":["facts.yaml"]},{"id":"reviewer","model":"test-reviewer","instructions":"reviewer.md","cases":["facts.yaml"]}]}`)
	writeFile(t, filepath.Join(in, "flow.json"), authoredFlow)
	return filepath.Join(t.TempDir(), "team"), workspace.FlowCreateOptions{Name: "release-team", MembersFile: filepath.Join(in, "members.json"), FlowFile: filepath.Join(in, "flow.json")}
}

// Losing the critical extension, native member coverage or original artifact
// identity must break the round trip, not quietly select an ordinary member.
func TestFlowCreatePrepareRoundTrip(t *testing.T) {
	ctx := context.Background()
	root, opt := flowCreateInputs(t)
	if err := workspace.CreateFlow(ctx, root, opt); err != nil {
		t.Fatal(err)
	}
	if resolved, err := workspace.Resolve(root, ""); err != nil || resolved != root {
		t.Fatalf("resolve: %q %v", resolved, err)
	}
	strictCache := filepath.Join(t.TempDir(), "strict-cache")
	if _, err := workspace.Prepare(ctx, root, strictCache); err == nil {
		t.Fatal("default prepare accepted flow")
	}
	if _, err := os.Stat(strictCache); !os.IsNotExist(err) {
		t.Fatalf("default reader wrote cache: %v", err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	p, err := workspace.PrepareFlow(ctx, root, cache)
	if err != nil {
		t.Fatal(err)
	}
	again, err := workspace.PrepareFlow(ctx, root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatal("cache changed the prepared identity")
	}
	inv, err := workspace.LoadFlowInventory(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Digest != p.Descriptor.Digest.String() || inv.Name != "release-team" || len(inv.Members) != 2 || inv.Members[0].MemberID != "reviewer" || inv.Members[1].MemberID != "writer" {
		t.Fatalf("incomplete inventory: %+v", inv)
	}
	if inv.Flow.ID != "release" || inv.Flow.Steps[1].Input.Source != "draft.output" || *inv.Flow.Cases[0].Runs != 3 {
		t.Fatalf("wrong flow: %+v", inv.Flow)
	}
	artifact, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	fromArtifact, err := workspace.LoadFlowArtifactInventory(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, fromArtifact) {
		t.Fatal("source-free inventory differs")
	}
	if _, err := agentsuite.ValidatePath(p.Path); err == nil {
		t.Fatal("default suite reader accepted critical flow")
	}
	if _, err := localartifact.ReadLayout(ctx, p.Path, ""); err == nil {
		t.Fatal("default artifact reader accepted critical flow")
	}
	if _, err := workspace.Load(ctx, p); err == nil {
		t.Fatal("execution loader accepted flow")
	}
	if _, err := workspace.LoadInventory(ctx, p); err == nil {
		t.Fatal("ordinary inventory accepted flow")
	}
	if _, err := workspace.LoadArtifact(ctx, artifact); err == nil {
		t.Fatal("artifact execution loader accepted flow")
	}
	if _, err := workspace.LoadArtifactInventory(ctx, artifact); err == nil {
		t.Fatal("ordinary artifact inventory accepted flow")
	}
	g, err := agentsuite.ValidatePathFlowV1Alpha1(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if inv.FlowDigest != g.FlowDigest() || artifact.Descriptor().Digest != p.Descriptor.Digest {
		t.Fatal("original S/F lost")
	}
	inv.Flow.Steps[0].Member = "changed"
	*inv.Flow.Cases[0].Runs = 99
	inv.Members[0].Instructions[0] = 'X'
	fresh, err := workspace.LoadFlowArtifactInventory(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Flow.Steps[0].Member != "writer" || *fresh.Flow.Cases[0].Runs != 3 || string(fresh.Members[0].Instructions) != "Review the actual draft.\n" {
		t.Fatal("returned inventory aliases retained data")
	}
	before := authoredTree(t, root)
	if err := workspace.CreateFlow(ctx, root, opt); err == nil {
		t.Fatal("overwrote occupied root")
	}
	if !reflect.DeepEqual(before, authoredTree(t, root)) {
		t.Fatal("occupied create changed files")
	}
}

// A 1 MiB public-file ceiling must not accidentally truncate a valid flow.
func TestFlowLargeDocumentAndNonFlowLimit(t *testing.T) {
	for _, size := range []int{(1 << 20) + 1024, 4 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			root, opt := flowCreateInputs(t)
			large := strings.Replace(authoredFlow, "frozen sources", strings.Repeat("x", size-len(authoredFlow)+len("frozen sources")), 1)
			writeFile(t, opt.FlowFile, large)
			if err := workspace.CreateFlow(ctx, root, opt); err != nil {
				t.Fatal(err)
			}
			p, err := workspace.PrepareFlow(ctx, root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			inv, err := workspace.LoadFlowInventory(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			a, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, p.Path, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Files()[agentsuite.FlowPathV1Alpha1]) != size || len(inv.Flow.Cases[0].Input) != size-len(authoredFlow)+len("frozen sources") {
				t.Fatal("flow input truncated")
			}
			writeFile(t, filepath.Join(root, "agents/writer/instructions.md"), strings.Repeat("x", (1<<20)+1))
			cache := filepath.Join(t.TempDir(), "oversize-cache")
			if _, err := workspace.PrepareFlow(ctx, root, cache); err == nil {
				t.Fatal("flow mode broadened instruction size limit")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid source wrote cache")
			}
		})
	}
}

func TestFlowCreateRefusesBeforeDestinationWrites(t *testing.T) {
	for _, kind := range []string{"escape", "symlink", "duplicate", "unknown", "null", "oversize", "bad-reference", "invalid-case", "missing-input"} {
		t.Run(kind, func(t *testing.T) {
			root, opt := flowCreateInputs(t)
			switch kind {
			case "escape":
				writeFile(t, opt.MembersFile, strings.Replace(string(readFile(t, opt.MembersFile)), "writer.md", "../writer.md", 1))
			case "symlink":
				if err := os.Symlink("writer.md", filepath.Join(filepath.Dir(opt.MembersFile), "alias.md")); err != nil {
					t.Skip(err)
				}
				writeFile(t, opt.MembersFile, strings.Replace(string(readFile(t, opt.MembersFile)), "writer.md", "alias.md", 1))
			case "duplicate":
				writeFile(t, opt.MembersFile, strings.Replace(string(readFile(t, opt.MembersFile)), `"model":"test-writer"`, `"model":"test-writer","model":"other"`, 1))
			case "unknown":
				writeFile(t, opt.MembersFile, strings.Replace(string(readFile(t, opt.MembersFile)), `"id":"writer"`, `"id":"writer","runtime":"orka"`, 1))
			case "null":
				writeFile(t, opt.FlowFile, strings.Replace(authoredFlow, `"runs":3`, `"runs":null`, 1))
			case "oversize":
				writeFile(t, opt.FlowFile, strings.Repeat(" ", (4<<20)+1))
			case "bad-reference":
				writeFile(t, opt.FlowFile, strings.Replace(authoredFlow, `"draft.output"`, `"missing.output"`, 1))
			case "invalid-case":
				writeFile(t, filepath.Join(filepath.Dir(opt.MembersFile), "facts.yaml"), "id: facts\nunknown: nope\n")
			case "missing-input":
				opt.FlowFile = ""
			}
			if err := workspace.CreateFlow(context.Background(), root, opt); err == nil {
				t.Fatal("accepted invalid input")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("invalid create touched destination: %v", err)
			}
		})
	}
}

func TestFlowSourceChangesInvalidatePrepared(t *testing.T) {
	ctx := context.Background()
	root, opt := flowCreateInputs(t)
	if err := workspace.CreateFlow(ctx, root, opt); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	p, err := workspace.PrepareFlow(ctx, root, cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agents/writer/instructions.md", "coordination/flow.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			before := readFile(t, path)
			replacement := append(append([]byte{}, before...), '\n')
			if strings.HasSuffix(name, "flow.json") {
				replacement = []byte(strings.Replace(string(before), "frozen sources", "different sources", 1))
			}
			writeFile(t, path, string(replacement))
			next, err := workspace.PrepareFlow(ctx, root, cache)
			if err != nil {
				t.Fatal(err)
			}
			if next.Fingerprint == p.Fingerprint || next.Descriptor.Digest == p.Descriptor.Digest {
				t.Fatal("source edit retained old identity")
			}
			writeFile(t, path, string(before))
		})
	}
}

func TestFlowNativeClosureAndDefaultPackingRefusals(t *testing.T) {
	ctx := context.Background()
	root, opt := flowCreateInputs(t)
	if err := workspace.CreateFlow(ctx, root, opt); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.PrepareFlow(ctx, root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, p.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"extra-file", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			files := a.Files()
			if kind == "extra-file" {
				files["extra.txt"] = []byte("not in the native graph")
			} else {
				files["metadata/kmx/native-multi.json"] = []byte(strings.Replace(string(files["metadata/kmx/native-multi.json"]), "kmx.native-multi/v2", "changed", 1))
			}
			source := t.TempDir()
			for name, b := range files {
				writeFile(t, filepath.Join(source, name), string(b))
			}
			strict := filepath.Join(t.TempDir(), "strict")
			if _, err := suiteoras.Push(ctx, source, strict, "flow"); err == nil {
				t.Fatal("default packer accepted critical flow")
			}
			if _, err := os.Stat(strict); !os.IsNotExist(err) {
				t.Fatal("default flow refusal published layout")
			}
			layout := filepath.Join(t.TempDir(), "layout")
			result, err := suiteoras.PrepareFlowV1Alpha1Layout(ctx, source, layout, "flow")
			if err != nil {
				t.Fatal("public graph fixture must be valid", err)
			}
			changed, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, layout, result.Descriptor.Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.LoadFlowArtifactInventory(ctx, changed); err == nil {
				t.Fatal("accepted graph outside complete native regeneration")
			}
		})
	}
	for _, kind := range []string{"name", "descriptor", "fingerprint", "roots"} {
		bad := *p
		switch kind {
		case "name":
			bad.Name = "other"
		case "descriptor":
			bad.Descriptor.Size++
		case "fingerprint":
			bad.Fingerprint = "not-a-digest"
		case "roots":
			var index map[string]any
			if err := json.Unmarshal(readFile(t, filepath.Join(p.Path, "index.json")), &index); err != nil {
				t.Fatal(err)
			}
			roots := index["manifests"].([]any)
			index["manifests"] = append(roots, roots[0])
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(p.Path, "index.json"), string(raw))
		}
		if _, err := workspace.LoadFlowInventory(ctx, &bad); err == nil {
			t.Fatalf("accepted substituted %s", kind)
		}
	}
}

func TestFlowCanceledInventoryPreservesIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.LoadFlowInventory(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := workspace.LoadFlowArtifactInventory(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFlowDecodedCredentialsRefuseBeforePersistence(t *testing.T) {
	key := strings.Repeat("a1b2", 8)
	secret := "facts\n" + key + "\nend"
	if secretshapes.Match(secret) == nil {
		t.Fatal("synthetic decoded fixture must match detector")
	}
	for _, field := range []string{"input", "regex"} {
		t.Run(field, func(t *testing.T) {
			var flow agentsuite.FlowV1Alpha1
			if err := json.Unmarshal([]byte(authoredFlow), &flow); err != nil {
				t.Fatal(err)
			}
			if field == "input" {
				flow.Cases[0].Input = secret
			} else {
				check := &flow.Cases[0].Assertions[0].Check
				check.Type = field
				check.Value = secret
				if field == "regex" {
					check.Value = ""
					check.Pattern = secret
				}
			}
			raw, err := json.Marshal(flow)
			if err != nil {
				t.Fatal(err)
			}
			if secretshapes.Match(string(raw)) != nil {
				t.Fatal("fixture must reproduce JSON boundary masking")
			}
			root, opt := flowCreateInputs(t)
			writeFile(t, opt.FlowFile, string(raw))
			err = workspace.CreateFlow(t.Context(), root, opt)
			if err == nil {
				t.Error("CreateFlow persisted decoded credential-shaped text")
			} else if strings.Contains(err.Error(), key) {
				t.Error("error disclosed synthetic credential")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Error("rejected create touched destination")
			}
			clean, cleanOpt := flowCreateInputs(t)
			if err := workspace.CreateFlow(t.Context(), clean, cleanOpt); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(clean, "coordination/flow.json"), string(raw))
			cache := filepath.Join(t.TempDir(), "credential-cache")
			_, err = workspace.PrepareFlow(t.Context(), clean, cache)
			if err == nil {
				t.Error("PrepareFlow persisted decoded credential-shaped text")
			} else if strings.Contains(err.Error(), key) {
				t.Error("error disclosed synthetic credential")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Error("rejected prepare wrote cache")
			}
		})
	}
}

func TestFlowCreateWithSymlinkedSystemTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix TMPDIR symlink boundary")
	}
	root, opt := flowCreateInputs(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skip(err)
	}
	t.Setenv("TMPDIR", alias)
	if err := workspace.CreateFlow(t.Context(), root, opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "workspace.yaml")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(real)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary stages not cleaned", err)
	}
}

func TestFlowMaximumMemberAndCaseInventory(t *testing.T) {
	ctx := context.Background()
	root, opt := flowCreateInputs(t)
	in := filepath.Dir(opt.MembersFile)
	rows := []map[string]any{}
	for i := range 16 {
		id := fmt.Sprintf("member-%02d", i)
		paths := []string{}
		for j := range 8 {
			if i == 15 && j == 7 {
				continue
			} // 127 member cases + one flow case = 128.
			name := fmt.Sprintf("case-%03d.yaml", j)
			paths = append(paths, name)
			writeFile(t, filepath.Join(in, name), fmt.Sprintf("id: case-%03d\ninput: frozen facts\nexpectContains: [facts]\n", j))
		}
		rows = append(rows, map[string]any{"id": id, "model": "test-model", "instructions": "writer.md", "cases": paths})
	}
	b, err := json.Marshal(map[string]any{"schemaVersion": "kmx.members/v1alpha1", "members": rows})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, opt.MembersFile, string(b))
	writeFile(t, opt.FlowFile, strings.NewReplacer("writer", "member-00", "reviewer", "member-01").Replace(authoredFlow))
	if err := workspace.CreateFlow(ctx, root, opt); err != nil {
		t.Fatal(err)
	}
	// The maximal closed inventory also includes one optional policy per member.
	for i := range 16 {
		declarePolicy(t, root, fmt.Sprintf("member-%02d", i), networkPolicy(policyEndpointA))
	}
	p, err := workspace.PrepareFlow(ctx, root, filepath.Join(t.TempDir(), "policy-cache"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := localartifact.ReadLayoutFlowV1Alpha1(ctx, p.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Files()) != 212 || packagedTarEntries(t, p) > 256 {
		t.Fatalf("maximum inventory changed: %d files, %d entries", len(a.Files()), packagedTarEntries(t, p))
	}
	inv, err := workspace.LoadFlowInventory(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, m := range inv.Members {
		total += len(m.Cases)
	}
	if len(inv.Members) != 16 || total != 127 {
		t.Fatalf("maximum inventory truncated: %d/%d", len(inv.Members), total)
	}
	writeFile(t, filepath.Join(root, "eval/member-15/case-007.yaml"), "id: case-007\ninput: facts\nexpectContains: [facts]\n")
	tooMany := filepath.Join(t.TempDir(), "too-many")
	if _, err := workspace.PrepareFlow(ctx, root, tooMany); err == nil {
		t.Fatal("accepted 129 combined member/flow cases")
	}
	if _, err := os.Stat(tooMany); !os.IsNotExist(err) {
		t.Fatal("over-budget graph wrote cache")
	}
}
