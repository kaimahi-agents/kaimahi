package oras

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

// PushResult identifies an AgentSuite pushed to a referenced OCI target.
type PushResult struct {
	Path       string
	Reference  string
	Updated    bool
	Descriptor ocispec.Descriptor
	Report     *agentsuite.Report
}

// Push deterministically packages an extracted AgentSuite directory and
// pushes it to a referenced OCI image-layout target. Existing layouts retain
// unrelated references.
func Push(
	ctx context.Context,
	source string,
	destination string,
	reference string,
) (PushResult, error) {
	if reference == "" {
		return PushResult{}, errors.New("AgentSuite push reference is required")
	}
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return PushResult{}, err
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return PushResult{}, err
	}
	destinationParent := filepath.Dir(destinationPath)
	parentInfo, err := os.Stat(destinationParent)
	if err != nil {
		return PushResult{}, fmt.Errorf("stat AgentSuite target parent: %w", err)
	}
	if !parentInfo.IsDir() {
		return PushResult{}, fmt.Errorf("AgentSuite target parent %s is not a directory", destinationParent)
	}
	canonicalSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return PushResult{}, fmt.Errorf("resolve AgentSuite directory: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(destinationParent)
	if err != nil {
		return PushResult{}, fmt.Errorf("resolve AgentSuite target parent: %w", err)
	}
	canonicalDestination := filepath.Join(canonicalParent, filepath.Base(destinationPath))
	if pathWithin(canonicalSource, canonicalDestination) {
		return PushResult{}, errors.New("AgentSuite target must be outside the source directory")
	}

	sourceRoot, err := os.MkdirTemp("", "agentsuite-source-cas-*")
	if err != nil {
		return PushResult{}, err
	}
	defer os.RemoveAll(sourceRoot)
	sourceStore, err := oci.New(sourceRoot)
	if err != nil {
		return PushResult{}, fmt.Errorf("create AgentSuite source CAS: %w", err)
	}
	contentDescriptor, err := pushDirectory(ctx, sourcePath, sourceStore)
	if err != nil {
		return PushResult{}, fmt.Errorf("archive AgentSuite directory: %w", err)
	}

	packedRoot, err := os.MkdirTemp("", "agentsuite-packed-*")
	if err != nil {
		return PushResult{}, fmt.Errorf("create staged AgentSuite artifact: %w", err)
	}
	defer os.RemoveAll(packedRoot)
	packedStore, err := oci.New(packedRoot)
	if err != nil {
		return PushResult{}, fmt.Errorf("create staged AgentSuite artifact: %w", err)
	}
	packed, err := New(nil).Pack(ctx, sourceStore, packedStore, contentDescriptor)
	if err != nil {
		return PushResult{}, err
	}

	targetPath := destinationPath
	publishTarget := false
	updatedTarget := false
	targetInfo, err := os.Lstat(destinationPath)
	if os.IsNotExist(err) {
		targetPath, err = os.MkdirTemp(destinationParent, "."+filepath.Base(destinationPath)+"-*")
		if err != nil {
			return PushResult{}, fmt.Errorf("create staged AgentSuite target: %w", err)
		}
		publishTarget = true
		defer func() {
			if publishTarget {
				_ = os.RemoveAll(targetPath)
			}
		}()
	} else if err != nil {
		return PushResult{}, fmt.Errorf("inspect AgentSuite target: %w", err)
	} else if !targetInfo.IsDir() {
		return PushResult{}, fmt.Errorf("AgentSuite target %s is not a directory", destinationPath)
	} else if err := requireOCILayout(destinationPath, "AgentSuite target"); err != nil {
		return PushResult{}, err
	} else {
		updatedTarget = true
	}
	target, err := oci.New(targetPath)
	if err != nil {
		return PushResult{}, fmt.Errorf("open AgentSuite OCI target: %w", err)
	}
	if _, err := NewPusher(target).Push(ctx, packedStore, packed.Descriptor, reference); err != nil {
		return PushResult{}, err
	}
	if publishTarget {
		if err := os.Rename(targetPath, destinationPath); err != nil {
			return PushResult{}, fmt.Errorf("publish AgentSuite OCI target: %w", err)
		}
		publishTarget = false
	}
	return PushResult{
		Path:       destinationPath,
		Reference:  reference,
		Updated:    updatedTarget,
		Descriptor: packed.Descriptor,
		Report:     packed.Report,
	}, nil
}

