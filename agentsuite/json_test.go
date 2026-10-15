package agentsuite

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalProducesCanonicalJSON(t *testing.T) {
	data, err := Marshal(Suite{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeSuite,
		Name:          "example",
		Agents:        []ManifestRef{},
		ToolProviderCatalog: ManifestRef{
			ID:   "catalog",
			Path: "tool-providers/catalog.json",
			Descriptor: Descriptor{
				MediaType: MediaTypeToolProviderCatalog,
				Digest:    strings.Repeat("a", 64),
				Size:      2,
			},
		},
		Compositions:  []CompositionRef{},
		BuildProfiles: []ManifestRef{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsAny(data, "\n\t ") {
		t.Fatalf("Marshal() returned non-canonical whitespace: %q", data)
	}
	if !bytes.HasPrefix(data, []byte(`{"agents":[],"buildProfiles":[]`)) {
		t.Fatalf("Marshal() did not sort object keys: %s", data)
	}
}

func TestMarshalRejectsLossyJCSNumber(t *testing.T) {
	if _, err := Marshal(struct {
		Value uint64 `json:"value"`
	}{Value: 9_007_199_254_740_993}); err == nil {
		t.Fatal("Marshal() accepted a number that changes during JCS canonicalization")
	}
}

func TestUnmarshalRejectsUnknownFields(t *testing.T) {
	var platform Platform
	err := Unmarshal([]byte(`{"os":"linux","architecture":"amd64","unexpected":true}`), &platform)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Unmarshal() error = %v, want unknown field", err)
	}
}

func TestDigestUsesCanonicalJSON(t *testing.T) {
	left, err := Digest([]byte("{\n  \"b\": 2,\n  \"a\": 1\n}"))
	if err != nil {
		t.Fatal(err)
	}
	right, err := Digest([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("Digest() differs for equivalent documents: %s != %s", left, right)
	}
}

func TestDescriptorUnmarshalJSONAcceptsValidShapes(t *testing.T) {
	cases := map[string]string{
		"minimal":          `{"mediaType":"text/plain","digest":"sha256:` + strings.Repeat("a", 64) + `","size":2}`,
		"zero size":        `{"mediaType":"text/plain","digest":"sha256:` + strings.Repeat("a", 64) + `","size":0}`,
		"with annotations": `{"mediaType":"text/plain","digest":"sha256:` + strings.Repeat("a", 64) + `","size":2,"annotations":{"k":"v"}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var descriptor Descriptor
			if err := json.Unmarshal([]byte(doc), &descriptor); err != nil {
				t.Fatalf("UnmarshalJSON() error = %v", err)
			}
		})
	}
}

func TestDescriptorUnmarshalJSONRejectsClosedShapeViolations(t *testing.T) {
	digest := strings.Repeat("a", 64)
	cases := map[string]string{
		"unknown field": `{"mediaType":"text/plain","digest":"sha256:` + digest + `","size":2,"extra":true}`,
		// OCI's inline-content "data" field is unsupported here; reject it too.
		"data field":        `{"mediaType":"text/plain","digest":"sha256:` + digest + `","size":2,"data":"aGVsbG8="}`,
		"null mediaType":    `{"mediaType":null,"digest":"sha256:` + digest + `","size":2}`,
		"null digest":       `{"mediaType":"text/plain","digest":null,"size":2}`,
		"null size":         `{"mediaType":"text/plain","digest":"sha256:` + digest + `","size":null}`,
		"null annotations":  `{"mediaType":"text/plain","digest":"sha256:` + digest + `","size":2,"annotations":null}`,
		"missing size":      `{"mediaType":"text/plain","digest":"sha256:` + digest + `"}`,
		"missing mediaType": `{"digest":"sha256:` + digest + `","size":2}`,
		"missing digest":    `{"mediaType":"text/plain","size":2}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var descriptor Descriptor
			if err := json.Unmarshal([]byte(doc), &descriptor); err == nil {
				t.Fatalf("UnmarshalJSON(%s) = nil error, want rejection", doc)
			}
		})
	}
}

func TestIsValidDigest(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if !IsValidDigest(valid) {
		t.Fatalf("IsValidDigest(%q) = false", valid)
	}
	for _, invalid := range []string{
		"",
		strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("a", 63),
	} {
		if IsValidDigest(invalid) {
			t.Errorf("IsValidDigest(%q) = true", invalid)
		}
	}
}
