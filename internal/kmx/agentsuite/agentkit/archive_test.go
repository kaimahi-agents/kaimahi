package agentkit

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func testOCIArchive(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	write := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add := func(media string, data []byte) map[string]any {
		hash := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(hash[:])
		write("blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), data)
		return map[string]any{"mediaType": media, "digest": digest, "size": len(data)}
	}
	config := add("application/vnd.oci.image.config.v1+json", []byte(`{"os":"linux","architecture":"amd64"}`))
	manifest, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config, "layers": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	image := add("application/vnd.oci.image.manifest.v1+json", manifest)
	image["platform"] = map[string]string{"os": "linux", "architecture": "amd64"}
	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{image}})
	if err != nil {
		t.Fatal(err)
	}
	write("index.json", index)
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
