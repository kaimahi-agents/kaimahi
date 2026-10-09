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

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
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
	// This preflight is not atomic with the subsequent tag update.
	existing, err := repository.Resolve(ctx, targetReference)
	if err == nil {
		if existing.Digest == packed.Descriptor.Digest {
			return result, nil
		}
		if !force {
			return PushResult{}, fmt.Errorf("AgentSuite registry tag %q already points to %s; refusing to replace it with %s without --force", reference, existing.Digest, packed.Descriptor.Digest)
		}
	} else if !errors.Is(err, errdef.ErrNotFound) {
		return PushResult{}, fmt.Errorf("check existing AgentSuite registry tag %q: %w", reference, err)
	}
	if _, err := NewPusher(repository).Push(ctx, packedStore, packed.Descriptor, targetReference); err != nil {
		return PushResult{}, err
	}
	return result, nil
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
	anonymous := plainHTTP && !isLoopbackHost((&url.URL{Host: parsed.Registry}).Hostname())
	client := *auth.DefaultClient
	httpClient := *client.Client
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

var _ agentsuite.Target = (*remote.Repository)(nil)
