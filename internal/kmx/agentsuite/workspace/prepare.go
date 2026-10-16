package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// Prepared identifies the exact verified artifact to transfer, not a source
// directory to re-archive. Fingerprint identifies only selected authored inputs.
type Prepared struct {
	Path        string
	Fingerprint string
	Descriptor  ocispec.Descriptor
	Name        string
}

// SourceFile is a frozen authored input, before resolution or canonicalization.
// Mode preserves Git's executable-bit comparison; source bytes bind the fingerprint.
type SourceFile struct {
	Data []byte
	Mode fs.FileMode
}

// SourceFiles returns only authored inputs belonging to the selected candidate.
// It does not expose generated cache files or unrelated workspace content.
func SourceFiles(ctx context.Context, root string, prepared *Prepared) (map[string]SourceFile, error) {
	s, err := loadSource(ctx, root)
	if err != nil {
		return nil, err
	}
	if s.fingerprint != prepared.Fingerprint {
		return nil, errors.New("workspace source changed after preparation")
	}
	return s.source, nil
}

type cacheRecord struct {
	Fingerprint string             `json:"fingerprint"`
	Descriptor  ocispec.Descriptor `json:"descriptor"`
	Name        string             `json:"name"`
}

func equalFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, v := range a {
		if !bytes.Equal(v, b[name]) {
			return false
		}
	}
	return true
}
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func privateTree(root string, setMode bool) error {
	return filepath.WalkDir(root, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("cache symlinks are not supported")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return errors.New("cache contains nonregular file")
		}
		if d.IsDir() {
			if setMode {
				return os.Chmod(name, 0o700)
			}
			if info.Mode().Perm() != 0o700 {
				return errors.New("cache directories must be private (0700)")
			}
		}
		return nil
	})
}
func cached(ctx context.Context, key string, s *snapshot) (*Prepared, error) {
	if err := privateTree(key, false); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := readBounded(r, "prepared.json")
	if err != nil {
		return nil, fmt.Errorf("incomplete/corrupt prepared cache: %w", err)
	}
	var rec cacheRecord
	if err := agentsuite.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	if rec.Fingerprint != s.fingerprint || rec.Name != s.name {
		return nil, errors.New("prepared cache source identity mismatch")
	}
	p := &Prepared{filepath.Join(key, "layout"), rec.Fingerprint, rec.Descriptor, rec.Name}
	files, err := verify(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("corrupt prepared cache: %w", err)
	}
	if !equalFiles(files, s.files) {
		return nil, errors.New("prepared cache differs from selected source")
	}
	return p, nil
}

// Prepare atomically publishes a complete private cache entry. Concurrent
// candidates may duplicate work; only a verified complete winner is reused.
func Prepare(ctx context.Context, root, cacheDir string) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := safeAbsolute(root)
	if err != nil {
		return nil, err
	}
	cacheDir, err = safeAbsolute(cacheDir)
	if err != nil {
		return nil, err
	}
	if within(root, cacheDir) || within(cacheDir, root) {
		return nil, errors.New("prepared cache must be outside and not contain the workspace")
	}
	s, err := loadSource(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(cacheDir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o700 {
		return nil, errors.New("prepared cache must be private (0700)")
	}
	key := filepath.Join(cacheDir, strings.TrimPrefix(s.fingerprint, "sha256:"))
	if _, err := os.Lstat(key); err == nil {
		p, err := cached(ctx, key, s)
		if err != nil {
			return nil, err
		}
		if err := CheckCurrent(ctx, root, p); err != nil {
			return nil, err
		}
		return p, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	stage, err := os.MkdirTemp(cacheDir, ".prepare-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	source := filepath.Join(stage, "source")
	if err := writeFiles(source, s.files); err != nil {
		return nil, err
	}
	result, err := suiteoras.Push(ctx, source, filepath.Join(stage, "layout"), "prepared")
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(source); err != nil {
		return nil, err
	}
	if err := privateTree(stage, true); err != nil {
		return nil, err
	}
	p := &Prepared{result.Path, s.fingerprint, result.Descriptor, s.name}
	if err := Verify(ctx, p); err != nil {
		return nil, err
	}
	record, err := agentsuite.Marshal(cacheRecord{p.Fingerprint, p.Descriptor, p.Name})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "prepared.json"), record, 0o600); err != nil {
		return nil, err
	}
	if err := CheckCurrent(ctx, root, p); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, key); err != nil {
		if _, statErr := os.Lstat(key); statErr != nil {
			return nil, err
		}
		p, err = cached(ctx, key, s)
		if err != nil {
			return nil, err
		}
	} else {
		p.Path = filepath.Join(key, "layout")
	}
	if err := CheckCurrent(ctx, root, p); err != nil {
		return nil, err
	}
	return p, nil
}

// CheckCurrent refuses a silently substituted publication candidate. It does
// not reinterpret source identity as a PortableAgent digest or evidence.
func CheckCurrent(ctx context.Context, root string, p *Prepared) error {
	if p == nil {
		return errors.New("prepared artifact is required")
	}
	s, err := loadSource(ctx, root)
	if err != nil {
		return err
	}
	if s.fingerprint != p.Fingerprint {
		return errors.New("workspace changed since preparation; prepare again (rerun evaluation before promotion)")
	}
	return ctx.Err()
}

// Verify validates both the public OCI graph and the closed native profile.
func Verify(ctx context.Context, p *Prepared) error { _, err := verify(ctx, p); return err }
func verify(ctx context.Context, p *Prepared) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || !agentsuite.IsValidDigest(p.Fingerprint) {
		return nil, errors.New("valid prepared artifact identity is required")
	}
	root, err := safeAbsolute(p.Path)
	if err != nil {
		return nil, err
	}
	if err := privateTree(root, false); err != nil {
		return nil, err
	}
	if _, err := agentsuite.ValidatePath(root); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	b, err := readBounded(r, "index.json")
	r.Close()
	if err != nil {
		return nil, err
	}
	var index ocispec.Index
	if err := agentsuite.Unmarshal(b, &index); err != nil {
		return nil, err
	}
	if len(index.Manifests) != 1 || !content.Equal(index.Manifests[0], p.Descriptor) || index.Manifests[0].ArtifactType != p.Descriptor.ArtifactType {
		return nil, errors.New("prepared descriptor does not match layout root")
	}
	stage, err := os.MkdirTemp("", "kmx-native-verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	out := filepath.Join(stage, "suite")
	if _, err := suiteoras.Pull(ctx, root, p.Descriptor.Digest.String(), out); err != nil {
		return nil, err
	}
	extracted, err := os.OpenRoot(out)
	if err != nil {
		return nil, err
	}
	defer extracted.Close()
	files := map[string][]byte{}
	total := 0
	err = fs.WalkDir(extracted.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if len(files) >= suiteoras.MaxArtifactFiles {
			return errors.New("native artifact exceeds file count limit")
		}
		b, err := readBounded(extracted, name)
		if err != nil {
			return err
		}
		total += len(b)
		if total > 8<<20 {
			return errors.New("native artifact exceeds 8 MiB")
		}
		files[name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	name, err := validateNative(files)
	if err != nil {
		return nil, err
	}
	if name != p.Name {
		return nil, errors.New("prepared name does not match artifact")
	}
	return files, ctx.Err()
}
