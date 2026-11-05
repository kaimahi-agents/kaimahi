package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var errCompiledUnavailable = errors.New("compiled package unavailable")

// CompiledPackage owns invocation-local verified bytes, not a retained handle.
// Publication destinations are caller-owned, create-only outputs. Place them
// below an owned parent to contain sibling ORAS staging and exclusion directories;
// Close removes only this compiler's staging, never an external copied layout.
// Close consumes the object even on error. Source content is non-confidential;
// separately bounded ORAS OS-temp copies may survive abrupt termination.
type CompiledPackage struct {
	mu                    sync.Mutex
	stage                 string
	parent, root          *os.Root
	parentInfo, stageInfo os.FileInfo
	artifact              *suiteoras.Artifact
	fingerprint           string
	closed                bool
}

// CompileMapped compiles copied semantic inputs through the existing resolver
// and disk-staged packager. stageParent is explicit private resource placement,
// never source/config discovery or package identity.
func CompileMapped(ctx context.Context, input MappedInput, stageParent string) (result *CompiledPackage, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	frozen, err := freezeMapped(input)
	if err != nil {
		return nil, err
	}
	snapshot, err := resolveMapped(ctx, frozen)
	if err != nil {
		return nil, err
	}
	parentPath, err := safeAbsolute(stageParent)
	if err != nil {
		return nil, errCompiledUnavailable
	}
	if err = privateTree(parentPath, false); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, errCompiledUnavailable
	}
	p := &CompiledPackage{parent: parent, fingerprint: snapshot.fingerprint}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	p.parentInfo, err = parent.Stat(".")
	if err != nil {
		return nil, errCompiledUnavailable
	}
	p.stage, err = os.MkdirTemp(parentPath, ".kmx-compile-*")
	if err != nil {
		return nil, errCompiledUnavailable
	}
	p.stageInfo, err = parent.Lstat(filepath.Base(p.stage))
	if err != nil {
		return nil, errCompiledUnavailable
	}
	p.root, err = parent.OpenRoot(filepath.Base(p.stage))
	if err != nil {
		return nil, errCompiledUnavailable
	}
	graph := filepath.Join(p.stage, "graph")
	if err = writeFiles(graph, snapshot.files); err != nil {
		return nil, errCompiledUnavailable
	}
	packed, err := suiteoras.Push(ctx, graph, filepath.Join(p.stage, "layout"), "compiled")
	if err != nil {
		return nil, err
	}
	p.artifact, err = suiteoras.ReadArtifactLayout(ctx, packed.Path, packed.Descriptor.Digest.String())
	if err != nil {
		return nil, err
	}
	if _, err = LoadArtifact(ctx, p.artifact); err != nil {
		return nil, err
	}
	if err = p.root.RemoveAll("graph"); err != nil {
		return nil, errCompiledUnavailable
	}
	if err = privateTree(p.stage, true); err != nil {
		return nil, errCompiledUnavailable
	}
	if _, err = p.current(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *CompiledPackage) checkIdentity() error {
	if p == nil || p.parent == nil || p.stageInfo == nil {
		return errCompiledUnavailable
	}
	parent, err := os.Lstat(filepath.Dir(p.stage))
	if err != nil || !parent.IsDir() || !os.SameFile(parent, p.parentInfo) {
		return errCompiledUnavailable
	}
	info, err := p.parent.Lstat(filepath.Base(p.stage))
	if err != nil || !info.IsDir() || !os.SameFile(info, p.stageInfo) {
		return errCompiledUnavailable
	}
	if p.root != nil {
		if _, err := p.root.Stat("."); err != nil {
			return errCompiledUnavailable
		}
	}
	return nil
}

// current is called only while the use is exclusive (or before publication).
func (p *CompiledPackage) current(ctx context.Context) (*suiteoras.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.closed || p.artifact == nil {
		return nil, errCompiledUnavailable
	}
	if err := p.checkIdentity(); err != nil {
		return nil, err
	}
	artifact, err := suiteoras.ReadArtifactLayout(ctx, filepath.Join(p.stage, "layout"), p.artifact.Descriptor().Digest.String())
	if err != nil {
		return nil, err
	}
	if _, err = LoadArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	fingerprint, err := MappedArtifactFingerprint(ctx, artifact)
	if err != nil || fingerprint != p.fingerprint {
		return nil, errCompiledUnavailable
	}
	if err = p.checkIdentity(); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (p *CompiledPackage) Descriptor() ocispec.Descriptor {
	if p == nil {
		return ocispec.Descriptor{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.artifact == nil {
		return ocispec.Descriptor{}
	}
	return p.artifact.Descriptor()
}
func (p *CompiledPackage) Fingerprint() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.artifact == nil {
		return ""
	}
	return p.fingerprint
}
func (p *CompiledPackage) Load(ctx context.Context) (*NativeSuite, error) {
	if p == nil {
		return nil, errCompiledUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	artifact, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	return LoadArtifact(ctx, artifact)
}
func (p *CompiledPackage) Sources(ctx context.Context) (SourceBundle, error) {
	if p == nil {
		return SourceBundle{}, errCompiledUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	artifact, err := p.current(ctx)
	if err != nil {
		return SourceBundle{}, err
	}
	return ReadSources(ctx, artifact)
}
func (p *CompiledPackage) CopyToLayout(ctx context.Context, destination string) (ocispec.Descriptor, error) {
	if p == nil {
		return ocispec.Descriptor{}, errCompiledUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	artifact, err := p.current(ctx)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	result, err := suiteoras.PushLayoutCreateOnly(ctx, filepath.Join(p.stage, "layout"), destination, "compiled", artifact.Descriptor())
	if err != nil {
		return result.Descriptor, err
	}
	// Acknowledged output is retained if a late source/close check fails.
	if _, err = p.current(ctx); err != nil {
		return result.Descriptor, err
	}
	return result.Descriptor, nil
}
func (p *CompiledPackage) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.stage != "" {
		err = p.checkIdentity()
	}
	if p.root != nil {
		err = errors.Join(err, p.root.Close())
	}
	if err == nil && p.stage != "" && p.parent != nil {
		err = p.parent.RemoveAll(filepath.Base(p.stage))
	}
	if p.parent != nil {
		err = errors.Join(err, p.parent.Close())
	}
	p.artifact = nil
	return err
}
