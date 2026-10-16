package oras

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
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
	force bool,
	options PushOptions,
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
	result := PushResult{
		Path:       reference,
		Reference:  reference,
		Descriptor: packed.Descriptor,
		Report:     packed.Report,
	}
	return pushRegistryGraph(ctx, packedStore, repository, targetReference, force, options, result)
}

// pushRegistryGraph publishes either a packed directory or a prepared graph.
// Provenance must attach before tagging, independent of how the graph was made.
func pushRegistryGraph(ctx context.Context, source kmxsuite.ReadOnlyStorage, repository *remote.Repository, targetReference string, force bool, options PushOptions, result PushResult) (PushResult, error) {
	// This preflight is not atomic with the subsequent tag update.
	existing, err := repository.Resolve(ctx, targetReference)
	alreadyTagged := false
	if err == nil {
		alreadyTagged = existing.Digest == result.Descriptor.Digest
		if !alreadyTagged && !force {
			return PushResult{}, fmt.Errorf("AgentSuite registry tag %q already points to %s; refusing to replace it with %s without --force", result.Reference, existing.Digest, result.Descriptor.Digest)
		}
	} else if !errors.Is(err, errdef.ErrNotFound) {
		return PushResult{}, fmt.Errorf("check existing AgentSuite registry tag %q: %w", result.Reference, err)
	}
	var artifact *provenanceArtifact
	if options.Provenance != nil {
		built, err := buildProvenance(result.Descriptor, result.Report.Name, *options.Provenance)
		if err != nil {
			return PushResult{}, err
		}
		artifact = &built
	}
	if alreadyTagged {
		// Write nothing if pull already sees kmx provenance.
		if artifact != nil {
			if found, _, err := provenanceFor(ctx, repository, result.Descriptor); err == nil && len(found) > 0 {
				result.Provenance = &found[0]
				return result, nil
			}
			if err := attachOrWarn(ctx, repository, *artifact, options.Provenance.Require, &result); err != nil {
				return PushResult{}, err
			}
		}
		return result, nil
	}
	// Tag last, so a required provenance failure leaves the tag alone.
	if err := oraslib.CopyGraph(ctx, source, repository, result.Descriptor, oraslib.DefaultCopyGraphOptions); err != nil {
		return PushResult{}, fmt.Errorf("push AgentSuite %q: %w", result.Reference, err)
	}
	if artifact != nil {
		if err := attachOrWarn(ctx, repository, *artifact, options.Provenance.Require, &result); err != nil {
			return PushResult{}, err
		}
	}
	if err := repository.Tag(ctx, result.Descriptor, targetReference); err != nil {
		return PushResult{}, fmt.Errorf("tag AgentSuite %q: %w", result.Reference, err)
	}
	return result, nil
}

func attachOrWarn(
	ctx context.Context,
	dst kmxsuite.Storage,
	artifact provenanceArtifact,
	require bool,
	result *PushResult,
) error {
	if err := pushProvenance(ctx, dst, artifact); err != nil {
		if require {
			return fmt.Errorf("%w; the tag was not changed", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("AgentSuite provenance was not attached: %v", err))
		return nil
	}
	result.Provenance = &artifact.summary
	return nil
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
	repository.PlainHTTP = plainHTTP
	repository.ReferrerListMaxPages = 4
	repository.MaxMetadataBytes = maxProvenanceObjectBytes
	// Deleting old referrer indexes fails on registries without DELETE.
	repository.SkipReferrersGC = true
	anonymous := plainHTTP && !isLoopbackHost((&url.URL{Host: parsed.Registry}).Hostname())
	client := *auth.DefaultClient
	httpClient := *client.Client
	// ORAS auth installs a redirect callback, bypassing net/http's default
	// ten-redirect limit. Keep that bound even with a permissive upstream policy.
	checkRedirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		return nil
	}
	httpClient.Transport = &registryTransport{base: httpClient.Transport, anonymous: anonymous}
	if anonymous {
		// Do not open Docker configuration or use auth machinery: even an
		// anonymous bearer challenge could produce an Authorization header.
		repository.Client = &anonymousRegistryClient{Client: &httpClient}
		return repository, parsed.Reference, nil
	}
	if store == nil {
		store, err = credentials.NewStoreFromDocker(credentials.StoreOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("open Docker credential store: %w", err)
		}
	}
	client.Client = &httpClient
	client.Cache = auth.NewCache()
	client.Credential = credentials.Credential(store)
	repository.Client = &client
	return repository, parsed.Reference, nil
}

type anonymousRegistryClient struct {
	*http.Client
}

func (c *anonymousRegistryClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.Client.Do(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, errors.New("registry requires authentication; refusing authentication over plain HTTP to a non-loopback host; use HTTPS")
	}
	return resp, err
}

// registryTransport guards each outgoing request, including redirects and
// OAuth POSTs whose credentials are in the body rather than an auth header.
// Auth-capable clients refuse all remote HTTP requests, so credential bodies
// never need to be inspected or consumed.
type registryTransport struct {
	base      http.RoundTripper
	anonymous bool
}

func (t *registryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	remoteHTTP := req.URL.Scheme == "http" && !isLoopbackHost(req.URL.Hostname())
	if (remoteHTTP && !t.anonymous) || (t.anonymous && len(req.Header.Values("Authorization")) != 0) {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("refusing credentials over plain HTTP to non-loopback host %q; use HTTPS for authenticated registry access", req.URL.Host)
	}
	// ORAS sets the generated index's known size but does not bound its PUT.
	// Refuse growth before replacing an index that our own pull can still read.
	if req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/manifests/") && req.Header.Get("Content-Type") == ocispec.MediaTypeImageIndex && req.ContentLength > maxProvenanceObjectBytes {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("registry index size %d exceeds %d bytes: %w", req.ContentLength, maxProvenanceObjectBytes, errdef.ErrSizeExceedsLimit)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var _ kmxsuite.Target = (*remote.Repository)(nil)
