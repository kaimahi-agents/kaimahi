package agentsuite

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type imageArchiveFixture struct {
	nested                                 bool
	sbom, provenance                       bool
	binding, subject, predicate            string
	extraImage                             bool
	corrupt                                bool
	unsafe                                 bool
	duplicate                              bool
	badPlatform                            bool
	predicateBody                          any
	indexBody, manifestBody, statementBody json.RawMessage
}

func makeImageArchive(t *testing.T, f imageArchiveFixture) ([]byte, string, string, string) {
	t.Helper()
	blobs := map[string][]byte{}
	add := func(media string, body any) ociDescriptor {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		d := ociDescriptor{MediaType: media, Digest: digestBytes(data), Size: int64(len(data))}
		blobs["blobs/sha256/"+strings.TrimPrefix(d.Digest, "sha256:")] = data
		return d
	}
	config := add("application/vnd.oci.image.config.v1+json", map[string]string{"os": "linux", "architecture": "amd64"})
	var imageBody any = ociManifest{SchemaVersion: 2, MediaType: ociManifestMediaType, Config: config, Layers: []ociDescriptor{}}
	if f.manifestBody != nil {
		imageBody = f.manifestBody
	}
	image := add(ociManifestMediaType, imageBody)
	image.Platform = &Platform{OS: "linux", Architecture: "amd64"}
	if f.badPlatform {
		image.Platform.Architecture = "arm64"
	}
	manifests := []ociDescriptor{image}
	sbomDigest := ""
	if f.extraImage {
		extra := add(ociManifestMediaType, ociManifest{SchemaVersion: 2, Config: config, Layers: []ociDescriptor{}, Annotations: map[string]string{"extra": "image"}})
		extra.Platform = image.Platform
		manifests = append(manifests, extra)
	}
	var layers []ociDescriptor
	for _, p := range []struct {
		enabled   bool
		predicate string
	}{{f.sbom, "https://spdx.dev/Document"}, {f.provenance, "https://slsa.dev/provenance/v0.2"}} {
		if !p.enabled {
			continue
		}
		subject := strings.TrimPrefix(image.Digest, "sha256:")
		if f.subject != "" {
			subject = f.subject
		}
		predicate := p.predicate
		if f.predicate != "" {
			predicate = f.predicate
		}
		var predicateBody any = map[string]any{"fixture": true}
		if p.predicate == "https://spdx.dev/Document" && f.predicateBody != nil {
			predicateBody = f.predicateBody
		}
		var statementBody any = map[string]any{"_type": "https://in-toto.io/Statement/v0.1", "predicateType": predicate, "subject": []any{map[string]any{"name": "writer", "digest": map[string]string{"sha256": subject}}}, "predicate": predicateBody}
		if f.statementBody != nil {
			statementBody = f.statementBody
		}
		layer := add("application/vnd.in-toto+json", statementBody)
		layer.Annotations = map[string]string{"in-toto.io/predicate-type": p.predicate}
		layers = append(layers, layer)
		if p.predicate == "https://spdx.dev/Document" {
			sbomDigest = layer.Digest
		}
	}
	if len(layers) > 0 {
		att := add(ociManifestMediaType, ociManifest{SchemaVersion: 2, MediaType: ociManifestMediaType, Config: config, Layers: layers})
		att.Platform = &Platform{OS: "unknown", Architecture: "unknown"}
		binding := image.Digest
		if f.binding != "" {
			binding = f.binding
		}
		att.Annotations = map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": binding}
		manifests = append(manifests, att)
	}
	var indexBody any = ociIndex{SchemaVersion: 2, MediaType: ociIndexMediaType, Manifests: manifests}
	if f.indexBody != nil {
		indexBody = f.indexBody
	}
	rootDigest := ""
	if f.nested {
		nested := add(ociIndexMediaType, indexBody)
		rootDigest = nested.Digest
		indexBody = ociIndex{SchemaVersion: 2, MediaType: ociIndexMediaType, Manifests: []ociDescriptor{nested}}
	}
	indexBytes, err := json.Marshal(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	if rootDigest == "" {
		rootDigest = digestBytes(indexBytes)
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	write("index.json", indexBytes)
	for name, data := range blobs {
		if f.corrupt && strings.HasSuffix(name, strings.TrimPrefix(image.Digest, "sha256:")) {
			data = bytes.Repeat([]byte("x"), len(data))
		}
		write(name, data)
	}
	if f.unsafe {
		write("../escape", []byte("bad"))
	}
	if f.duplicate {
		write("index.json", indexBytes)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), image.Digest, rootDigest, sbomDigest
}

func TestInspectImageArchiveNestedAttestations(t *testing.T) {
	archive, image, index, sbom := makeImageArchive(t, imageArchiveFixture{nested: true, sbom: true, provenance: true})
	result, err := InspectImageArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), Platform{OS: "linux", Architecture: "amd64"}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest != image || result.IndexDigest != index || result.Digest == result.IndexDigest || result.SBOMDigest != sbom || len(result.Warnings) != 0 {
		t.Fatalf("result=%+v, want image=%s index=%s sbom=%s", result, image, index, sbom)
	}
}
func TestInspectImageArchiveAttestationFailures(t *testing.T) {
	for _, tc := range []struct {
		name                string
		fixture             imageArchiveFixture
		requested, required bool
		want                string
		warning             bool
	}{
		{name: "unattested opt out", fixture: imageArchiveFixture{}, requested: false},
		{name: "missing requested", fixture: imageArchiveFixture{nested: true}, requested: true, want: "SBOM and provenance", warning: true},
		{name: "required missing", fixture: imageArchiveFixture{nested: true}, requested: true, required: true, want: "required attestations"},
		{name: "only SBOM", fixture: imageArchiveFixture{sbom: true}, requested: true, required: true, want: "provenance"},
		{name: "only provenance", fixture: imageArchiveFixture{provenance: true}, requested: true, required: true, want: "SBOM"},
		{name: "descriptor binding mismatch", fixture: imageArchiveFixture{sbom: true, provenance: true, binding: "sha256:" + strings.Repeat("b", 64)}, requested: true, want: "attestation reference"},
		{name: "statement subject mismatch", fixture: imageArchiveFixture{sbom: true, provenance: true, subject: strings.Repeat("b", 64)}, requested: true, want: "subject"},
		{name: "predicate annotation mismatch", fixture: imageArchiveFixture{sbom: true, predicate: "https://other.example/predicate"}, requested: true, want: "predicate"},
		{name: "ambiguous images", fixture: imageArchiveFixture{extraImage: true}, want: "exactly one runnable"},
		{name: "wrong platform", fixture: imageArchiveFixture{badPlatform: true}, want: "platform"},
		{name: "corrupted blob", fixture: imageArchiveFixture{corrupt: true}, want: "digest"},
		{name: "unsafe path", fixture: imageArchiveFixture{unsafe: true}, want: "path"},
		{name: "duplicate entry", fixture: imageArchiveFixture{duplicate: true}, want: "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, _, _, _ := makeImageArchive(t, tc.fixture)
			result, err := InspectImageArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), Platform{OS: "linux", Architecture: "amd64"}, tc.requested, tc.required)
			if tc.warning {
				if err != nil || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], tc.want) {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				return
			}
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v, want %q", err, tc.want)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestInspectImageArchiveLargeSBOM(t *testing.T) {
	// A Python image inventory can exceed the Suite manifest's 100k member
	// budget even though every package object and the total bytes are small.
	packages := make([]any, 20_000)
	for i := range packages {
		packages[i] = map[string]any{"SPDXID": fmt.Sprintf("SPDXRef-package-%d", i), "name": "example-package", "versionInfo": "1.0", "downloadLocation": "NOASSERTION", "filesAnalyzed": false, "licenseConcluded": "NOASSERTION"}
	}
	files := make([]any, 1200)
	for i := range files {
		files[i] = map[string]any{"SPDXID": fmt.Sprintf("SPDXRef-file-%d", i), "fileName": fmt.Sprintf("/app/file-%d", i)}
	}
	predicate := map[string]any{"spdxVersion": "SPDX-2.3", "packages": packages, "files": files}
	archive, image, _, sbom := makeImageArchive(t, imageArchiveFixture{nested: true, sbom: true, provenance: true, predicateBody: predicate})
	result, err := InspectImageArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), Platform{OS: "linux", Architecture: "amd64"}, true, true)
	if err != nil || result.Digest != image || result.SBOMDigest != sbom {
		t.Fatalf("large SBOM: result=%+v error=%v", result, err)
	}
	data, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONTokens(data); err == nil || !strings.Contains(err.Error(), "member limit") {
		t.Fatalf("Suite JSON limits were relaxed: %v", err)
	}
}