func requireOCILayout(root, subject string) error {
	for _, name := range []string{"oci-layout", "index.json"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			return fmt.Errorf("%s is not an OCI image layout: %s: %w", subject, name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not an OCI image layout: %s is not a regular file", subject, name)
		}
	}
	return nil
}

func pathWithin(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil {
		return false
	}
	return relative == "." ||
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pushDirectory(
	ctx context.Context,
	root string,
	dst agentsuite.Pusher,
) (ocispec.Descriptor, error) {
	if dst == nil {
		return ocispec.Descriptor{}, errors.New("AgentSuite directory destination is required")
	}
	if err := ctx.Err(); err != nil {
		return ocispec.Descriptor{}, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve AgentSuite directory: %w", err)
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("stat AgentSuite directory: %w", err)
	}
	if !info.IsDir() {
		return ocispec.Descriptor{}, fmt.Errorf("AgentSuite source %s is not a directory", root)
	}
	rootHandle, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open AgentSuite directory: %w", err)
	}
	defer rootHandle.Close()

	var names []string
	if err := fs.WalkDir(rootHandle.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "." {
			names = append(names, name)
		}
		return nil
	}); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("walk AgentSuite directory: %w", err)
	}
	slices.Sort(names)

	archive, err := os.CreateTemp("", "agentsuite-content-*.tar.gz")
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	if err := writeArchive(ctx, rootHandle, names, archive); err != nil {
		archive.Close()
		return ocispec.Descriptor{}, err
	}
	if err := archive.Close(); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("close AgentSuite content archive: %w", err)
	}

	archive, err = os.Open(archivePath)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	digest, err := godigest.FromReader(archive)
	if err != nil {
		archive.Close()
		return ocispec.Descriptor{}, fmt.Errorf("digest AgentSuite content archive: %w", err)
	}
	size, err := archive.Seek(0, io.SeekEnd)
	if err != nil {
		archive.Close()
		return ocispec.Descriptor{}, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		archive.Close()
		return ocispec.Descriptor{}, err
	}
	descriptor := ocispec.Descriptor{
		MediaType: agentsuite.MediaTypeContent,
		Digest:    digest,
		Size:      size,
	}
	pushErr := dst.Push(ctx, descriptor, archive)
	closeErr := archive.Close()
	if pushErr != nil {
		return ocispec.Descriptor{}, fmt.Errorf("store AgentSuite content archive: %w", pushErr)
	}
	if closeErr != nil {
		return ocispec.Descriptor{}, fmt.Errorf("close AgentSuite content archive: %w", closeErr)
	}
	return descriptor, ctx.Err()
}

func writeArchive(
	ctx context.Context,
	root *os.Root,
	names []string,
	dst io.Writer,
) (err error) {
	gzipWriter := gzip.NewWriter(dst)
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	defer func() {
		err = errors.Join(err, tarWriter.Close(), gzipWriter.Close())
	}()

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("stat AgentSuite content %s: %w", name, err)
		}
		header := &tar.Header{
			Name:       filepath.ToSlash(name),
			Uid:        0,
			Gid:        0,
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Format:     tar.FormatPAX,
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			header.Name += "/"
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		case mode.IsRegular():
			header.Typeflag = tar.TypeReg
			header.Mode = 0o644
			if mode.Perm()&0o111 != 0 {
				header.Mode = 0o755
			}
			header.Size = info.Size()
			file, err := root.Open(name)
			if err != nil {
				return fmt.Errorf("open AgentSuite content %s: %w", name, err)
			}
			openedInfo, err := file.Stat()
			if err != nil {
				file.Close()
				return fmt.Errorf("stat opened AgentSuite content %s: %w", name, err)
			}
			if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
				file.Close()
				return fmt.Errorf("AgentSuite content %s changed while opening", name)
			}
			if err := tarWriter.WriteHeader(header); err != nil {
				file.Close()
				return err
			}
			written, copyErr := io.CopyN(tarWriter, &contextReader{ctx: ctx, reader: file}, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("read AgentSuite content %s: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close AgentSuite content %s: %w", name, closeErr)
			}
			if written != header.Size {
				return fmt.Errorf("AgentSuite content %s size changed while reading", name)
			}
			continue
		case mode&os.ModeSymlink != 0:
			target, err := root.Readlink(name)
			if err != nil {
				return fmt.Errorf("read AgentSuite symlink %s: %w", name, err)
			}
			header.Typeflag = tar.TypeSymlink
			header.Mode = 0o777
			header.Linkname = filepath.ToSlash(target)
		default:
			return fmt.Errorf("AgentSuite content %s has unsupported file type %s", name, mode.Type())
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
