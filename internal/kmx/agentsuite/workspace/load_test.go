package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
)

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
		RendererVersion:    "kmx.native-orka/v1", RequiredImages: []string{},
	}
	for _, name := range []string{"example.yaml", "second.yml"} {
		b := readFile(t, filepath.Join(root, "eval", name))
		want.Cases = append(want.Cases, workspace.PackagedCase{Path: "metadata/kmx/eval/" + name, Digest: digest.FromBytes(b).String(), Bytes: b})
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
