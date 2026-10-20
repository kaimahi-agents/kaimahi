package suiteview_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
	godigest "github.com/opencontainers/go-digest"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "agentsuite", name)
}

func pushFixture(t *testing.T, name, layout, reference string) suiteoras.PushResult {
	t.Helper()
	result, err := suiteoras.Push(t.Context(), fixture(name), layout, reference)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func refusalCode(t *testing.T, err error) suiteview.RefusalCode {
	t.Helper()
	var refusal *suiteview.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is not a typed refusal: %v", err)
	}
	return refusal.Code
}

func TestLoadLayoutAndArchiveReturnOwnedExactRecipe(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	pushed := pushFixture(t, "minimal", layout, "minimal:v1")
	selection := suiteview.Selection{ManifestDigest: pushed.Descriptor.Digest.String(), MemberID: "writer"}
	fromLayout, err := suiteview.LoadLayout(t.Context(), layout, selection)
	if err != nil {
		t.Fatal(err)
	}
	archive := archiveLayout(t, layout)
	fromArchive, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), selection)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromLayout, fromArchive) {
		t.Fatalf("layout/archive projections differ:\n%+v\n%+v", fromLayout, fromArchive)
	}
	if fromLayout.Artifact.Digest != pushed.Descriptor.Digest.String() || fromLayout.Artifact.MediaType != pushed.Descriptor.MediaType || fromLayout.Artifact.Size != pushed.Descriptor.Size {
		t.Fatal("artifact identity changed", fromLayout.Artifact)
	}
	if fromLayout.Suite.Name != "minimal" || !agentsuite.IsValidDigest(fromLayout.Suite.GraphDigest) || fromLayout.Member.ID != "writer" || fromLayout.Member.Descriptor.Digest != "sha256:2fe4605b0a28a5cebebb3e351c7288cb9a1dee03205a5f380afbad2d735cc274" || fromLayout.Member.Descriptor.MediaType != agentsuite.MediaTypeAgent {
		t.Fatal("suite/member identity changed", fromLayout)
	}
	if string(fromLayout.Instructions) != "Write clearly.\n" || fromLayout.InstructionsDescriptor.Digest != "sha256:516ff360a8ebb5e36e87f0d401a3cc7436ad2ad1059aae64670aff1078171df8" || fromLayout.Model.Model != "example-model" {
		t.Fatal("member recipe changed", fromLayout)
	}
	implicit, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{MemberID: "writer"})
	if err != nil || !reflect.DeepEqual(implicit, fromLayout) {
		t.Fatal("single root was not selected deterministically", err)
	}
	fromLayout.Instructions[0] = 'X'
	fromLayout.Model.SecretRefs = append(fromLayout.Model.SecretRefs, "changed")
	fresh, err := suiteview.LoadLayout(t.Context(), layout, selection)
	if err != nil || string(fresh.Instructions) != "Write clearly.\n" || len(fresh.Model.SecretRefs) != 0 {
		t.Fatal("caller changed retained recipe", err)
	}
}

