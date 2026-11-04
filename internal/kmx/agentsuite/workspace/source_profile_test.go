package workspace

import (
	"bytes"
	"context"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	godigest "github.com/opencontainers/go-digest"
)

func sourceSnapshot(t *testing.T) *snapshot {
	t.Helper()
	frozen, err := freezeMapped(mappedFixture())
	if err != nil {
		t.Fatal(err)
	}
	s, err := resolveMapped(context.Background(), frozen)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSourceProfileClosedRoundTrip(t *testing.T) {
	s := sourceSnapshot(t)
	if _, err := validateNative(s.files); err != nil {
		t.Fatal(err)
	}
	index, base, err := localartifact.SourceProfileBase(s.files)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.files) != len(base)+3 || index.MemberID != "greeter" || !bytes.Equal(s.files[index.Sources[0].Path], mappedFixture().Sources.Agent) || !bytes.Equal(s.files[index.Sources[1].Path], mappedFixture().Sources.Tests) {
		t.Fatal("originals changed")
	}
	for _, change := range []func(map[string][]byte){
		func(f map[string][]byte) { f[index.Sources[0].Path][0] = 'X' },
		func(f map[string][]byte) { f["metadata/kmx/source/extra.md"] = []byte("extra") },
		func(f map[string][]byte) { delete(f, index.Sources[1].Path) },
		func(f map[string][]byte) { f[localartifact.SourceIndexPath] = []byte(`{"schemaVersion":99}`) },
	} {
		files := map[string][]byte{}
		for k, v := range s.files {
			files[k] = bytes.Clone(v)
		}
		change(files)
		if _, err := validateNative(files); err == nil {
			t.Fatal("accepted substituted source profile")
		}
	}
}

func TestSourceFingerprintCompleteMap(t *testing.T) {
	s := sourceSnapshot(t)
	frozen, _ := freezeMapped(mappedFixture())
	same, err := resolveMapped(context.Background(), frozen)
	if err != nil || same.fingerprint != s.fingerprint {
		t.Fatal("not deterministic", err)
	}
	changed := mappedFixture()
	changed.Sources.Agent = append(changed.Sources.Agent, ' ')
	frozen, _ = freezeMapped(changed)
	other, err := resolveMapped(context.Background(), frozen)
	if err != nil || other.fingerprint == s.fingerprint {
		t.Fatal("formatting omitted from F", err)
	}
	if reflect.DeepEqual(other.files, s.files) {
		t.Fatal("source bytes omitted")
	}
	var frame map[string]any
	// Exact fixed domain/profile/version must be part of the serialized fingerprint frame.
	hashes := map[string]string{}
	for name, data := range s.files {
		hashes[name] = godigest.FromBytes(data).String()
	}
	frame = map[string]any{"domain": "kmx.mapped-input/v1", "packagerVersion": "kmx.orka-source-packager/v1", "packageProfile": "orka-source/v1", "files": hashes}
	data, err := suitecore.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	want, err := suitecore.Digest(data)
	if err != nil || want != s.fingerprint {
		t.Fatal("F does not cover complete regular file map", err)
	}
}

func sourceArtifact(t *testing.T) (*suiteoras.Artifact, *Prepared) {
	t.Helper()
	s := sourceSnapshot(t)
	root := t.TempDir()
	graph := filepath.Join(root, "graph")
	layout := filepath.Join(root, "layout")
	if err := writeFiles(graph, s.files); err != nil {
		t.Fatal(err)
	}
	result, err := suiteoras.Push(t.Context(), graph, layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := privateTree(layout, true); err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, result.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	return artifact, &Prepared{Path: layout, Fingerprint: s.fingerprint, Descriptor: result.Descriptor, Name: s.name}
}

func TestSourceProfilePreparedReadersRefuse(t *testing.T) {
	_, p := sourceArtifact(t)
	if err := Verify(t.Context(), p); err == nil || !strings.Contains(err.Error(), "source profile") {
		t.Fatalf("expected explicit source profile refusal: %v", err)
	}
	for _, load := range []func() error{
		func() error { _, err := Load(t.Context(), p); return err },
		func() error { _, err := LoadSelected(t.Context(), p, Selection{}); return err },
		func() error { _, err := LoadInventory(t.Context(), p); return err },
	} {
		if err := load(); err == nil {
			t.Fatal("source profile gained Prepared authority")
		}
	}
}

func TestSourceProfileLoadArtifact(t *testing.T) {
	a, _ := sourceArtifact(t)
	suite, err := LoadArtifact(t.Context(), a)
	if err != nil || suite.MemberID != "greeter" {
		t.Fatal(suite, err)
	}
	sources, err := ReadSources(t.Context(), a)
	if err != nil || !reflect.DeepEqual(sources, mappedFixture().Sources) {
		t.Fatal("source roundtrip", err)
	}
	sources.Agent[0] = 'X'
	again, err := ReadSources(t.Context(), a)
	if err != nil || again.Agent[0] == 'X' {
		t.Fatal("source alias", err)
	}
}
func TestSourceProfileArtifactSelection(t *testing.T) {
	a, _ := sourceArtifact(t)
	for _, s := range []Selection{{}, {Member: "greeter"}, {Member: "other"}, {RuntimeProfile: "orka"}, {Platform: "linux/amd64"}} {
		_, err := LoadArtifactSelected(t.Context(), a, s)
		good := s.RuntimeProfile == "" && s.Platform == "" && (s.Member == "" || s.Member == "greeter")
		if (err == nil) != good {
			t.Fatalf("%+v: %v", s, err)
		}
	}
}
func TestSourceProfileArtifactInventory(t *testing.T) {
	a, _ := sourceArtifact(t)
	if _, err := LoadArtifactInventory(t.Context(), a); err != nil {
		t.Fatal(err)
	}
}
func TestSourceProfileFlowReadersRefuse(t *testing.T) {
	a, _ := sourceArtifact(t)
	if _, err := LoadFlowArtifactInventory(t.Context(), a); err == nil {
		t.Fatal("source profile accepted as flow")
	}
}

func TestSourceProfileBaseAndPreparedBoundsStayLegacy(t *testing.T) {
	files := map[string][]byte{}
	for n := 0; n < 213; n++ {
		files[string(rune(0x100+n))] = []byte("x")
	}
	if nativeBounds(files) == nil {
		t.Fatal("213 base files accepted")
	}
	delete(files, string(rune(0x100+212)))
	if err := nativeBounds(files); err != nil {
		t.Fatal(err)
	}
}