func TestArchiveJSONMemberLimitsAndProtections(t *testing.T) {
	wide := map[string]string{}
	for i := range 2048 {
		wide[fmt.Sprintf("package-%d", i)] = "inventory"
	}
	data, err := json.Marshal(wide)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := decodeArchiveJSONWithLimits(data, &value, maxArchiveJSONBytes, maxArchiveObjectMembers, maxArchiveDocumentMembers); err != nil {
		t.Fatalf("wide inventory object: %v", err)
	}
	if err := validateJSONTokens(data); err == nil || !strings.Contains(err.Error(), "member limit") {
		t.Fatalf("Suite JSON limits were relaxed: %v", err)
	}
	for _, tc := range []struct{ data, want string }{
		{`{"predicate":{"package":"a","package":"b"}}`, "duplicate JSON key"},
		{strings.Repeat("[", 101) + "0" + strings.Repeat("]", 101), "JSON nesting"},
	} {
		if err := decodeArchiveJSONWithLimits([]byte(tc.data), &value, maxArchiveJSONBytes, maxArchiveObjectMembers, maxArchiveDocumentMembers); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("protection error=%v, want %q", err, tc.want)
		}
	}
}

func TestInspectImageArchiveRootArrayLimits(t *testing.T) {
	entries := "[" + strings.Repeat("{},", 1000) + "{}]"
	for _, tc := range []struct {
		name    string
		fixture imageArchiveFixture
	}{
		{"root index", imageArchiveFixture{indexBody: json.RawMessage(`{"schemaVersion":2,"manifests":` + entries + `}`)}},
		{"nested index", imageArchiveFixture{nested: true, indexBody: json.RawMessage(`{"schemaVersion":2,"manifests":` + entries + `}`)}},
		{"manifest layers", imageArchiveFixture{manifestBody: json.RawMessage(`{"schemaVersion":2,"config":{},"layers":` + entries + `}`)}},
		{"attestation subjects", imageArchiveFixture{sbom: true, statementBody: json.RawMessage(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://spdx.dev/Document","subject":` + entries + `,"predicate":{}}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, _, _, _ := makeImageArchive(t, tc.fixture)
			_, err := InspectImageArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), Platform{OS: "linux", Architecture: "amd64"}, false, false)
			if err == nil || !strings.Contains(err.Error(), "JSON array entry limit exceeded") {
				t.Fatalf("count limit must precede descriptor validation: error=%v", err)
			}
		})
	}
}

