package localartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializePreservesEntriesAndDetectsMetadataDrift(t *testing.T) {
	records := []tarRecord{
		{header: tar.Header{Name: "payload", Typeflag: tar.TypeDir, Mode: 0755}},
		{header: tar.Header{Name: "empty", Typeflag: tar.TypeDir, Mode: 0750}},
		{header: tar.Header{Name: "payload/tool", Typeflag: tar.TypeReg, Mode: 0555, Size: 1}, body: []byte("x")},
		{header: tar.Header{Name: "payload/hard", Typeflag: tar.TypeLink, Mode: 0555, Linkname: "payload/tool"}},
		{header: tar.Header{Name: "payload/link", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: "tool"}},
	}
	metadata := map[string]entry{}
	files, err := filesWithEntries(t.Context(), contentArchive(t, records), metadata, false)
	if err != nil {
		t.Fatal(err)
	}
	a := &Artifact{files: files, entries: metadata}
	root := filepath.Join(t.TempDir(), "materialized")
	if err := a.Materialize(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	tool, err := os.Stat(filepath.Join(root, "payload/tool"))
	if err != nil || tool.Mode().Perm() != 0555 {
		t.Fatal("execute mode lost", err)
	}
	hard, err := os.Stat(filepath.Join(root, "payload/hard"))
	if err != nil || !os.SameFile(tool, hard) {
		t.Fatal("hardlink lost", err)
	}
	link, err := os.Readlink(filepath.Join(root, "payload/link"))
	if err != nil || link != "tool" {
		t.Fatal("symlink changed", err)
	}
	dir, err := os.Stat(filepath.Join(root, "empty"))
	if err != nil || dir.Mode().Perm() != 0750 {
		t.Fatal("empty directory lost", err)
	}
	for _, tc := range []entry{{tar.TypeReg, 0644, ""}, {tar.TypeSymlink, 0777, "other"}, {tar.TypeLink, 0555, "payload/other"}} {
		copy := &Artifact{files: files, entries: map[string]entry{}}
		for k, v := range metadata {
			copy.entries[k] = v
		}
		copy.entries["payload/tool"] = tc
		if a.SameContent(copy) {
			t.Fatal("metadata drift ignored")
		}
	}
	if err := a.Materialize(t.Context(), root); err == nil {
		t.Fatal("existing output overwritten")
	}
}

func TestMaterializeRefusesUnreadableModesBeforeCreatingOutput(t *testing.T) {
	for _, kind := range []byte{tar.TypeReg, tar.TypeDir} {
		for _, mode := range []int64{0, 0100, 0200} {
			t.Run(fmt.Sprintf("%c/%o", kind, mode), func(t *testing.T) {
				metadata := map[string]entry{}
				files, err := filesWithEntries(t.Context(), contentArchive(t, []tarRecord{{header: tar.Header{Name: "payload", Typeflag: kind, Mode: mode}}}), metadata, false)
				if err != nil {
					t.Fatal("valid packaged mode refused at ingestion", err)
				}
				a := &Artifact{files: files, entries: metadata}
				root := filepath.Join(t.TempDir(), "stage")
				if err := a.Materialize(t.Context(), root); err == nil {
					t.Fatal("unreadable staging mode accepted")
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("mode refusal created staging", err)
				}
				if metadata["payload"].Mode != mode {
					t.Fatal("refusal changed declared mode identity")
				}
			})
		}
	}
}

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
