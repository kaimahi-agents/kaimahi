package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
)

func TestKagentPackLoadAndExactLayoutTransfer(t *testing.T) {
	root, cache := createWorkspace(t)
	setMemberRuntime(t, root, "runtime: {id: kagent, version: v0.10.3, engine: go}\n")
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Public question\nexpectContains: [answer]\n")
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if suite.Runtime != (workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) {
		t.Fatal("ambiguous loaded runtime", suite.Runtime)
	}
	if suite.RendererVersion != "kmx.native-kagent/v2" || suite.RequiredImages == nil || len(suite.RequiredImages) != 0 {
		t.Fatal("wrong native profile", suite)
	}
	out := extract(t, p)
	var record map[string]any
	if err := json.Unmarshal(readFile(t, filepath.Join(out, "metadata/kmx/native-kagent.json")), &record); err != nil {
		t.Fatal(err)
	}
	if record["executionProfile"] != "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1" {
		t.Fatal("unbound execution contract", record)
	}
	if _, err := os.Stat(filepath.Join(out, "metadata/kmx/native-orka.json")); !os.IsNotExist(err) {
		t.Fatal("two native profiles")
	}
	layout := filepath.Join(t.TempDir(), "copy")
	copied, err := suiteoras.PushLayout(t.Context(), p.Path, layout, "copy", p.Descriptor)
	if err != nil || copied.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("substituted S", err)
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
	copyP := *p
	copyP.Path = layout
	loaded, err := workspace.Load(t.Context(), &copyP)
	if err != nil || !reflect.DeepEqual(suite, loaded) {
		t.Fatal("layout copy changed packaged input", err)
	}
	for _, c := range loaded.Cases {
		if !bytes.Equal(c.Bytes, readFile(t, filepath.Join(out, c.Path))) {
			t.Fatal("case bytes changed")
		}
	}
}

func TestRuntimeSelectionBindsSButPreservesOrkaArtifact(t *testing.T) {
	root, cache := createWorkspace(t)
	setMemberRuntime(t, root, "")
	omitted, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	setMemberRuntime(t, root, "runtime: {id: orka}\n")
	explicit, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if omitted.Descriptor.Digest != explicit.Descriptor.Digest || omitted.Fingerprint == explicit.Fingerprint {
		t.Fatal("explicit Orka changed artifact or retained source identity")
	}
	before, err := workspace.Load(t.Context(), explicit)
	if err != nil {
		t.Fatal(err)
	}
	setMemberRuntime(t, root, "runtime: {id: kagent, version: v0.10.3, engine: go}\n")
	if err := workspace.CheckCurrent(t.Context(), root, explicit); err == nil {
		t.Fatal("runtime edit retained old candidate")
	}
	after, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := workspace.Load(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	if after.Descriptor.Digest == explicit.Descriptor.Digest || after.Fingerprint == explicit.Fingerprint || loaded.GraphDigest != before.GraphDigest {
		t.Fatal("runtime identity not bound separately from G")
	}
}

func TestLoadUsesVerifiedPackagedSnapshot(t *testing.T) {
	root, cache := createWorkspace(t)
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Public question\nexpectContains: [answer]\n")
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	graph := readFile(t, filepath.Join(out, "agentsuite.json"))
	instructions := []byte("Answer briefly.\n\nExact whitespace.\n")
	want := &workspace.NativeSuite{
		Digest: p.Descriptor.Digest.String(), GraphDigest: digest.FromBytes(graph).String(),
		Name: "greeter", MemberID: "greeter", Description: "Public questions", Model: "qwen-3.5-2b",
		Instructions: instructions, InstructionsDigest: digest.FromBytes(instructions).String(),
		MemberDigest:       digest.FromBytes(readFile(t, filepath.Join(out, "agents/greeter.json"))).String(),
		CompositionDigest:  digest.FromBytes(readFile(t, filepath.Join(out, "compositions/greeter-linux-amd64.json"))).String(),
		BuildProfileDigest: digest.FromBytes(readFile(t, filepath.Join(out, "build-profiles/default.json"))).String(),
		RendererVersion:    "kmx.native-orka/v2", Runtime: workspace.RuntimeProfile{ID: "orka"}, RequiredImages: []string{},
	}
	for _, id := range []string{"example", "second"} {
		p := "evals/greeter/cases/" + id + ".json"
		b := readFile(t, filepath.Join(out, p))
		want.Cases = append(want.Cases, workspace.PackagedCase{Path: p, Digest: digest.FromBytes(b).String(), Bytes: b})
	}
	record := filepath.Join(filepath.Dir(p.Path), "prepared.json")
	writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Changed instructions\n")
	writeFile(t, record, "not an execution input")
	got, err := workspace.Load(context.Background(), p)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("edited source or bad record substituted packaged inputs", err)
	}
	got.Instructions[0], got.Cases[0].Bytes[0] = 'X', 'X'
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	got, err = workspace.Load(context.Background(), p)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("removed source/record or caller mutation changed packaged inputs", err)
	}
}