func TestProjectMemberReturnsOwnedExactRecipe(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	copyTree(t, fixture("minimal"), source)
	annotateDescriptors(t, source)
	files := readFiles(t, source)
	rootBytes := files["agentsuite.json"]
	graphDigest, err := agentsuite.Digest(rootBytes)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := suiteview.ProjectMember(t.Context(), files, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Artifact != (suiteview.ArtifactIdentity{}) || recipe.Suite.Name != "minimal" || recipe.Suite.GraphDigest != graphDigest || recipe.Member.ID != "writer" || string(recipe.Instructions) != "Write clearly.\n" || recipe.Model.Model != "example-model" {
		t.Fatal("projected recipe changed identity or behavior", recipe)
	}
	if recipe.Member.Descriptor.Annotations["member-note"] != "retained" || recipe.InstructionsDescriptor.Annotations["instruction-note"] != "retained" {
		t.Fatal("projected recipe dropped descriptor annotations", recipe)
	}
	recipe.Instructions[0] = 'X'
	recipe.Member.Descriptor.Annotations["member-note"] = "changed"
	recipe.InstructionsDescriptor.Annotations["instruction-note"] = "changed"
	fresh, err := suiteview.ProjectMember(t.Context(), files, "writer")
	if err != nil || string(fresh.Instructions) != "Write clearly.\n" || fresh.Member.Descriptor.Annotations["member-note"] != "retained" || fresh.InstructionsDescriptor.Annotations["instruction-note"] != "retained" {
		t.Fatal("projected recipe aliases retained input", err, fresh)
	}
	if _, err := suiteview.ProjectMember(t.Context(), files, "absent-canary"); refusalCode(t, err) != suiteview.RefusalMemberNotFound || strings.Contains(err.Error(), "canary") {
		t.Fatalf("unsafe member refusal: %v", err)
	}
	files["agents/writer.json"][0] = 'X'
	if _, err := suiteview.ProjectMember(t.Context(), files, "writer"); refusalCode(t, err) != suiteview.RefusalMember {
		t.Fatalf("invalid member accepted: %v", err)
	}
	files["agentsuite.json"] = make([]byte, (1<<20)+1)
	if _, err := suiteview.ProjectMember(t.Context(), files, "writer"); refusalCode(t, err) != suiteview.RefusalLimit {
		t.Fatalf("unbounded projection input accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := suiteview.ProjectMember(ctx, files, "writer"); refusalCode(t, err) != suiteview.RefusalCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("projection cancellation identity lost: %v", err)
	}
}

func TestLoadLayoutRequiresUnambiguousRootAndExactMember(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	minimal := pushFixture(t, "minimal", layout, "minimal:v1")
	team := pushFixture(t, "coordinator-workers", layout, "team:v1")
	if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalAmbiguousArtifact {
		t.Fatalf("multiple roots were guessed: %v", err)
	}
	writer, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{ManifestDigest: team.Descriptor.Digest.String(), MemberID: "writer"})
	if err != nil || writer.Suite.Name != "coordinator-workers" || writer.Member.ID != "writer" || !strings.Contains(writer.Description, "Writes content") {
		t.Fatal("non-first member was not selected exactly", err, writer)
	}
	archive := archiveLayout(t, layout)
	if _, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalAmbiguousArtifact {
		t.Fatalf("archive roots were guessed: %v", err)
	}
	archiveWriter, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), suiteview.Selection{ManifestDigest: team.Descriptor.Digest.String(), MemberID: "writer"})
	if err != nil || !reflect.DeepEqual(archiveWriter, writer) {
		t.Fatal("archive exact-root selection diverged", err)
	}
	missing := "sha256:" + strings.Repeat("a", 64)
	if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{ManifestDigest: missing, MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalArtifactNotFound {
		t.Fatalf("missing root refusal changed: %v", err)
	}
	if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{ManifestDigest: minimal.Descriptor.Digest.String(), MemberID: "absent-canary"}); refusalCode(t, err) != suiteview.RefusalMemberNotFound || strings.Contains(err.Error(), "canary") {
		t.Fatalf("unsafe member refusal: %v", err)
	}
	for _, selection := range []suiteview.Selection{{}, {MemberID: "writer", ManifestDigest: "minimal:v1"}} {
		if _, err := suiteview.LoadLayout(t.Context(), layout, selection); refusalCode(t, err) != suiteview.RefusalSelection {
			t.Fatalf("invalid selection accepted: %v", err)
		}
	}
}

