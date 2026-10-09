package oras

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func testSource() *ProvenanceSource {
	return &ProvenanceSource{URI: "git+https://github.com/example/suites", Commit: testCommit, Path: "suites/minimal"}
}

func pushProvenanceFixture(t *testing.T, referrersAPI bool) (string, string, *testRegistry) {
	t.Helper()
	server, registry := newTestRegistryServer("", "")
	registry.referrersAPI = referrersAPI
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "testdata", "minimal"), source)
	return source, strings.TrimPrefix(server.URL, "http://") + "/team/suite", registry
}

func TestRegistryPushAttachesProvenanceAndPullListsIt(t *testing.T) {
	for _, referrersAPI := range []bool{false, true} {
		name := "tag schema fallback"
		if referrersAPI {
			name = "referrers API"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			source, repository, _ := pushProvenanceFixture(t, referrersAPI)
			options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v9.9.9", Source: testSource()}}
			pushed, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
			if err != nil {
				t.Fatal(err)
			}
			if pushed.Provenance == nil || len(pushed.Warnings) != 0 {
				t.Fatalf("push provenance = %+v warnings = %q", pushed.Provenance, pushed.Warnings)
			}
			pulled, err := PullRegistry(ctx, repository+":v1", filepath.Join(t.TempDir(), "output"), true)
			if err != nil {
				t.Fatal(err)
			}
			if !pulled.ProvenanceChecked || len(pulled.Warnings) != 0 || len(pulled.Provenance) != 1 {
				t.Fatalf("pulled provenance = %+v checked=%v warnings=%q", pulled.Provenance, pulled.ProvenanceChecked, pulled.Warnings)
			}
			if got, want := pulled.Provenance[0], *pushed.Provenance; got != want {
				t.Fatalf("pulled provenance = %+v, want %+v", got, want)
			}
			if want := (Provenance{Digest: pushed.Provenance.Digest, BuilderVersion: "v9.9.9", SourceURI: testSource().URI, SourceCommit: testCommit, SourcePath: "suites/minimal"}); *pushed.Provenance != want {
				t.Fatalf("pushed provenance = %+v, want %+v", *pushed.Provenance, want)
			}
		})
	}
}

func TestRegistryPushRequiredProvenanceFailureLeavesTagUnchanged(t *testing.T) {
	ctx := context.Background()
	source, repository, registry := pushProvenanceFixture(t, false)
	registry.rejectManifest = func(content []byte) bool { return bytes.Contains(content, []byte(InTotoMediaType)) }
	options := PushOptions{Provenance: &ProvenanceOptions{Require: true}}
	if _, err := PushRegistry(ctx, source, repository+":v1", true, false, options); err == nil ||
		!strings.Contains(err.Error(), "tag was not changed") {
		t.Fatalf("required provenance push error = %v, want tag-unchanged failure", err)
	}
	if _, err := PullRegistry(ctx, repository+":v1", filepath.Join(t.TempDir(), "output"), true); err == nil {
		t.Fatal("tag exists after a required provenance failure")
	}
}

