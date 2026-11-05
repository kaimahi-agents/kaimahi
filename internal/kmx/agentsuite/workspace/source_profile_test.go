package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
func TestSourceProfileTransportReaders(t *testing.T) {
	a, p := sourceArtifact(t)
	var outer bytes.Buffer
	tw := tar.NewWriter(&outer)
	err := filepath.WalkDir(p.Path, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(p.Path, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	fromArchive, err := suiteoras.ReadArtifactArchiveSelection(t.Context(), bytes.NewReader(outer.Bytes()), int64(outer.Len()), p.Descriptor.Digest.String())
	if err != nil || !reflect.DeepEqual(fromArchive.Files(), a.Files()) {
		t.Fatal("archive intake changed source graph", err)
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("unexpected registry mutation: %s", r.Method)
			w.WriteHeader(405)
			return
		}
		digest := filepath.Base(r.URL.Path)
		if !strings.HasPrefix(digest, "sha256:") {
			w.WriteHeader(404)
			return
		}
		data, err := os.ReadFile(filepath.Join(p.Path, "blobs/sha256", strings.TrimPrefix(digest, "sha256:")))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == "GET" {
			w.Write(data)
		}
	}))
	defer server.Close()
	registry, err := suiteoras.ReadArtifactRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/sources@"+p.Descriptor.Digest.String(), true)
	if err != nil || !reflect.DeepEqual(registry.Files(), a.Files()) {
		t.Fatal("registry intake changed graph", err)
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
