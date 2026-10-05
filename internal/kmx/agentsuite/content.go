package agentsuite

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxContentEntries = 100_000
	maxContentBytes   = int64(4 << 30)
	maxRetainedBytes  = int64(256 << 20)
	maxPathBytes      = 1024
)

type contentEntry struct {
	Path       string
	Type       string
	Mode       uint32
	UID        int
	GID        int
	Size       int64
	Digest     string
	LinkTarget string
	Data       []byte
}

type contentSet struct {
	entries map[string]contentEntry
	folded  map[string]string
}

func loadDirectory(root string) (*contentSet, error) {
	set := newContentSet()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	count := 0
	var total, retained int64
	err = filepath.WalkDir(canonicalRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == canonicalRoot {
			return nil
		}
		count++
		if count > maxContentEntries {
			return fmt.Errorf("content has more than %d entries", maxContentEntries)
		}
		rel, err := filepath.Rel(canonicalRoot, current)
		if err != nil {
			return err
		}
		name, err := validateContentPath(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("%s has setid or sticky mode", name)
		}
		record := contentEntry{Path: name, Mode: uint32(mode.Perm()), Size: info.Size()}
		switch {
		case mode.IsDir():
			record.Type = "directory"
		case mode.IsRegular():
			if mode.Perm()&0o022 != 0 {
				return fmt.Errorf("%s is group or world writable", name)
			}
			record.Type = "file"
			total += info.Size()
			if total > maxContentBytes {
				return fmt.Errorf("content exceeds %d bytes", maxContentBytes)
			}
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			hash := sha256.New()
			var data []byte
			if shouldRetainMetadata(name, info.Size()) {
				retained += info.Size()
				if retained > maxRetainedBytes {
					return fmt.Errorf("retained metadata exceeds %d bytes", maxRetainedBytes)
				}
				data, err = io.ReadAll(io.TeeReader(file, hash))
			} else {
				_, err = io.Copy(hash, file)
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			record.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
			record.Data = data
		case mode&os.ModeSymlink != 0:
			record.Type = "symlink"
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			if err := validateLinkTarget(name, filepath.ToSlash(target)); err != nil {
				return err
			}
			record.LinkTarget = filepath.ToSlash(target)
			record.Size = 0
		default:
			return fmt.Errorf("%s has unsupported file type %s", name, mode.Type())
		}
		if err := set.add(record); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

func loadContentLayer(reader io.Reader) (*contentSet, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("content layer is not gzip: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	set := newContentSet()
	regular := map[string]bool{}
	var total, retained int64
	for count := 0; ; count++ {
		if count >= maxContentEntries {
			return nil, fmt.Errorf("content has more than %d entries", maxContentEntries)
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read content tar: %w", err)
		}
		name, err := validateContentPath(header.Name)
		if err != nil {
			return nil, err
		}
		if header.Mode&0o7000 != 0 {
			return nil, fmt.Errorf("%s has setid or sticky mode %#o", name, header.Mode)
		}
		if header.Mode&0o022 != 0 &&
			(header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA || header.Typeflag == tar.TypeLink) {
			return nil, fmt.Errorf("%s is group or world writable", name)
		}
		record := contentEntry{
			Path: name,
			Mode: uint32(header.Mode & 0o777),
			UID:  header.Uid,
			GID:  header.Gid,
			Size: header.Size,
		}
		switch header.Typeflag {
		case tar.TypeDir:
			record.Type = "directory"
			record.Size = 0
		case tar.TypeReg, tar.TypeRegA:
			record.Type = "file"
			total += header.Size
			if header.Size < 0 || total > maxContentBytes {
				return nil, fmt.Errorf("content exceeds %d bytes", maxContentBytes)
			}
			hash := sha256.New()
			if shouldRetainMetadata(name, header.Size) {
				retained += header.Size
				if retained > maxRetainedBytes {
					return nil, fmt.Errorf("retained metadata exceeds %d bytes", maxRetainedBytes)
				}
				data, err := io.ReadAll(io.LimitReader(io.TeeReader(tarReader, hash), header.Size+1))
				if err != nil {
					return nil, err
				}
				if int64(len(data)) != header.Size {
					return nil, fmt.Errorf("%s size does not match tar header", name)
				}
				record.Data = data
			} else if written, err := io.CopyN(hash, tarReader, header.Size); err != nil || written != header.Size {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			record.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
			regular[name] = true
		case tar.TypeSymlink:
			record.Type = "symlink"
			if err := validateLinkTarget(name, header.Linkname); err != nil {
				return nil, err
			}
			record.LinkTarget = header.Linkname
			record.Size = 0
		case tar.TypeLink:
			record.Type = "hardlink"
			target, err := validateContentPath(header.Linkname)
			if err != nil {
				return nil, fmt.Errorf("%s hardlink target: %w", name, err)
			}
			if !regular[target] {
				return nil, fmt.Errorf("%s hardlink target %s is not an earlier regular file", name, target)
			}
			record.LinkTarget = target
			record.Size = 0
		default:
			return nil, fmt.Errorf("%s has unsupported tar type %d", name, header.Typeflag)
		}
		if err := set.add(record); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func newContentSet() *contentSet {
	return &contentSet{
		entries: map[string]contentEntry{},
		folded:  map[string]string{},
	}
}

func (s *contentSet) add(entry contentEntry) error {
	if _, exists := s.entries[entry.Path]; exists {
		return fmt.Errorf("duplicate content path %s", entry.Path)
	}
	folded := unicodeFold.String(entry.Path)
	if existing, exists := s.folded[folded]; exists {
		return fmt.Errorf("case-folding path collision between %s and %s", existing, entry.Path)
	}
	s.entries[entry.Path] = entry
	s.folded[folded] = entry.Path
	return nil
}

func (s *contentSet) data(name string) ([]byte, error) {
	name, err := validateContentPath(name)
	if err != nil {
		return nil, err
	}
	entry, ok := s.entries[name]
	if !ok {
		return nil, fmt.Errorf("required file %s is missing", name)
	}
	if entry.Type != "file" {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if entry.Data == nil {
		return nil, fmt.Errorf("%s is not retained metadata", name)
	}
	return append([]byte(nil), entry.Data...), nil
}

func shouldRetainMetadata(name string, size int64) bool {
	return strings.HasSuffix(name, ".json") && size <= int64(maxJSONBytes)
}

func validateContentPath(value string) (string, error) {
	if value == "" || len(value) > maxPathBytes || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("unsafe content path %q", value)
	}
	if strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("content path must be relative POSIX syntax: %q", value)
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("content path is not normalized below the root: %q", value)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("content path contains an unsafe segment: %q", value)
		}
	}
	return clean, nil
}

func validateLinkTarget(name, target string) error {
	if target == "" || strings.Contains(target, "\\") || strings.HasPrefix(target, "/") {
		return fmt.Errorf("%s has unsafe link target %q", name, target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%s link target escapes the content root", name)
	}
	return nil
}
