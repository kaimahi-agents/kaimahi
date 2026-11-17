package workspace

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"sync"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var errEvaluationSnapshot = errors.New("workspace evaluation snapshot is unavailable")

// GeneratedValidator validates one generated loose AgentSuite view before its
// exact bytes become an execution subject. Implementations own transient storage
// and must return only after cleanup is complete or explicitly uncertain.
type GeneratedValidator func(context.Context, map[string][]byte) error

// EvaluationSnapshot owns one exact invocation-local Orka package and its source
// fingerprint. It is not a cache, registry target, publication plan, or evidence.
type EvaluationSnapshot struct {
	mu          sync.Mutex
	root        string
	fingerprint string
	artifact    *suiteoras.GeneratedArtifact
	suite       *NativeSuite
	selected    map[string]string
	identities  map[string]os.FileInfo
	rootInfo    os.FileInfo
	closed      bool
}

// FreezeOrkaEvaluation snapshots, validates, and packages one exact native Orka
// workspace without publishing a filesystem OCI layout.
func FreezeOrkaEvaluation(ctx context.Context, root string, validate GeneratedValidator) (_ *EvaluationSnapshot, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if validate == nil {
		return nil, errEvaluationSnapshot
	}
	root, err = safeAbsolute(root)
	if err != nil {
		return nil, err
	}
	source, err := loadSource(ctx, root)
	if err != nil {
		return nil, err
	}
	if source.runtime != (RuntimeProfile{ID: "orka"}) {
		return nil, ErrRuntimeMismatch
	}
	files := cloneGeneratedFiles(source.files)
	if _, err := validateNative(files); err != nil {
		return nil, verificationFailed(err)
	}
	if err := validate(ctx, cloneGeneratedFiles(files)); err != nil {
		return nil, err
	}
	artifact, err := suiteoras.PackGeneratedFiles(ctx, files)
	if err != nil {
		return nil, err
	}
	rootInfo, identities, err := sourceIdentitySnapshot(root, source.selected)
	if err != nil {
		return nil, err
	}
	snapshot := &EvaluationSnapshot{root: root, fingerprint: source.fingerprint, artifact: artifact, selected: maps.Clone(source.selected), identities: identities, rootInfo: rootInfo}
	defer func() {
		if err != nil {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	descriptor := artifact.Descriptor()
	snapshot.suite, err = loadNativeFiles(ctx, files, recipeArtifactIdentity(descriptor))
	if err != nil {
		return nil, err
	}
	if snapshot.suite.Digest != descriptor.Digest.String() || snapshot.suite.Runtime != (RuntimeProfile{ID: "orka"}) {
		return nil, errEvaluationSnapshot
	}
	if err := errors.Join(snapshot.suite.RequireUnprofiled(), snapshot.suite.RequirePolicyFree(), artifact.Verify(ctx)); err != nil {
		return nil, err
	}
	if err := snapshot.checkCurrent(ctx); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (p *EvaluationSnapshot) Descriptor() ocispec.Descriptor {
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

func (p *EvaluationSnapshot) Fingerprint() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ""
	}
	return p.fingerprint
}

func (p *EvaluationSnapshot) Suite(ctx context.Context) (*NativeSuite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errEvaluationSnapshot
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.suite == nil || p.artifact == nil || p.artifact.Verify(ctx) != nil {
		return nil, errEvaluationSnapshot
	}
	return cloneEvaluationSuite(p.suite), nil
}

func (p *EvaluationSnapshot) CheckCurrent(ctx context.Context) error {
	if p == nil {
		return errEvaluationSnapshot
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errEvaluationSnapshot
	}
	return p.checkCurrent(ctx)
}

func (p *EvaluationSnapshot) checkCurrent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := loadSource(ctx, p.root)
	if err != nil {
		return err
	}
	if current.fingerprint != p.fingerprint || current.runtime != (RuntimeProfile{ID: "orka"}) || !maps.Equal(current.selected, p.selected) {
		return errors.New("workspace changed since preparation; prepare again (rerun evaluation before promotion)")
	}
	rootInfo, identities, err := sourceIdentitySnapshot(p.root, current.selected)
	if err != nil || !os.SameFile(p.rootInfo, rootInfo) || len(identities) != len(p.identities) {
		return errors.New("workspace changed since preparation; prepare again (rerun evaluation before promotion)")
	}
	for name, before := range p.identities {
		now := identities[name]
		if now == nil || !os.SameFile(before, now) || before.Size() != now.Size() || before.Mode() != now.Mode() || !before.ModTime().Equal(now.ModTime()) {
			return errors.New("workspace changed since preparation; prepare again (rerun evaluation before promotion)")
		}
	}
	if p.artifact == nil || p.artifact.Verify(ctx) != nil {
		return errEvaluationSnapshot
	}
	return ctx.Err()
}

func (p *EvaluationSnapshot) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.suite = nil
	p.fingerprint = ""
	p.selected = nil
	p.identities = nil
	p.rootInfo = nil
	if p.artifact == nil {
		return nil
	}
	err := p.artifact.Close()
	p.artifact = nil
	return err
}

func sourceIdentitySnapshot(root string, selected map[string]string) (os.FileInfo, map[string]os.FileInfo, error) {
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer handle.Close()
	rootInfo, err := handle.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	identities := make(map[string]os.FileInfo, len(selected))
	for name := range selected {
		if err := confined(handle, name); err != nil {
			return nil, nil, err
		}
		before, err := handle.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			return nil, nil, err
		}
		file, err := handle.Open(name)
		if err != nil {
			return nil, nil, err
		}
		opened, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			return nil, nil, errors.New("workspace source changed while freezing")
		}
		identities[name] = opened
	}
	return rootInfo, identities, nil
}

func cloneGeneratedFiles(files map[string][]byte) map[string][]byte {
	cloned := make(map[string][]byte, len(files))
	for name, raw := range files {
		cloned[name] = bytes.Clone(raw)
	}
	return cloned
}

func cloneEvaluationSuite(suite *NativeSuite) *NativeSuite {
	if suite == nil {
		return nil
	}
	cloned := *suite
	cloned.Instructions = bytes.Clone(suite.Instructions)
	cloned.RequiredImages = append([]string(nil), suite.RequiredImages...)
	cloned.Cases = make([]PackagedCase, len(suite.Cases))
	for i, item := range suite.Cases {
		cloned.Cases[i] = item
		cloned.Cases[i].Bytes = bytes.Clone(item.Bytes)
	}
	if suite.Policy != nil {
		policy := *suite.Policy
		policy.Bytes = bytes.Clone(suite.Policy.Bytes)
		cloned.Policy = &policy
	}
	return &cloned
}
