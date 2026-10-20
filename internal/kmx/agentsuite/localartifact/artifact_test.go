package localartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"testing"
)

func TestFilesRejectsFileAncestorCollisions(t *testing.T) {
	for _, names := range [][]string{{"agents", "agents/writer.json"}, {"agents/writer.json", "agents"}} {
		compressed := contentArchive(t, []tarRecord{
			{header: tar.Header{Name: names[0], Mode: 0o644, Typeflag: tar.TypeReg, Size: 1}, body: []byte("x")},
			{header: tar.Header{Name: names[1], Mode: 0o644, Typeflag: tar.TypeReg, Size: 1}, body: []byte("x")},
		})
		if _, err := Files(t.Context(), compressed); !errors.Is(err, ErrArtifact) {
			t.Fatalf("file ancestor collision accepted for %v: %v", names, err)
		}
	}
}

func TestFilesAcceptsConfinedLinksWithoutProjectingAliases(t *testing.T) {
	records := []tarRecord{
		{header: tar.Header{Name: "payload/tool", Mode: 0o555, Typeflag: tar.TypeReg, Size: 1}, body: []byte("x")},
		{header: tar.Header{Name: "payload/tool-hard", Mode: 0o555, Typeflag: tar.TypeLink, Linkname: "payload/tool"}},
		{header: tar.Header{Name: "payload/tool-link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "tool"}},
	}
	files, err := Files(t.Context(), contentArchive(t, records))
	if err != nil {
		t.Fatal(err)
	}
	if string(files["payload/tool"]) != "x" || files["payload/tool-hard"] != nil || files["payload/tool-link"] != nil {
		t.Fatal("links became projected regular-file aliases", files)
	}
}

func TestFilesRejectsLinkAncestorCollisions(t *testing.T) {
	records := []tarRecord{
		{header: tar.Header{Name: "payload/root", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{header: tar.Header{Name: "payload/root/child", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1}, body: []byte("x")},
	}
	if _, err := Files(t.Context(), contentArchive(t, records)); !errors.Is(err, ErrArtifact) {
		t.Fatalf("link ancestor collision accepted: %v", err)
	}
}

func TestFilesClassifiesLimits(t *testing.T) {
	record := tarRecord{header: tar.Header{Name: "too-large", Mode: 0o644, Typeflag: tar.TypeReg, Size: fileBytes + 1}, body: bytes.Repeat([]byte("x"), fileBytes+1)}
	if _, err := Files(t.Context(), contentArchive(t, []tarRecord{record})); !errors.Is(err, ErrArtifactLimit) {
		t.Fatalf("file budget was not a limit refusal: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Files(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
}

func TestFilesRequiresPhysicalTerminator(t *testing.T) {
	records := []tarRecord{{header: tar.Header{Name: "zero-payload", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1024}, body: make([]byte, 1024)}}
	raw := contentArchiveWithoutTerminator(t, records)
	if _, err := Files(t.Context(), raw); !errors.Is(err, ErrArtifact) {
		t.Fatalf("content archive without terminator accepted: %v", err)
	}
}

type tarRecord struct {
	header tar.Header
	body   []byte
}

func contentArchive(t *testing.T, records []tarRecord) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, record := range records {
		header := record.header
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(record.body) != 0 {
			if _, err := io.Copy(tarWriter, bytes.NewReader(record.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func contentArchiveWithoutTerminator(t *testing.T, records []tarRecord) []byte {
	t.Helper()
	var tarBytes bytes.Buffer
	tarWriter := tar.NewWriter(&tarBytes)
	for _, record := range records {
		header := record.header
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(record.body) != 0 {
			if _, err := tarWriter.Write(record.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Flush(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(tarBytes.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
