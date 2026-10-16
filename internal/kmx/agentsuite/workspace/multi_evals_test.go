package workspace_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// A source filename orders evaluation; a case ID names the public JSON path.
func TestMultiCaseOrderAndIdentityChanges(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 1)
	writeFile(t, filepath.Join(root, "eval/member-01/a-first.yaml"), "id: z-first\ninput: Question\nruns: 3\nminPasses: 2\nexpectContains: [answer]\n")
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.LoadInventory(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if got := before.Members[1].Cases; len(got) != 2 || got[0].Path != "evals/member-01/cases/z-first.json" || got[1].Path != "evals/member-01/cases/case-000.json" {
		t.Fatal("cases were sorted by ID instead of authored filename", got)
	}
	file := filepath.Join(root, "eval/member-01/a-first.yaml")
	writeFile(t, file, string(readFile(t, file))+"# raw source spelling only\n")
	comment, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	afterComment, err := workspace.LoadInventory(t.Context(), comment)
	if err != nil {
		t.Fatal(err)
	}
	if comment.Fingerprint == p.Fingerprint || comment.Descriptor.Digest != p.Descriptor.Digest || !reflect.DeepEqual(before, afterComment) {
		t.Fatal("raw comment must change fingerprint without changing packaged identity or bytes")
	}
	writeFile(t, file, strings.ReplaceAll(string(readFile(t, file)), "input: Question", "input: Different question"))
	changed, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	after, err := workspace.LoadInventory(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == comment.Fingerprint || changed.Descriptor.Digest == comment.Descriptor.Digest || after.GraphDigest == before.GraphDigest || after.Members[1].Cases[0].Digest == before.Members[1].Cases[0].Digest {
		t.Fatal("case change did not bind fingerprint, public graph, artifact and case identity")
	}
	if !reflect.DeepEqual(after.Members[0].Cases, before.Members[0].Cases) {
		t.Fatal("editing second member changed first member cases")
	}
}

// Every inventory entry point must preserve the same safe decoder refusal.
func TestMultiInventoryNamesUnsupportedPublicCases(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 1)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mutate func(*agentsuite.EvaluationIndex, map[string]any)
		want   string
	}{
		"non-critical": {func(_ *agentsuite.EvaluationIndex, c map[string]any) {
			c["assertions"].([]any)[0].(map[string]any)["critical"] = false
		}, "packaged evaluation assertion is non-critical; KMX evaluates every assertion as critical"},
		"runs over limit": {func(_ *agentsuite.EvaluationIndex, c map[string]any) { c["runs"] = 101 }, "evaluation policy requires 1 <= minPasses <= runs <= 100"},
		"extension": {func(_ *agentsuite.EvaluationIndex, c map[string]any) {
			a := c["assertions"].([]any)[0].(map[string]any)
			a["id"], a["type"], a["critical"] = "extension", "example.custom", false
		}, "packaged evaluation assertion is non-critical; KMX evaluates every assertion as critical"},
	} {
		t.Run(name, func(t *testing.T) {
			out := extract(t, p)
			rebindMemberEvals(t, out, "member-01", tc.mutate)
			assertMultiInventoryRefused(t, p, out, "packaged native suite verification failed: "+tc.want)
		})
	}
}

func TestMultiPublicValidationRefusesCrossMemberCases(t *testing.T) {
	root, cache := multiWorkspace(t, 2, 1)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	rebindMemberEvals(t, out, "member-01", func(index *agentsuite.EvaluationIndex, _ map[string]any) {
		index.Cases[0].Path = "evals/member-00/cases/case-000.json"
	})
	if _, err := suiteoras.Push(t.Context(), out, filepath.Join(t.TempDir(), "layout"), "cross-member"); err == nil || !strings.Contains(err.Error(), "owning agent") {
		t.Fatal("cross-member case reference was not refused by public graph validation", err)
	}
}

// The fixtures must remain public-valid so failures exercise native verification.
func assertMultiInventoryRefused(t *testing.T, p *workspace.Prepared, out, want string) {
	t.Helper()
	layout := filepath.Join(t.TempDir(), "layout")
	result, err := suiteoras.Push(t.Context(), out, layout, "mutated")
	if err != nil {
		t.Fatal("negative fixture is not public-valid", err)
	}
	if err := filepath.WalkDir(layout, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chmod(path, 0700)
	}); err != nil {
		t.Fatal(err)
	}
	candidate := *p
	candidate.Path, candidate.Descriptor = layout, result.Descriptor
	if err := workspace.Verify(t.Context(), &candidate); err == nil {
		t.Fatal("invalid multi graph verified")
	}
	if got, err := workspace.LoadInventory(t.Context(), &candidate); got != nil || err == nil || err.Error() != want {
		t.Fatalf("prepared inventory refusal = %v, want %q", err, want)
	}
	if _, err := workspace.Load(t.Context(), &candidate); err == nil || errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatal("invalid snapshot incorrectly received valid-multi sentinel", err)
	}
	a, err := suiteoras.ReadArtifactLayout(t.Context(), layout, result.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := workspace.LoadArtifactInventory(t.Context(), a); got != nil || err == nil || err.Error() != want {
		t.Fatalf("source-free inventory refusal = %v, want %q", err, want)
	}
	if _, err := workspace.LoadArtifact(t.Context(), a); err == nil || errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatal("invalid artifact received valid-multi sentinel", err)
	}
}

func packagedTarEntries(t *testing.T, p *workspace.Prepared) int {
	t.Helper()
	blob := func(d string) []byte {
		return readFile(t, filepath.Join(p.Path, "blobs/sha256", strings.TrimPrefix(d, "sha256:")))
	}
	var manifest ocispec.Manifest
	if err := agentsuite.Unmarshal(blob(p.Descriptor.Digest.String()), &manifest); err != nil || len(manifest.Layers) != 1 {
		t.Fatal("invalid test manifest", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(blob(manifest.Layers[0].Digest.String())))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr, entries := tar.NewReader(gz), 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		entries++
	}
}
