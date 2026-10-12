package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Substituting source fingerprints, build-image pins or graph identity for the
// verified packaged identities, or omitting a case, must break this contract.
func TestLoadExactNativePackagedInput(t *testing.T) {
	root, cache := createWorkspace(t)
	writeFile(t, filepath.Join(root, "eval/zeta.yaml"), "id: zeta\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
	writeFile(t, filepath.Join(root, "eval/alpha.yml"), "id: alpha\ninput: What is 1+1?\nexpectContains: [\"2\"]\n")
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	rootBytes := readFile(t, filepath.Join(out, "agentsuite.json"))
	var suite spec.Suite
	if err := spec.Unmarshal(rootBytes, &suite); err != nil {
		t.Fatal(err)
	}
	var agent spec.Agent
	if err := spec.Unmarshal(readFile(t, filepath.Join(out, suite.Agents[0].Path)), &agent); err != nil {
		t.Fatal(err)
	}
	want := &workspace.NativeSuite{
		Digest:             p.Descriptor.Digest.String(),
		GraphDigest:        godigest.FromBytes(rootBytes).String(), // prepared public JSON is JCS
		Name:               "greeter",
		MemberID:           "greeter",
		Description:        "Public questions",
		Model:              "qwen-3.5-2b",
		Instructions:       []byte("Answer briefly.\n\nExact whitespace.\n"),
		MemberDigest:       suite.Agents[0].Digest,
		InstructionsDigest: agent.Instructions.Digest,
		CompositionDigest:  suite.Compositions[0].Digest,
		BuildProfileDigest: suite.BuildProfiles[0].Digest,
		RendererVersion:    "kmx.native-orka/v1",
		RequiredImages:     []string{},
	}
	for _, name := range []string{"alpha.yml", "example.yaml", "zeta.yaml"} {
		b := readFile(t, filepath.Join(root, "eval", name))
		want.Cases = append(want.Cases, workspace.PackagedCase{Path: "metadata/kmx/eval/" + name, Digest: godigest.FromBytes(b).String(), Bytes: b})
	}
	for _, ref := range []spec.FileRef{
		{Path: suite.Agents[0].Path, Digest: want.MemberDigest},
		agent.Instructions,
		{Path: suite.Compositions[0].Path, Digest: want.CompositionDigest},
		{Path: suite.BuildProfiles[0].Path, Digest: want.BuildProfileDigest},
	} {
		if got := godigest.FromBytes(readFile(t, filepath.Join(out, ref.Path))).String(); got != ref.Digest {
			t.Fatalf("fixture reference %s does not identify its packaged bytes: %s != %s", ref.Path, got, ref.Digest)
		}
	}
	got, err := workspace.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packaged input mismatch:\n got: %+v\nwant: %+v", got, want)
	}
	if got.Digest == got.GraphDigest || got.Digest == p.Fingerprint {
		t.Fatal("suite manifest identity was replaced by source/graph identity")
	}
}

// Authored edits and caller mutation must not substitute the immutable candidate.
func TestLoadUsesPreparedBytesNotLiveSourceOrCacheRecord(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	original, err := workspace.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Changed instructions\n")
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
	member := filepath.Join(root, "agents/greeter/member.yaml")
	writeFile(t, member, strings.ReplaceAll(string(readFile(t, member)), "qwen-3.5-2b", "different-model"))
	writeFile(t, filepath.Join(filepath.Dir(p.Path), "prepared.json"), "invalid cache record")
	if err := workspace.CheckCurrent(context.Background(), root, p); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("source edit was not detected: %v", err)
	}
	loaded, err := workspace.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, original) {
		t.Fatal("live source or cache record replaced prepared bytes/identity")
	}
	loaded.Instructions[0] = 'X'
	loaded.Cases[0].Bytes[0] = 'X'
	loaded.Cases[0].Path = "substituted"
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	again, err := workspace.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, original) {
		t.Fatal("caller mutation or source removal changed packaged input")
	}
}

// Load must not return partially decoded input on invalid identities, canceled
// verification, descriptor substitution or damaged manifest/content bytes.
func TestLoadRefusesInvalidPreparedArtifact(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got, err := workspace.Load(context.Background(), nil); err == nil || got != nil {
			t.Fatalf("nil artifact result: %+v, %v", got, err)
		}
	})
	for _, change := range []string{"canceled", "fingerprint", "name", "descriptor size", "descriptor digest", "descriptor artifact type", "manifest", "layer"} {
		t.Run(change, func(t *testing.T) {
			root, cache := createWorkspace(t)
			p, err := workspace.Prepare(context.Background(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch change {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "fingerprint":
				p.Fingerprint = "not-a-digest"
			case "name":
				p.Name = "other"
			case "descriptor size":
				p.Descriptor.Size++
			case "descriptor digest":
				p.Descriptor.Digest = godigest.FromString("other manifest")
			case "descriptor artifact type":
				p.Descriptor.ArtifactType = "unexpected"
			case "manifest", "layer":
				name := filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded())
				if change == "layer" {
					var manifest ocispec.Manifest
					if err := spec.Unmarshal(readFile(t, name), &manifest); err != nil {
						t.Fatal(err)
					}
					name = filepath.Join(p.Path, "blobs/sha256", manifest.Layers[0].Digest.Encoded())
				}
				if err := os.Chmod(name, 0o600); err != nil {
					t.Fatal(err)
				}
				writeFile(t, name, "corrupt bytes")
			}
			got, err := workspace.Load(ctx, p)
			if err == nil || got != nil {
				t.Fatalf("accepted %s or returned partial input: %+v, %v", change, got, err)
			}
			if change == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
