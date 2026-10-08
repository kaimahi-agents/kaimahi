package oras

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// PushRegistry packages and pushes an extracted AgentSuite to an OCI registry.
func PushRegistry(
	ctx context.Context,
	source string,
	reference string,
	plainHTTP bool,
) (PushResult, error) {
	parsed, err := registry.ParseReference(reference)
	if err != nil {
		return PushResult{}, fmt.Errorf("parse AgentSuite registry reference %q: %w", reference, err)
	}
	if err := parsed.ValidateReferenceAsTag(); err != nil {
		return PushResult{}, fmt.Errorf("AgentSuite registry push requires a tag: %w", err)
	}
	repository, targetReference, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return PushResult{}, err
	}
	packedRoot, packedStore, packed, err := packDirectory(ctx, source)
	if err != nil {
		return PushResult{}, err
	}
	defer os.RemoveAll(packedRoot)
	if _, err := NewPusher(repository).Push(ctx, packedStore, packed.Descriptor, targetReference); err != nil {
		return PushResult{}, err
	}
	return PushResult{
		Path:       reference,
		Reference:  reference,
		Descriptor: packed.Descriptor,
		Report:     packed.Report,
	}, nil
}

// PullRegistry pulls and extracts an AgentSuite from an OCI registry.
func PullRegistry(
	ctx context.Context,
	reference string,
	output string,
	plainHTTP bool,
) (PullResult, error) {
	repository, targetReference, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return PullResult{}, err
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return PullResult{}, err
	}
	result, err := pullToDirectory(ctx, repository, targetReference, outputPath)
	if err != nil {
		return PullResult{}, err
	}
	result.Reference = reference
	return result, nil
}

func newRegistryRepository(
	value string,
	plainHTTP bool,
	store credentials.Store,
) (*remote.Repository, string, error) {
	parsed, err := registry.ParseReference(value)
	if err != nil {
		return nil, "", fmt.Errorf("parse AgentSuite registry reference %q: %w", value, err)
	}
	if parsed.Reference == "" {
		return nil, "", errors.New("AgentSuite registry reference must include a tag or digest")
	}
	repository, err := remote.NewRepository(parsed.Registry + "/" + parsed.Repository)
	if err != nil {
		return nil, "", fmt.Errorf("create AgentSuite registry target: %w", err)
	}
	if store == nil {
		store, err = credentials.NewStoreFromDocker(credentials.StoreOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("open Docker credential store: %w", err)
		}
	}
	client := *auth.DefaultClient
	client.Credential = credentials.Credential(store)
	repository.Client = &client
	repository.PlainHTTP = plainHTTP
	return repository, parsed.Reference, nil
}

var _ agentsuite.Target = (*remote.Repository)(nil)
