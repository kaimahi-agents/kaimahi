package oras_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	orasbinding "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

const testReference = "v1"

func TestORASStoresSatisfyAgentSuiteContracts(t *testing.T) {
	var _ agentsuite.Storage = memory.New()
	var _ agentsuite.Target = memory.New()
	var _ oraslib.Target = memory.New()
}

func TestPusherCopiesGraphAndAssignsReference(t *testing.T) {
	ctx := context.Background()
	src, root := newArtifact(t)
	dst := memory.New()

	got, err := orasbinding.NewPusher(dst).Push(ctx, src, root, testReference)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(got, root) {
		t.Fatalf("Push() root = %+v, want %+v", got, root)
	}
	resolved, err := dst.Resolve(ctx, testReference)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(resolved, root) {
		t.Fatalf("resolved root = %+v, want %+v", resolved, root)
	}
	assertGraphExists(t, dst, root)
}

func TestPullerResolvesReferenceAndCopiesGraph(t *testing.T) {
	ctx := context.Background()
	src, root := newArtifact(t)
	if err := src.Tag(ctx, root, testReference); err != nil {
		t.Fatal(err)
	}
	dst := memory.New()

	got, err := orasbinding.NewPuller(src).Pull(ctx, testReference, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(got, root) {
		t.Fatalf("Pull() root = %+v, want %+v", got, root)
	}
	assertGraphExists(t, dst, root)
	if _, err := dst.Resolve(ctx, testReference); err == nil {
		t.Fatal("Pull() assigned a destination reference")
	}
}

func TestTransferContractsAcceptNonORASCAS(t *testing.T) {
	ctx := context.Background()
	src, root := newArtifact(t)
	fakeSource := newFakeStorage()

	if _, err := orasbinding.NewPusher(memory.New()).Push(ctx, fakeSource, root, testReference); err == nil {
		t.Fatal("Push() succeeded without source content")
	}
	if err := copyIntoFake(ctx, src, fakeSource, root); err != nil {
		t.Fatal(err)
	}
	if _, err := orasbinding.NewPusher(memory.New()).Push(ctx, fakeSource, root, testReference); err != nil {
		t.Fatal(err)
	}
	if err := src.Tag(ctx, root, testReference); err != nil {
		t.Fatal(err)
	}
	fakeDestination := newFakeStorage()
	if _, err := orasbinding.NewPuller(src).Pull(ctx, testReference, fakeDestination); err != nil {
		t.Fatal(err)
	}
	assertGraphExists(t, fakeDestination, root)
}

func TestPusherPropagatesTagFailure(t *testing.T) {
	src, root := newArtifact(t)
	want := errors.New("tag rejected")
	dst := &failingTagTarget{Target: memory.New(), err: want}

	_, err := orasbinding.NewPusher(dst).Push(context.Background(), src, root, testReference)
	if !errors.Is(err, want) {
		t.Fatalf("Push() error = %v, want tag failure", err)
	}
}

func TestTransfersRejectMissingBindings(t *testing.T) {
	ctx := context.Background()
	src, root := newArtifact(t)
	if _, err := orasbinding.NewPusher(nil).Push(ctx, src, root, testReference); err == nil {
		t.Fatal("Push() with nil destination succeeded")
	}
	if _, err := orasbinding.NewPuller(nil).Pull(ctx, testReference, memory.New()); err == nil {
		t.Fatal("Pull() with nil source succeeded")
	}
}

func newArtifact(t *testing.T) (*memory.Store, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	config := []byte("{}")
	configDescriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeEmptyConfig, config)
	if err := store.Push(ctx, configDescriptor, bytes.NewReader(config)); err != nil {
		t.Fatal(err)
	}
	layer := []byte("AgentSuite content")
	layerDescriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, layer)
	if err := store.Push(ctx, layerDescriptor, bytes.NewReader(layer)); err != nil {
		t.Fatal(err)
	}
	root, err := oraslib.PackManifest(
		ctx,
		store,
		oraslib.PackManifestVersion1_1,
		agentsuite.MediaTypeArtifact,
		oraslib.PackManifestOptions{
			Layers:           []ocispec.Descriptor{layerDescriptor},
			ConfigDescriptor: &configDescriptor,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func assertGraphExists(t *testing.T, store agentsuite.ReadOnlyStorage, root ocispec.Descriptor) {
	t.Helper()
	exists, err := store.Exists(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("root %s does not exist", root.Digest)
	}
}

type failingTagTarget struct {
	oraslib.Target
	err error
}

func (t *failingTagTarget) Tag(context.Context, ocispec.Descriptor, string) error {
	return t.err
}

type fakeStorage struct {
	mu      sync.RWMutex
	content map[string][]byte
}

var _ agentsuite.Storage = (*fakeStorage)(nil)

func newFakeStorage() *fakeStorage {
	return &fakeStorage{content: map[string][]byte{}}
}

func (s *fakeStorage) Fetch(_ context.Context, descriptor ocispec.Descriptor) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.content[descriptor.Digest.String()]
	if !ok {
		return nil, errors.New("content not found")
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(data))), nil
}

func (s *fakeStorage) Push(_ context.Context, descriptor ocispec.Descriptor, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if int64(len(data)) != descriptor.Size {
		return errors.New("content size does not match descriptor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content[descriptor.Digest.String()] = data
	return nil
}

func (s *fakeStorage) Exists(_ context.Context, descriptor ocispec.Descriptor) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.content[descriptor.Digest.String()]
	return ok, nil
}

func copyIntoFake(
	ctx context.Context,
	src agentsuite.ReadOnlyStorage,
	dst agentsuite.Storage,
	root ocispec.Descriptor,
) error {
	return oraslib.CopyGraph(ctx, src, dst, root, oraslib.DefaultCopyGraphOptions)
}
