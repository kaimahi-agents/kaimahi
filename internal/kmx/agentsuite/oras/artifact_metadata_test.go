package oras

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"testing"
)

func TestArtifactRefusesHiddenMetadataBeforeTarNext(t *testing.T) {
	// archive/tar consumes both PAX headers and replaces the nonempty PAX map
	// with the empty map, so checking only the returned file header misses them.
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for _, item := range []struct{ name, body string }{{"first", "14 comment=ok\n"}, {"second", ""}, {"agentsuite.json", "{}"}} {
		if err := tw.WriteHeader(&tar.Header{Name: item.name, Mode: 0644, Size: int64(len(item.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(item.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := data.Bytes()
	for _, offset := range []int{0, 1024} {
		header := raw[offset : offset+512]
		header[156] = tar.TypeXHeader
		for i := 148; i < 156; i++ {
			header[i] = ' '
		}
		sum := 0
		for _, v := range header {
			sum += int(v)
		}
		copy(header[148:156], []byte(fmt.Sprintf("%06o\x00 ", sum)))
	}
	// The fixture must demonstrate the standard reader's metadata hiding, rather
	// than relying on malformed headers to trigger the expected refusal.
	reader := tar.NewReader(bytes.NewReader(raw))
	h, err := reader.Next()
	if err != nil || h.Name != "agentsuite.json" || len(h.PAXRecords) != 0 {
		t.Fatalf("fixture did not hide metadata: %+v %v", h, err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactFiles(t.Context(), compressed.Bytes()); !errors.Is(err, ErrArtifact) {
		t.Fatalf("hidden PAX headers accepted: %v", err)
	}
}