func TestArchiveJSONRootArrayLimitBeforeUnmarshal(t *testing.T) {
	for _, field := range []string{"manifests", "MANIFESTS", `manife\u0073ts`, "layers", "subject"} {
		for _, count := range []int{1000, 1001} {
			t.Run(fmt.Sprintf("%s/%d", field, count), func(t *testing.T) {
				entries := "[" + strings.Repeat("{},", count-1) + "{}]"
				data := []byte(`{"` + field + `":` + entries + `}`)
				if err := validateJSONTokens(data); err != nil {
					t.Fatalf("Suite array counts must remain unrestricted: %v", err)
				}
				var value struct {
					Manifests []ociDescriptor `json:"manifests"`
					Layers    []ociDescriptor `json:"layers"`
					Subject   []struct {
						Digest map[string]string `json:"digest"`
					} `json:"subject"`
				}
				err := decodeArchiveJSON(data, &value)
				length := len(value.Manifests) + len(value.Layers) + len(value.Subject)
				if count == 1000 {
					if err != nil || length != count {
						t.Fatalf("at limit: length=%d error=%v", length, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "JSON array entry limit exceeded") || length != 0 {
					t.Fatalf("count limit must precede typed allocation: length=%d error=%v", length, err)
				}
			})
		}
	}
}

func TestArchiveJSONStructuralMetadataLimits(t *testing.T) {
	var wide strings.Builder
	wide.WriteByte('{')
	for i := range 1025 {
		if i != 0 {
			wide.WriteByte(',')
		}
		fmt.Fprintf(&wide, `"field%d":0`, i)
	}
	wide.WriteByte('}')
	for _, tc := range []struct{ name, data, want string }{
		{"bytes", `{"padding":"` + strings.Repeat("x", 4<<20) + `"}`, "OCI JSON metadata exceeds 4194304 bytes"},
		{"object members", wide.String(), "JSON object member limit exceeded"},
		{"document members", `{"inventory":{"files":[` + strings.Repeat(`{"a":0,"b":0,"c":0,"d":0,"e":0},`, 20000) + `{}]}}`, "JSON object member limit exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value json.RawMessage
			if err := decodeArchiveJSON([]byte(tc.data), &value); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("structural metadata: error=%v, want %q", err, tc.want)
			}
			if err := decodeArchiveJSONWithLimits([]byte(tc.data), &value, maxArchiveJSONBytes, maxArchiveObjectMembers, maxArchiveDocumentMembers); err != nil {
				t.Fatalf("larger statement budget must accept inventory: %v", err)
			}
		})
	}
}

func TestInspectImageArchiveCanceled(t *testing.T) {
	archive, _, _, _ := makeImageArchive(t, imageArchiveFixture{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := InspectImageArchive(ctx, bytes.NewReader(archive), int64(len(archive)), Platform{OS: "linux", Architecture: "amd64"}, false, false)
	if err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}