func TestLoadPreservesCompleteModelRequirement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model agentsuite.ModelRequirement
	}{
		{"exact", agentsuite.ModelRequirement{Protocol: "openai-compatible", Model: "example-model", EndpointEnv: "MODEL_BASE_URL", SecretRefs: []string{"model-api-key"}}},
		{"capabilities", agentsuite.ModelRequirement{Protocol: "openai-compatible", Capabilities: &agentsuite.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 4096, OutputTokens: 1024, Streaming: true, ToolCalling: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			copyTree(t, fixture("minimal"), source)
			rewriteModel(t, source, tc.model)
			layout := filepath.Join(t.TempDir(), "layout")
			pushed, err := suiteoras.Push(t.Context(), source, layout, "model:v1")
			if err != nil {
				t.Fatal(err)
			}
			selection := suiteview.Selection{ManifestDigest: pushed.Descriptor.Digest.String(), MemberID: "writer"}
			got, err := suiteview.LoadLayout(t.Context(), layout, selection)
			if err != nil || !reflect.DeepEqual(got.Model, tc.model) {
				t.Fatal("model requirement was narrowed", err, got.Model)
			}
			if got.Model.Capabilities != nil {
				got.Model.Capabilities.ContextTokens = 1
			}
			if len(got.Model.SecretRefs) != 0 {
				got.Model.SecretRefs[0] = "changed"
			}
			fresh, err := suiteview.LoadLayout(t.Context(), layout, selection)
			if err != nil || !reflect.DeepEqual(fresh.Model, tc.model) {
				t.Fatal("nested model fields alias retained recipe", err, fresh.Model)
			}
		})
	}
}

func TestLoadPreservesDescriptorAnnotationsWithoutAliasing(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	copyTree(t, fixture("minimal"), source)
	annotateDescriptors(t, source)
	layout := filepath.Join(t.TempDir(), "layout")
	pushed, err := suiteoras.Push(t.Context(), source, layout, "annotated:v1")
	if err != nil {
		t.Fatal(err)
	}
	selection := suiteview.Selection{ManifestDigest: pushed.Descriptor.Digest.String(), MemberID: "writer"}
	recipe, err := suiteview.LoadLayout(t.Context(), layout, selection)
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Member.Descriptor.Annotations["member-note"] != "retained" || recipe.InstructionsDescriptor.Annotations["instruction-note"] != "retained" {
		t.Fatal("public recipe dropped descriptor annotations", recipe)
	}
	recipe.Member.Descriptor.Annotations["member-note"] = "changed"
	recipe.InstructionsDescriptor.Annotations["instruction-note"] = "changed"
	fresh, err := suiteview.LoadLayout(t.Context(), layout, selection)
	if err != nil || fresh.Member.Descriptor.Annotations["member-note"] != "retained" || fresh.InstructionsDescriptor.Annotations["instruction-note"] != "retained" {
		t.Fatal("public recipe descriptor annotations alias retained input", err, fresh)
	}
}

