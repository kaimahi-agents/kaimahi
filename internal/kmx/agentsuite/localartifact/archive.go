package localartifact

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"

	suitecore "github.com/agent-primitives/suite/core"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	outerArchiveBytes   = 64 << 20
	outerArchiveEntries = 4096
	outerPathBytes      = 1024
)

// ReadArchive freezes and reads an uncompressed OCI image-layout tar.
func ReadArchive(ctx context.Context, source io.ReaderAt, size int64, digest string) (*Artifact, error) {
	return readArchive(ctx, source, size, digest, false)
}

func readArchive(ctx context.Context, source io.ReaderAt, size int64, digest string, flowMode bool) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || size < 0 || digest != "" && !suitecore.IsValidDigest(digest) {
		return nil, ErrArtifact
	}
	if size > outerArchiveBytes {
		return nil, ErrArtifactLimit
	}
	raw, err := io.ReadAll(&contextReader{ctx: ctx, reader: io.NewSectionReader(source, 0, size)})
	if err != nil || int64(len(raw)) != size {
		return nil, errors.Join(ErrArtifact, err)
	}
	files, err := archiveFiles(ctx, raw)
	if err != nil {
		return nil, err
	}
	return readSource(ctx, archiveSource{files: files}, digest, flowMode)
}

type archiveSource struct{ files map[string][]byte }

func (s archiveSource) read(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := s.files[name]
	if !ok {
		return nil, ErrArtifact
	}
	limit := manifestBytes
	if name == "oci-layout" {
		limit = 1024
	}
	if len(raw) > limit {
		return nil, ErrArtifactLimit
	}
	return bytes.Clone(raw), nil
}

func (s archiveSource) Fetch(ctx context.Context, descriptor ocispec.Descriptor) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !suitecore.IsValidDigest(descriptor.Digest.String()) {
		return nil, ErrArtifact
	}
	raw, ok := s.files["blobs/sha256/"+descriptor.Digest.Encoded()]
	if !ok {
		return nil, ErrArtifact
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func archiveFiles(ctx context.Context, raw []byte) (map[string][]byte, error) {
	if len(raw) < 1024 || len(raw)%512 != 0 {
		return nil, ErrArtifact
	}
	if len(raw) > outerArchiveBytes {
		return nil, ErrArtifactLimit
	}
	terminator := raw[len(raw)-1024:]
	if len(bytes.Trim(terminator, "\x00")) != 0 {
		return nil, ErrArtifact
	}
	stream := bytes.NewReader(raw[:len(raw)-1024])
	reader := tar.NewReader(stream)
	files := map[string][]byte{}
	types := map[string]byte{}
	rootSeen := false
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := (len(raw) - 1024 - stream.Len() + 511) / 512 * 512
		if offset+512 <= len(raw)-1024 {
			kind := raw[offset+156]
			if kind != tar.TypeReg && kind != 0 && kind != tar.TypeDir {
				return nil, ErrArtifact
			}
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrArtifact
		}
		if entries >= outerArchiveEntries || len(header.Name) > outerPathBytes {
			return nil, ErrArtifactLimit
		}
		name, err := archivePath(header)
		if err != nil || len(header.PAXRecords) != 0 {
			return nil, ErrArtifact
		}
		if name == "" {
			if rootSeen || header.Typeflag != tar.TypeDir || header.Size != 0 {
				return nil, ErrArtifact
			}
			rootSeen = true
			continue
		}
		_, duplicate := types[name]
		if duplicate || archivePathConflict(name, header.Typeflag == tar.TypeDir, types) {
			return nil, ErrArtifact
		}
		types[name] = header.Typeflag
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, ErrArtifact
			}
		case tar.TypeReg, 0:
			if header.Size < 0 {
				return nil, ErrArtifact
			}
			if header.Size > outerArchiveBytes {
				return nil, ErrArtifactLimit
			}
			data, err := io.ReadAll(&contextReader{ctx: ctx, reader: reader})
			if err != nil || int64(len(data)) != header.Size {
				return nil, ErrArtifact
			}
			files[name] = data
		default:
			return nil, ErrArtifact
		}
	}
	if stream.Len() != 0 && len(bytes.Trim(raw[len(raw)-1024-stream.Len():len(raw)-1024], "\x00")) != 0 {
		return nil, ErrArtifact
	}
	return files, nil
}

func archivePath(header *tar.Header) (string, error) {
	name := strings.TrimPrefix(header.Name, "./")
	if header.Typeflag == tar.TypeDir {
		if name == "" && header.Name == "./" {
			return "", nil
		}
		original := name
		name = strings.TrimSuffix(name, "/")
		if original != name+"/" {
			return "", ErrArtifact
		}
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || !fs.ValidPath(name) || name == "." {
		return "", ErrArtifact
	}
	return name, nil
}

func archivePathConflict(name string, directory bool, types map[string]byte) bool {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if kind, ok := types[parent]; ok && kind != tar.TypeDir {
			return true
		}
	}
	if directory {
		return false
	}
	prefix := name + "/"
	for existing := range types {
		if strings.HasPrefix(existing, prefix) {
			return true
		}
	}
	return false
}
