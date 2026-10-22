//go:build !windows

package oras

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func extractContentLayer(ctx context.Context, src io.Reader, destination string) (err error) {
	gzipReader, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("open AgentSuite content gzip: %w", err)
	}
	defer func() {
		err = errors.Join(err, gzipReader.Close())
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open AgentSuite extraction root: %w", err)
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	tarReader := tar.NewReader(gzipReader)
	var directories []extractedDirectory
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read AgentSuite content tar: %w", err)
		}
		name, err := extractionPath(header)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("create AgentSuite directory %s: %w", header.Name, err)
			}
			directories = append(directories, extractedDirectory{name: name, mode: os.FileMode(header.Mode & 0o777)})
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode&0o777))
			if err != nil {
				return fmt.Errorf("create AgentSuite file %s: %w", header.Name, err)
			}
			written, copyErr := io.CopyN(file, &contextReader{ctx: ctx, reader: tarReader}, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract AgentSuite file %s: %w", header.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close AgentSuite file %s: %w", header.Name, closeErr)
			}
			if written != header.Size {
				return fmt.Errorf("AgentSuite file %s size does not match its header", header.Name)
			}
			if err := root.Chmod(name, os.FileMode(header.Mode&0o777)); err != nil {
				return fmt.Errorf("set AgentSuite file mode %s: %w", header.Name, err)
			}
			if err := root.Chtimes(name, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
				return fmt.Errorf("set AgentSuite file time %s: %w", header.Name, err)
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			if err := root.Symlink(filepath.FromSlash(header.Linkname), name); err != nil {
				return fmt.Errorf("create AgentSuite symlink %s: %w", header.Name, err)
			}
		case tar.TypeLink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			if err := root.Link(filepath.FromSlash(header.Linkname), name); err != nil {
				return fmt.Errorf("create AgentSuite hardlink %s: %w", header.Name, err)
			}
		default:
			return fmt.Errorf("AgentSuite content %s has unsupported tar type %d", header.Name, header.Typeflag)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		directory := directories[i]
		if err := root.Chmod(directory.name, directory.mode); err != nil {
			return fmt.Errorf("set AgentSuite directory mode %s: %w", directory.name, err)
		}
		if err := root.Chtimes(directory.name, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			return fmt.Errorf("set AgentSuite directory time %s: %w", directory.name, err)
		}
	}
	return nil
}