func TestLoadArchiveRefusesUnsafeBoundariesAndPreservesCancellation(t *testing.T) {
	regular := func(name, body string) archiveRecord {
		return archiveRecord{header: tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(body))}, body: []byte(body)}
	}
	for _, tc := range []struct {
		name    string
		records []archiveRecord
	}{
		{"traversal", []archiveRecord{regular("../index.json", "{}")}},
		{"duplicate", []archiveRecord{regular("index.json", "{}"), regular("index.json", "{}")}},
		{"dot-prefix traversal", []archiveRecord{regular("./../index.json", "{}")}},
		{"dot-prefix duplicate", []archiveRecord{regular("index.json", "{}"), regular("./index.json", "{}")}},
		{"symlink", []archiveRecord{{header: tar.Header{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "other"}}}},
		{"fifo", []archiveRecord{{header: tar.Header{Name: "index.json", Typeflag: tar.TypeFifo}}}},
		{"file parent before child", []archiveRecord{regular("blobs", "x"), regular("blobs/sha256/value", "x")}},
		{"file parent after child", []archiveRecord{regular("blobs/sha256/value", "x"), regular("blobs", "x")}},
		{"pax", []archiveRecord{{header: tar.Header{Name: "index.json", Mode: 0o644, Typeflag: tar.TypeReg, Size: 2, PAXRecords: map[string]string{"comment": "x"}}, body: []byte("{}")}}},
		{"gnu metadata", []archiveRecord{{header: tar.Header{Name: strings.Repeat("x", 2000), Mode: 0o644, Typeflag: tar.TypeReg, Size: 2, Format: tar.FormatGNU}, body: []byte("{}")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := makeArchive(t, tc.records)
			_, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(raw), int64(len(raw)), suiteview.Selection{MemberID: "writer"})
			if refusalCode(t, err) != suiteview.RefusalArtifact {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
	base := makeArchive(t, []archiveRecord{regular("index.json", "{}")})
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"truncated", base[:len(base)-512]},
		{"nonzero trailing", append(append([]byte{}, base...), append([]byte{1}, make([]byte, 511)...)...)},
		{"concatenated", append(append([]byte{}, base...), base...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(tc.raw), int64(len(tc.raw)), suiteview.Selection{MemberID: "writer"})
			if refusalCode(t, err) != suiteview.RefusalArtifact {
				t.Fatalf("malformed archive accepted: %v", err)
			}
		})
	}
	tooLarge := int64((64 << 20) + 1)
	if _, err := suiteview.LoadArchive(t.Context(), zeroReaderAt{}, tooLarge, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalLimit {
		t.Fatalf("archive size budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := suiteview.LoadArchive(ctx, panicReaderAt{}, 0, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
	deadline, cancelDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if _, err := suiteview.LoadArchive(deadline, panicReaderAt{}, 0, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalCanceled || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline identity lost: %v", err)
	}
	if _, err := suiteview.LoadArchive(t.Context(), nil, 0, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalArtifact {
		t.Fatalf("nil archive accepted: %v", err)
	}
	if _, err := suiteview.LoadArchive(t.Context(), bytes.NewReader([]byte("short")), 1024, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalArtifact {
		t.Fatalf("short archive accepted: %v", err)
	}
	for _, tc := range []struct {
		records []archiveRecord
		want    suiteview.RefusalCode
	}{
		{[]archiveRecord{regular("oci-layout", strings.Repeat(" ", 1025))}, suiteview.RefusalLimit},
		{[]archiveRecord{regular("oci-layout", `{"imageLayoutVersion":"1.0.0"}`), regular("index.json", strings.Repeat(" ", (1<<20)+1))}, suiteview.RefusalLimit},
	} {
		raw := makeArchive(t, tc.records)
		if _, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(raw), int64(len(raw)), suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != tc.want {
			t.Fatalf("oversized metadata accepted: %v", err)
		}
	}
}

func TestLoadArchiveAcceptsSingleDotPrefix(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	pushed := pushFixture(t, "minimal", layout, "minimal:v1")
	records := []archiveRecord{
		{header: tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: tar.Header{Name: "./blobs/", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: tar.Header{Name: "./blobs/sha256/", Mode: 0o755, Typeflag: tar.TypeDir}},
	}
	for _, record := range layoutRecords(t, layout) {
		record.header.Name = "./" + record.header.Name
		records = append(records, record)
	}
	selection := suiteview.Selection{ManifestDigest: pushed.Descriptor.Digest.String(), MemberID: "writer"}
	want, err := suiteview.LoadLayout(t.Context(), layout, selection)
	if err != nil {
		t.Fatal(err)
	}
	raw := makeArchive(t, records)
	got, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(raw), int64(len(raw)), selection)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("single-dot-prefixed archive changed recipe", err, got)
	}
}

func TestLoadRejectsOCIEnvelopeParserDifferentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"unknown index field", func(t *testing.T, layout string) {
			mutateJSONFile(t, filepath.Join(layout, "index.json"), func(document map[string]any) {
				document["artifactType"] = agentsuite.MediaTypeArtifact
			})
		}},
		{"null root urls", func(t *testing.T, layout string) {
			mutateJSONFile(t, filepath.Join(layout, "index.json"), func(document map[string]any) {
				document["manifests"].([]any)[0].(map[string]any)["urls"] = nil
			})
		}},
		{"null root data", func(t *testing.T, layout string) {
			mutateJSONFile(t, filepath.Join(layout, "index.json"), func(document map[string]any) {
				document["manifests"].([]any)[0].(map[string]any)["data"] = nil
			})
		}},
		{"null child urls", func(t *testing.T, layout string) {
			rewriteSelectedManifest(t, layout, func(manifest map[string]any) {
				manifest["config"].(map[string]any)["urls"] = nil
			})
		}},
		{"null child data", func(t *testing.T, layout string) {
			rewriteSelectedManifest(t, layout, func(manifest map[string]any) {
				manifest["layers"].([]any)[0].(map[string]any)["data"] = nil
			})
		}},
		{"unknown manifest field", func(t *testing.T, layout string) {
			rewriteSelectedManifest(t, layout, func(manifest map[string]any) {
				manifest["unknown"] = true
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			pushFixture(t, "minimal", layout, "minimal:v1")
			tc.mutate(t, layout)
			if _, err := agentsuite.ValidatePath(layout); err == nil {
				t.Fatal("fixture does not demonstrate an upstream validation refusal")
			}
			if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalArtifact {
				t.Fatalf("malformed envelope accepted: %v", err)
			}
		})
	}
}

func TestLoadArchiveRequiresPhysicalTerminator(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	pushFixture(t, "minimal", layout, "minimal:v1")
	records := layoutRecords(t, layout)
	records = append(records, archiveRecord{header: tar.Header{Name: "unused-zero-payload", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1024}, body: make([]byte, 1024)})
	raw := makeArchiveWithoutTerminator(t, records)
	if len(raw) < 1024 || len(bytes.Trim(raw[len(raw)-1024:], "\x00")) != 0 {
		t.Fatal("fixture does not end with an all-zero file payload")
	}
	if _, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(raw), int64(len(raw)), suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalArtifact {
		t.Fatalf("archive without terminator accepted: %v", err)
	}
}

func TestLoadAcceptsConformingSupplementalLink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	copyTree(t, fixture("minimal"), source)
	if err := os.MkdirAll(filepath.Join(source, "metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../agentsuite.json", filepath.Join(source, "metadata", "suite-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	layout := filepath.Join(t.TempDir(), "layout")
	pushed, err := suiteoras.Push(t.Context(), source, layout, "linked:v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{ManifestDigest: pushed.Descriptor.Digest.String(), MemberID: "writer"}); err != nil {
		t.Fatalf("conforming supplemental link refused: %v", err)
	}
}

func TestLoadReportsConsumerContentLimit(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	copyTree(t, fixture("minimal"), source)
	if err := os.WriteFile(filepath.Join(source, "supplement.bin"), bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "layout")
	if _, err := suiteoras.Push(t.Context(), source, layout, "large:v1"); err != nil {
		t.Fatal("upstream validator did not accept the consumer-limit fixture", err)
	}
	if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{MemberID: "writer"}); refusalCode(t, err) != suiteview.RefusalLimit {
		t.Fatalf("consumer content limit was not typed: %v", err)
	}
}

func TestRefusalsDoNotEchoUntrustedPaths(t *testing.T) {
	const canary = "private-canary-path"
	_, err := suiteview.LoadLayout(t.Context(), filepath.Join(t.TempDir(), canary), suiteview.Selection{MemberID: "writer"})
	if refusalCode(t, err) != suiteview.RefusalArtifact || strings.Contains(err.Error(), canary) {
		t.Fatalf("path escaped refusal boundary: %v", err)
	}
}

type zeroReaderAt struct{}

func (zeroReaderAt) ReadAt(p []byte, _ int64) (int, error) {
	clear(p)
	return len(p), nil
}

type panicReaderAt struct{}

func (panicReaderAt) ReadAt([]byte, int64) (int, error) {
	panic("archive source read after cancellation")
}

type archiveRecord struct {
	header tar.Header
	body   []byte
}

func makeArchive(t *testing.T, records []archiveRecord) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, record := range records {
		header := record.header
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(record.body) != 0 {
			if _, err := writer.Write(record.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func makeArchiveWithoutTerminator(t *testing.T, records []archiveRecord) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, record := range records {
		header := record.header
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(record.body) != 0 {
			if _, err := writer.Write(record.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func archiveLayout(t *testing.T, layout string) []byte {
	t.Helper()
	return makeArchive(t, layoutRecords(t, layout))
}

func layoutRecords(t *testing.T, layout string) []archiveRecord {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(layout, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(layout, name)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	records := make([]archiveRecord, 0, len(names))
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(layout, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, archiveRecord{header: tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(body))}, body: body})
	}
	return records
}

func mutateJSONFile(t *testing.T, name string, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteSelectedManifest(t *testing.T, layout string, mutate func(map[string]any)) {
	t.Helper()
	indexPath := filepath.Join(layout, "index.json")
	indexRaw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]any
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		t.Fatal(err)
	}
	descriptor := index["manifests"].([]any)[0].(map[string]any)
	digest := descriptor["digest"].(string)
	manifestPath := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	mutate(manifest)
	manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	newDigest := godigest.FromBytes(manifestRaw)
	newPath := filepath.Join(layout, "blobs", "sha256", newDigest.Encoded())
	if err := os.WriteFile(newPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor["digest"] = newDigest.String()
	descriptor["size"] = len(manifestRaw)
	indexRaw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, indexRaw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

func readFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)], err = os.ReadFile(name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func rewriteModel(t *testing.T, source string, model agentsuite.ModelRequirement) {
	t.Helper()
	memberPath := filepath.Join(source, "agents", "writer.json")
	memberBytes, err := os.ReadFile(memberPath)
	if err != nil {
		t.Fatal(err)
	}
	var member agentsuite.Agent
	if agentsuite.Unmarshal(memberBytes, &member) != nil {
		t.Fatal("decode member")
	}
	member.Model = model
	memberBytes, err = agentsuite.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memberPath, memberBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(source, "agentsuite.json")
	rootBytes, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	var root agentsuite.Suite
	if agentsuite.Unmarshal(rootBytes, &root) != nil {
		t.Fatal("decode root")
	}
	root.Agents[0].Descriptor.Size = int64(len(memberBytes))
	root.Agents[0].Descriptor.Digest, err = agentsuite.Digest(memberBytes)
	if err != nil {
		t.Fatal(err)
	}
	rootBytes, err = agentsuite.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootPath, rootBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

func annotateDescriptors(t *testing.T, source string) {
	t.Helper()
	memberPath := filepath.Join(source, "agents", "writer.json")
	memberBytes, err := os.ReadFile(memberPath)
	if err != nil {
		t.Fatal(err)
	}
	var member agentsuite.Agent
	if agentsuite.Unmarshal(memberBytes, &member) != nil {
		t.Fatal("decode member")
	}
	member.Instructions.Descriptor.Annotations = map[string]string{"instruction-note": "retained"}
	memberBytes, err = agentsuite.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memberPath, memberBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(source, "agentsuite.json")
	rootBytes, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	var root agentsuite.Suite
	if agentsuite.Unmarshal(rootBytes, &root) != nil {
		t.Fatal("decode root")
	}
	root.Agents[0].Descriptor.Size = int64(len(memberBytes))
	root.Agents[0].Descriptor.Digest, err = agentsuite.Digest(memberBytes)
	if err != nil {
		t.Fatal(err)
	}
	root.Agents[0].Descriptor.Annotations = map[string]string{"member-note": "retained"}
	rootBytes, err = agentsuite.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootPath, rootBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

var _ io.ReaderAt = zeroReaderAt{}
