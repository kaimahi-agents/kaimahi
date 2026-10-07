package oras_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	orasbinding "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
)

func TestFileTargetsPackPushPullAgentSuite(t *testing.T) {
	ctx := context.Background()
	suite, src, contentDescriptor := newAgentSuiteSource(t, "file-target-e2e")

	packedTarget := newFileTarget(t)
	packed, err := orasbinding.New(nil).Pack(ctx, src, packedTarget, contentDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if packed.Report == nil || packed.Report.Name != suite.Name {
		t.Fatalf("packed report = %+v, want suite %q", packed.Report, suite.Name)
	}

	remoteTarget := newFileTarget(t)
	pushedRoot, err := orasbinding.NewPusher(remoteTarget).Push(
		ctx,
		packedTarget,
		packed.Descriptor,
		testReference,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameDescriptor(t, pushedRoot, packed.Descriptor)

	resolvedRoot, err := remoteTarget.Resolve(ctx, testReference)
	if err != nil {
		t.Fatal(err)
	}
	assertSameDescriptor(t, resolvedRoot, packed.Descriptor)

	pulledTarget := newFileTarget(t)
	pulledRoot, err := orasbinding.NewPuller(remoteTarget).Pull(
		ctx,
		testReference,
		pulledTarget,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameDescriptor(t, pulledRoot, packed.Descriptor)

	report, err := (orasbinding.LayoutValidator{}).Validate(ctx, pulledTarget, pulledRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Name != suite.Name ||
		report.Agents != len(suite.Agents) ||
		report.Compositions != len(suite.Compositions) {
		t.Fatalf("pulled report = %+v, want suite %+v", report, suite)
	}
	if _, err := pulledTarget.Resolve(ctx, testReference); err == nil {
		t.Fatal("Pull() assigned the remote reference to the local target")
	}
}

func TestPackedFileTargetTimestampsDoNotChangeDescriptor(t *testing.T) {
	ctx := context.Background()
	_, src, contentDescriptor := newAgentSuiteSource(t, "file-target-e2e")
	packedRoot := t.TempDir()
	packedTarget, err := oci.New(packedRoot)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := orasbinding.New(nil).Pack(ctx, src, packedTarget, contentDescriptor)
	if err != nil {
		t.Fatal(err)
	}

	timestamp := time.Unix(2_000_000_000, 0).UTC()
	if err := filepath.WalkDir(packedRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			return os.Chtimes(path, timestamp, timestamp)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	report, err := (orasbinding.LayoutValidator{}).Validate(ctx, packedTarget, packed.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if report.Name != "file-target-e2e" {
		t.Fatalf("validated suite = %q, want file-target-e2e", report.Name)
	}

	remoteTarget := newFileTarget(t)
	pushedRoot, err := orasbinding.NewPusher(remoteTarget).Push(
		ctx,
		packedTarget,
		packed.Descriptor,
		testReference,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameDescriptor(t, pushedRoot, packed.Descriptor)
	resolvedRoot, err := remoteTarget.Resolve(ctx, testReference)
	if err != nil {
		t.Fatal(err)
	}
	assertSameDescriptor(t, resolvedRoot, packed.Descriptor)
}

func TestPushAddsMultipleAgentSuiteRepositoriesToOneLayout(t *testing.T) {
	ctx := context.Background()
	layoutRoot := t.TempDir()
	layout, err := oci.New(layoutRoot)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		reference string
	}{
		{name: "writer-alpha", reference: "agentsuites/alpha:v1"},
		{name: "writer-beta", reference: "agentsuites/beta:v1"},
	}
	roots := make(map[string]ocispec.Descriptor, len(tests))
	for _, test := range tests {
		suite, src, contentDescriptor := newAgentSuiteSource(t, test.name)
		packedTarget := newFileTarget(t)
		packed, err := orasbinding.New(nil).Pack(ctx, src, packedTarget, contentDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		pushed, err := orasbinding.NewPusher(layout).Push(
			ctx,
			packedTarget,
			packed.Descriptor,
			test.reference,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertSameDescriptor(t, pushed, packed.Descriptor)
		roots[test.reference] = packed.Descriptor

		pulledTarget := newFileTarget(t)
		pulled, err := orasbinding.NewPuller(layout).Pull(ctx, test.reference, pulledTarget)
		if err != nil {
			t.Fatal(err)
		}
		assertSameDescriptor(t, pulled, packed.Descriptor)
		report, err := (orasbinding.LayoutValidator{}).Validate(ctx, pulledTarget, pulled)
		if err != nil {
			t.Fatal(err)
		}
		if report.Name != suite.Name {
			t.Fatalf("pulled suite = %q, want %q", report.Name, suite.Name)
		}
	}
	if content.Equal(roots[tests[0].reference], roots[tests[1].reference]) {
		t.Fatal("distinct AgentSuites produced the same root descriptor")
	}

	for reference, want := range roots {
		resolved, err := layout.Resolve(ctx, reference)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", reference, err)
		}
		assertSameDescriptor(t, resolved, want)
	}

	indexBytes, err := os.ReadFile(filepath.Join(layoutRoot, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	indexRoots := map[string]ocispec.Descriptor{}
	for _, descriptor := range index.Manifests {
		reference := descriptor.Annotations[ocispec.AnnotationRefName]
		if reference != "" {
			indexRoots[reference] = descriptor
		}
	}
	if len(indexRoots) != len(roots) {
		t.Fatalf("index references = %v, want %v", indexRoots, roots)
	}
	for reference, want := range roots {
		got, ok := indexRoots[reference]
		if !ok {
			t.Fatalf("index does not contain repository reference %q", reference)
		}
		assertSameDescriptor(t, got, want)
	}
}

func newAgentSuiteSource(
	t *testing.T,
	name string,
) (agentsuite.Suite, *memory.Store, ocispec.Descriptor) {
	t.Helper()
	fixtureRoot := filepath.Join("..", "testdata", "minimal")
	rawSuite, err := os.ReadFile(filepath.Join(fixtureRoot, "agentsuite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture agentsuite.Suite
	if err := json.Unmarshal(rawSuite, &fixture); err != nil {
		t.Fatal(err)
	}
	suite := agentsuite.Suite{
		SchemaVersion:    agentsuite.SpecVersion,
		MediaType:        agentsuite.MediaTypeSuite,
		Name:             name,
		Agents:           fixture.Agents,
		ToolCatalog:      fixture.ToolCatalog,
		ToolCompositions: fixture.ToolCompositions,
		Compositions:     fixture.Compositions,
		BuildProfiles:    fixture.BuildProfiles,
		Capabilities:     fixture.Capabilities,
		Extensions:       fixture.Extensions,
	}

	contentRoot := t.TempDir()
	if err := os.CopyFS(contentRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatal(err)
	}
	suiteData, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, "agentsuite.json"), suiteData, 0o644); err != nil {
		t.Fatal(err)
	}

	data := buildContentLayer(t, contentRoot)
	descriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, data)
	store := memory.New()
	if err := store.Push(context.Background(), descriptor, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return suite, store, descriptor
}

func newFileTarget(t *testing.T) *oci.Store {
	t.Helper()
	target, err := oci.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func assertSameDescriptor(t *testing.T, got, want ocispec.Descriptor) {
	t.Helper()
	if !content.Equal(got, want) {
		t.Fatalf("descriptor = %+v, want %+v", got, want)
	}
}
