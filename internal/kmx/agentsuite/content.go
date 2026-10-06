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
	"unicode/utf8"
)

const (
	maxContentEntries        = 100_000
	maxContentBytes          = int64(4 << 30)
	maxRetainedMetadataBytes = int64(256 << 20)
	maxPathBytes             = 1024
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
	set := &contentSet{entries: map[string]contentEntry{}, folded: map[string]string{}}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	count := 0
	var total, retained int64
	err = fs.WalkDir(rootHandle.FS(), ".", func(current string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == "." {
			return nil
		}
		count++
		if count > maxContentEntries {
			return fmt.Errorf("content has more than %d entries", maxContentEntries)
		}
		name, err := validateContentPath(current)
		if err != nil {
			return err
		}
		info, err := rootHandle.Lstat(current)
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
			file, err := rootHandle.Open(current)
			if err != nil {
				return err
			}
			openedInfo, err := file.Stat()
			if err != nil {
				file.Close()
				return err
			}
			if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
				file.Close()
				return fmt.Errorf("%s changed while opening", name)
			}
			mode = openedInfo.Mode()
			if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || mode.Perm()&0o022 != 0 {
				file.Close()
				return fmt.Errorf("%s has unsafe file mode", name)
			}
			record.Type = "file"
			record.Mode = uint32(mode.Perm())
			record.Size = openedInfo.Size()
			if record.Size < 0 || record.Size > maxContentBytes-total {
				file.Close()
				return fmt.Errorf("content exceeds %d bytes", maxContentBytes)
			}
			total += record.Size
			hash := sha256.New()
			var data []byte
			if shouldRetainMetadata(name, record.Size) {
				var nextRetained int64
				nextRetained, err = addRetainedMetadata(retained, record.Size)
				if err != nil {
					file.Close()
					return err
				}
				data, err = io.ReadAll(io.LimitReader(io.TeeReader(file, hash), record.Size+1))
				if err == nil && int64(len(data)) != record.Size {
					err = fmt.Errorf("%s size changed while reading", name)
				}
				retained = nextRetained
			} else {
				var written int64
				written, err = io.Copy(hash, io.LimitReader(file, record.Size+1))
				if err == nil && written != record.Size {
					err = fmt.Errorf("%s size changed while reading", name)
				}
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
			target, err := rootHandle.Readlink(current)
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
	set := &contentSet{entries: map[string]contentEntry{}, folded: map[string]string{}}
	regular := map[string]bool{}
	var total, retained int64
	count := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read content tar: %w", err)
		}
		count++
		if count > maxContentEntries {
			return nil, fmt.Errorf("content has more than %d entries", maxContentEntries)
		}
		rawName := header.Name
		if header.Typeflag == tar.TypeDir {
			rawName = strings.TrimSuffix(rawName, "/")
		}
		name, err := validateContentPath(rawName)
		if err != nil {
			return nil, err
		}
		if header.Mode&0o7000 != 0 {
			return nil, fmt.Errorf("%s has setid or sticky mode %#o", name, header.Mode)
		}
		if header.Mode&0o022 != 0 && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeLink) {
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
		case tar.TypeReg:
			record.Type = "file"
			if header.Size < 0 || header.Size > maxContentBytes-total {
				return nil, fmt.Errorf("content exceeds %d bytes", maxContentBytes)
			}
			total += header.Size
			hash := sha256.New()
			if shouldRetainMetadata(name, header.Size) {
				nextRetained, err := addRetainedMetadata(retained, header.Size)
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(io.LimitReader(io.TeeReader(tarReader, hash), header.Size+1))
				if err != nil {
					return nil, err
				}
				if int64(len(data)) != header.Size {
					return nil, fmt.Errorf("%s size does not match tar header", name)
				}
				record.Data = data
				retained = nextRetained
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
	written, err := io.CopyN(io.Discard, gzipReader, 1)
	if written != 0 {
		return nil, errors.New("content gzip contains data after the tar archive")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("finish content gzip stream: %w", err)
	}
	return set, nil
}

func (s *contentSet) add(entry contentEntry) error {
	if _, exists := s.entries[entry.Path]; exists {
		return fmt.Errorf("duplicate content path %s", entry.Path)
	}
	folded := foldCase(entry.Path)
	if existing, ok := s.folded[folded]; ok {
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
	return strings.HasSuffix(name, ".json") && size >= 0 && size <= maxJSONBytes
}

func addRetainedMetadata(retained, size int64) (int64, error) {
	if retained < 0 || size < 0 || size > maxRetainedMetadataBytes-retained {
		return 0, fmt.Errorf("retained metadata exceeds %d bytes", maxRetainedMetadataBytes)
	}
	return retained + size, nil
}

func validateContentPath(value string) (string, error) {
	if value == "" || len(value) > maxPathBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
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
	if target == "" || len(target) > maxPathBytes || !utf8.ValidString(target) || strings.ContainsRune(target, '\x00') ||
		strings.Contains(target, "\\") || strings.HasPrefix(target, "/") {
		return fmt.Errorf("%s has unsafe link target %q", name, target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%s link target escapes the content root", name)
	}
	return nil
}