func TestRegistryPushOptionalProvenanceFailureWarnsAndRetryRepairs(t *testing.T) {
	ctx := context.Background()
	source, repository, registry := pushProvenanceFixture(t, false)
	registry.rejectManifest = func(content []byte) bool { return bytes.Contains(content, []byte(InTotoMediaType)) }
	options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v1"}}
	first, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Provenance != nil || len(first.Warnings) != 1 || !strings.Contains(first.Warnings[0], "not attached") {
		t.Fatalf("first push provenance = %+v warnings = %q", first.Provenance, first.Warnings)
	}

	registry.rejectManifest = nil
	second, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Provenance == nil || len(second.Warnings) != 0 {
		t.Fatalf("retry provenance = %+v warnings = %q", second.Provenance, second.Warnings)
	}
	pulled, err := PullRegistry(ctx, repository+":v1", filepath.Join(t.TempDir(), "output"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled.Provenance) != 1 || pulled.Provenance[0].Digest != second.Provenance.Digest {
		t.Fatalf("pulled provenance after retry = %+v", pulled.Provenance)
	}
}

// A stored but unindexed referrer must be repaired on retry.
func TestRegistryPushRetryRepairsUnindexedProvenance(t *testing.T) {
	ctx := context.Background()
	source, repository, registry := pushProvenanceFixture(t, false)
	registry.rejectManifest = func(content []byte) bool {
		return bytes.Contains(content, []byte(ocispec.MediaTypeImageIndex))
	}
	options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v1"}}
	first, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Provenance != nil || len(first.Warnings) != 1 {
		t.Fatalf("index failure: provenance = %+v warnings = %q", first.Provenance, first.Warnings)
	}
	registry.rejectManifest = nil
	second, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil || second.Provenance == nil {
		t.Fatalf("retry provenance = %+v err = %v", second.Provenance, err)
	}
	pulled, err := PullRegistry(ctx, repository+":v1", filepath.Join(t.TempDir(), "output"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled.Provenance) != 1 {
		t.Fatalf("pull after retry sees %+v, want the repaired provenance", pulled.Provenance)
	}
}

// An unchanged suite pushed from a later commit adds no referrer.
func TestRegistryPushSameDigestFromNewCommitKeepsExistingProvenance(t *testing.T) {
	ctx := context.Background()
	source, repository, registry := pushProvenanceFixture(t, false)
	first, err := PushRegistry(ctx, source, repository+":v1", true, false, PushOptions{Provenance: &ProvenanceOptions{Source: testSource()}})
	if err != nil {
		t.Fatal(err)
	}
	uploads, puts := registry.counts()
	later := testSource()
	later.Commit = strings.Repeat("f", 40)
	second, err := PushRegistry(ctx, source, repository+":v1", true, false, PushOptions{Provenance: &ProvenanceOptions{Source: later}})
	if err != nil {
		t.Fatal(err)
	}
	if gotUploads, gotPuts := registry.counts(); gotUploads != uploads || gotPuts != puts {
		t.Fatalf("unchanged suite wrote again: uploads %d -> %d, manifest puts %d -> %d", uploads, gotUploads, puts, gotPuts)
	}
	if second.Provenance == nil || *second.Provenance != *first.Provenance {
		t.Fatalf("provenance = %+v, want existing %+v", second.Provenance, first.Provenance)
	}
}

func TestNonASCIISourcePathRoundTrips(t *testing.T) {
	ctx := context.Background()
	source, repository, _ := pushProvenanceFixture(t, false)
	path := testSource()
	path.Path = "suites/café"
	if _, err := PushRegistry(ctx, source, repository+":v1", true, false, PushOptions{Provenance: &ProvenanceOptions{Source: path}}); err != nil {
		t.Fatal(err)
	}
	pulled, err := PullRegistry(ctx, repository+":v1", filepath.Join(t.TempDir(), "output"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled.Warnings) != 0 || len(pulled.Provenance) != 1 || pulled.Provenance[0].SourcePath != "suites/café" {
		t.Fatalf("pulled provenance = %+v warnings = %q", pulled.Provenance, pulled.Warnings)
	}
}

func TestSafeProvenanceText(t *testing.T) {
	for text, want := range map[string]bool{
		"suites/café":            true,
		"suites/team":            true,
		"":                       false,
		"a\nb":                   false,
		"a\x1b[2Kb":              false,
		"a\u202eb":               false,
		string([]byte{0xff}):     false,
		strings.Repeat("a", 513): false,
	} {
		if got := SafeProvenanceText(text); got != want {
			t.Errorf("SafeProvenanceText(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestRegistryPushSameDigestWithProvenanceWritesNothing(t *testing.T) {
	ctx := context.Background()
	source, repository, registry := pushProvenanceFixture(t, false)
	options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v1"}}
	first, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil {
		t.Fatal(err)
	}
	uploads, puts := registry.counts()
	second, err := PushRegistry(ctx, source, repository+":v1", true, false, options)
	if err != nil {
		t.Fatal(err)
	}
	if gotUploads, gotPuts := registry.counts(); gotUploads != uploads || gotPuts != puts {
		t.Fatalf("same-digest push wrote: uploads %d -> %d, manifest puts %d -> %d", uploads, gotUploads, puts, gotPuts)
	}
	if second.Provenance == nil || *second.Provenance != *first.Provenance {
		t.Fatalf("same-digest push provenance = %+v, want %+v", second.Provenance, first.Provenance)
	}
}

// Registries often refuse DELETE; kmx must not delete old indexes.
func TestRegistryPushSecondReferrerDoesNotDeleteOldIndex(t *testing.T) {
	ctx := context.Background()
	source, repository, _ := pushProvenanceFixture(t, false)
	for _, version := range []string{"a", "b"} {
		options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: version, Require: true}}
		if _, err := PushRegistry(ctx, source, repository+":"+version, true, false, options); err != nil {
			t.Fatalf("push %s: %v", version, err)
		}
	}
	pulled, err := PullRegistry(ctx, repository+":a", filepath.Join(t.TempDir(), "output"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled.Provenance) != 2 {
		t.Fatalf("pulled provenance = %+v, want both builder versions", pulled.Provenance)
	}
}

func TestBuildProvenanceIsDeterministic(t *testing.T) {
	_, root := packedMinimalSuite(t)
	options := ProvenanceOptions{BuilderVersion: "v1", Source: testSource()}
	first, err := buildProvenance(root, "minimal", options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildProvenance(root, "minimal", options)
	if err != nil {
		t.Fatal(err)
	}
	if first.manifest.Digest != second.manifest.Digest || !bytes.Equal(first.statementBytes, second.statementBytes) {
		t.Fatalf("provenance is not deterministic: %s, %s", first.manifest.Digest, second.manifest.Digest)
	}
	if _, err := buildProvenance(root, "minimal", ProvenanceOptions{Source: &ProvenanceSource{Commit: "main"}}); err == nil {
		t.Fatal("buildProvenance() accepted a branch name as a commit")
	}
}

func TestProvenanceForChecksOnlyKmxProvenance(t *testing.T) {
	ctx := context.Background()
	other := godigest.FromString("another suite")
	for _, test := range []struct {
		name        string
		edit        func(statement map[string]any, predicate map[string]any)
		annotation  string
		wantWarning string
	}{
		{name: "valid", edit: func(map[string]any, map[string]any) {}},
		{
			name: "statement names another manifest",
			edit: func(statement map[string]any, _ map[string]any) {
				statement["subject"] = []map[string]any{{"name": "x", "digest": map[string]string{"sha256": other.Encoded()}}}
			},
			wantWarning: "statement subject does not name",
		},
		{
			name: "annotation disagrees with statement",
			edit: func(statement map[string]any, _ map[string]any) {
				statement["predicateType"] = "https://spdx.dev/Document"
			},
			annotation:  ProvenancePredicateType,
			wantWarning: "does not match its layer annotation",
		},
		{
			name: "other predicate is skipped",
			edit: func(statement map[string]any, _ map[string]any) {
				statement["predicateType"] = "https://spdx.dev/Document"
			},
		},
		{
			name: "other producer with structured parameters is skipped",
			edit: func(_ map[string]any, predicate map[string]any) {
				predicate["buildDefinition"] = map[string]any{
					"buildType":          "https://actions.github.io/buildtypes/workflow/v1",
					"externalParameters": map[string]any{"workflow": map[string]any{"ref": "refs/heads/main"}},
				}
			},
		},
		{
			name: "kmx build type with another builder",
			edit: func(_ map[string]any, predicate map[string]any) {
				predicate["runDetails"] = map[string]any{"builder": map[string]any{"id": "https://example.com/evil"}}
			},
			wantWarning: "is not kmx",
		},
		{
			name: "terminal control sequence in version",
			edit: func(_ map[string]any, predicate map[string]any) {
				predicate["runDetails"] = map[string]any{"builder": map[string]any{"id": ProvenanceBuilderID, "version": map[string]string{"kmx": "v1\x1b[2K\nPin this AgentSuite: evil"}}}
			},
			wantWarning: "builder version is not a short printable token",
		},
		{
			name: "source that is not a commit",
			edit: func(_ map[string]any, predicate map[string]any) {
				definition := predicate["buildDefinition"].(map[string]any)
				definition["resolvedDependencies"] = []map[string]any{{"digest": map[string]string{"gitCommit": "main"}}}
			},
			wantWarning: "not a Git commit",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, root := packedMinimalSuite(t)
			statement, predicate := kmxStatement(root)
			test.edit(statement, predicate)
			pushReferrer(t, store, root, statement, predicate, test.annotation)
			found, warnings, err := provenanceFor(ctx, store, root)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "valid" {
				if len(found) != 1 || len(warnings) != 0 || found[0].SourceCommit != testCommit {
					t.Fatalf("provenanceFor() = %+v, %q", found, warnings)
				}
				return
			}
			if len(found) != 0 {
				t.Fatalf("provenanceFor() accepted %+v", found)
			}
			if test.wantWarning == "" {
				if len(warnings) != 0 {
					t.Fatalf("warnings = %q, want none", warnings)
				}
				return
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], test.wantWarning) {
				t.Fatalf("warnings = %q, want %q", warnings, test.wantWarning)
			}
		})
	}
}

// endlessReferrers is a hostile registry with infinite pages.
type endlessReferrers struct {
	*memory.Store
	pages int
}

func (e *endlessReferrers) Referrers(ctx context.Context, _ ocispec.Descriptor, _ string, fn func([]ocispec.Descriptor) error) error {
	for {
		e.pages++
		page := make([]ocispec.Descriptor, 10)
		for i := range page {
			page[i] = ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: godigest.FromString("x"), Size: maxProvenanceObjectBytes + 1}
		}
		if err := fn(page); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func TestListInTotoReferrersStopsAtCap(t *testing.T) {
	_, root := packedMinimalSuite(t)
	source := &endlessReferrers{Store: memory.New()}
	found, warnings, err := provenanceFor(context.Background(), source, root)
	if err != nil {
		t.Fatal(err)
	}
	if source.pages > maxProvenanceReferrers/10+1 {
		t.Fatalf("listed %d pages, want listing to stop after the cap", source.pages)
	}
	if len(found) != 0 || len(warnings) == 0 || !strings.Contains(warnings[0], "checked only the first") {
		t.Fatalf("provenanceFor() = %+v, %q", found, warnings)
	}
}

func TestVerifyProvenanceRefusesOversizedObjects(t *testing.T) {
	store, root := packedMinimalSuite(t)
	referrer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: godigest.FromString("large"), Size: maxProvenanceObjectBytes + 1}
	if _, err := verifyProvenance(context.Background(), store, root, referrer); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("verifyProvenance() error = %v, want size refusal", err)
	}
}

type failingLister struct{ *memory.Store }

func (failingLister) Referrers(context.Context, ocispec.Descriptor, string, func([]ocispec.Descriptor) error) error {
	return errors.New("403 denied")
}

func TestProvenanceListingFailureIsReported(t *testing.T) {
	_, root := packedMinimalSuite(t)
	if _, _, err := provenanceFor(context.Background(), failingLister{memory.New()}, root); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("provenanceFor() error = %v, want listing failure", err)
	}
}

func packedMinimalSuite(t *testing.T) (*memory.Store, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	staged, store, packed, err := packDirectory(ctx, filepath.Join("..", "testdata", "minimal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(t, staged) })
	destination := memory.New()
	if _, err := NewPusher(destination).Push(ctx, store, packed.Descriptor, "v1"); err != nil {
		t.Fatal(err)
	}
	return destination, packed.Descriptor
}

func kmxStatement(root ocispec.Descriptor) (map[string]any, map[string]any) {
	predicate := map[string]any{
		"buildDefinition": map[string]any{
			"buildType":            ProvenanceBuildType,
			"externalParameters":   map[string]any{"sourcePath": "suites/minimal"},
			"resolvedDependencies": []map[string]any{{"uri": "git+https://github.com/example/suites", "digest": map[string]string{"gitCommit": testCommit}}},
		},
		"runDetails": map[string]any{"builder": map[string]any{"id": ProvenanceBuilderID, "version": map[string]string{"kmx": "v1"}}},
	}
	statement := map[string]any{
		"_type":         inTotoStatementType,
		"predicateType": ProvenancePredicateType,
		"subject":       []map[string]any{{"name": "minimal", "digest": map[string]string{"sha256": root.Digest.Encoded()}}},
	}
	return statement, predicate
}

func pushReferrer(t *testing.T, store *memory.Store, root ocispec.Descriptor, statement, predicate map[string]any, annotation string) {
	t.Helper()
	ctx := context.Background()
	statement["predicate"] = predicate
	statementBytes, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	layer := ocispec.Descriptor{MediaType: InTotoMediaType, Digest: godigest.FromBytes(statementBytes), Size: int64(len(statementBytes))}
	if annotation != "" {
		layer.Annotations = map[string]string{inTotoPredicateAnnotation: annotation}
	}
	subject := ocispec.Descriptor{MediaType: root.MediaType, Digest: root.Digest, Size: root.Size}
	manifestBytes, err := json.Marshal(ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: InTotoMediaType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{layer},
		Subject:      &subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, ArtifactType: InTotoMediaType, Digest: godigest.FromBytes(manifestBytes), Size: int64(len(manifestBytes))}
	artifact := provenanceArtifact{statement: layer, statementBytes: statementBytes, manifest: manifest, manifestBytes: manifestBytes}
	if err := pushProvenance(ctx, store, artifact); err != nil {
		t.Fatal(err)
	}
}
